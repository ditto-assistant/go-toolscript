package toolscript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"unicode/utf16"
)

// Undefined represents an absent JS value. Marshal omits it in objects and
// encodes it as null in arrays and at the top level.
var Undefined any = undefined{}

type undefined struct{}

// Object preserves hosts whose property view differs from their JSON export
// (for example, a Go struct with omitzero fields exposed through Goja).
// Fields and Export must contain only immutable JSON data.
type Object struct {
	Fields map[string]any
	Export any
}

// Dispatch is the only capability exposed to a plan. Arguments/results must
// be JSON-compatible Go values (float64 numbers), or Undefined. Implementations
// must honor context cancellation; with Parallelism > 1 they must be thread safe.
type Dispatch func(context.Context, string, any) (any, error)

// ExecuteOptions bounds each execution independently. Zero values use defaults.
type ExecuteOptions struct {
	Dispatch    Dispatch
	Fatal       func(error) bool
	MaxCalls    int // default 64
	MaxSteps    int // default 100000
	MaxItems    int // default 1024 per array map/batch
	Parallelism int // default 1; maximum 64
}

// Result retains the call count and fatal status even after execution fails.
type Result struct {
	Value   any
	Calls   int
	Aborted bool
}
type execution struct {
	opts    ExecuteOptions
	calls   atomic.Int64
	steps   atomic.Int64
	aborted atomic.Bool
	fatalMu sync.Mutex
	fatal   error
}
type environment map[string]any

// Execute runs a validated program. It never returns ErrUnsupported and never
// retries a call. Parallel batches join all workers before returning, including
// on errors; callers needing a hard deadline must retain admission until exit.
func (p *Program) Execute(ctx context.Context, opts ExecuteOptions) (Result, error) {
	if opts.MaxCalls <= 0 {
		opts.MaxCalls = 64
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 100000
	}
	if opts.MaxItems <= 0 {
		opts.MaxItems = 1024
	}
	if opts.Parallelism <= 0 {
		opts.Parallelism = 1
	}
	if opts.Parallelism > 64 {
		opts.Parallelism = 64
	}
	x := &execution{opts: opts}
	v, err := p.run(ctx, x, environment{})
	if x.steps.Load() > int64(opts.MaxSteps) {
		err = errors.New("step limit exceeded")
	}
	if x.fatal != nil {
		err = x.fatal
		v = Undefined
	}
	return Result{Value: v, Calls: int(x.calls.Load()), Aborted: x.aborted.Load()}, err
}
func (x *execution) tick(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if x.aborted.Load() {
		return errors.New("tool execution aborted")
	}
	if x.steps.Add(1) > int64(x.opts.MaxSteps) {
		return errors.New("step limit exceeded")
	}
	return nil
}
func (p *Program) run(ctx context.Context, x *execution, env environment) (any, error) {
	for _, s := range p.statements {
		v, err := s.value.eval(ctx, x, env)
		if err != nil {
			return nil, err
		}
		if s.ret {
			return v, nil
		}
		for _, b := range s.bindings {
			val := v
			for _, key := range b.keys {
				if text, ok := val.(string); ok && key.iterable {
					runes := []rune(text)
					index, _ := strconv.Atoi(key.key)
					if index < len(runes) {
						val = string(runes[index])
					} else {
						val = Undefined
					}
				} else {
					val, err = member(val, key.key)
				}
				if err != nil {
					return nil, err
				}
			}
			if b.require != "" {
				if val == nil {
					return nil, errors.New("cannot destructure null")
				}
				if _, ok := val.(undefined); ok {
					return nil, errors.New("cannot destructure undefined")
				}
				if b.require == "array" {
					if _, ok := val.([]any); !ok {
						if _, str := val.(string); str {
							continue
						}
						return nil, errors.New("array binding requires an array")
					}
				}
			} else {
				env[b.name] = val
			}
		}
	}
	return Undefined, nil
}
func (e *expr) eval(ctx context.Context, x *execution, env environment) (any, error) {
	if err := x.tick(ctx); err != nil {
		return nil, err
	}
	switch e.kind {
	case "literal":
		return e.value, nil
	case "ref":
		return env[e.name], nil
	case "member":
		v, err := e.children[0].eval(ctx, x, env)
		if err != nil {
			return nil, err
		}
		return member(v, e.name)
	case "array":
		out := make([]any, len(e.children))
		for i, v := range e.children {
			a, err := v.eval(ctx, x, env)
			if err != nil {
				return nil, err
			}
			out[i] = a
		}
		return out, nil
	case "object":
		out := make(map[string]any, len(e.fields))
		for _, f := range e.fields {
			a, err := f.value.eval(ctx, x, env)
			if err != nil {
				return nil, err
			}
			out[f.name] = a
		}
		return out, nil
	case "call":
		arg, err := e.children[0].eval(ctx, x, env)
		if err != nil {
			return nil, err
		}
		return x.call(ctx, e.name, arg)
	case "map":
		values, err := e.mapValues(ctx, x, env)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(values))
		for i, v := range values {
			a, err := e.body.run(ctx, x, e.mapEnv(env, v, i))
			if err != nil {
				return nil, err
			}
			out[i] = a
		}
		return out, nil
	case "all", "allSettled":
		return e.batch(ctx, x, env)
	}
	return nil, errors.New("invalid execution plan")
}
func (x *execution) call(ctx context.Context, name string, arg any) (value any, err error) {
	// Also contains host panics inside batch goroutines.
	defer func() {
		if r := recover(); r != nil {
			value = nil
			err = fmt.Errorf("host panic: %v", r)
		}
	}()
	if err = x.tick(ctx); err != nil {
		return nil, err
	}
	for {
		n := x.calls.Load()
		if n >= int64(x.opts.MaxCalls) {
			return nil, fmt.Errorf("tool-call limit (%d) exceeded", x.opts.MaxCalls)
		}
		if x.calls.CompareAndSwap(n, n+1) {
			break
		}
	}
	if x.opts.Dispatch == nil {
		return nil, errors.New("no dispatcher configured")
	}
	value, err = x.opts.Dispatch(ctx, name, arg)
	if err != nil && x.opts.Fatal != nil && x.opts.Fatal(err) {
		x.fatalMu.Lock()
		if x.fatal == nil {
			x.fatal = err
		}
		x.aborted.Store(true)
		x.fatalMu.Unlock()
	}
	return value, err
}
func (e *expr) mapValues(ctx context.Context, x *execution, env environment) ([]any, error) {
	v, err := e.children[0].eval(ctx, x, env)
	if err != nil {
		return nil, err
	}
	values, ok := v.([]any)
	if !ok {
		return nil, errors.New("map receiver is not an array")
	}
	if len(values) > x.opts.MaxItems {
		return nil, errors.New("map item limit exceeded")
	}
	return values, nil
}
func (e *expr) mapEnv(env environment, v any, i int) environment {
	out := make(environment, len(env)+2)
	for k, a := range env {
		out[k] = a
	}
	if len(e.params) > 0 {
		out[e.params[0]] = v
	}
	if len(e.params) > 1 {
		out[e.params[1]] = float64(i)
	}
	return out
}
func (e *expr) batch(ctx context.Context, x *execution, env environment) (any, error) {
	arg := e.children[0]
	size := len(arg.children)
	var values []any
	if arg.kind == "map" {
		var err error
		values, err = arg.mapValues(ctx, x, env)
		if err != nil {
			return nil, err
		}
		size = len(values)
	}
	if size > x.opts.MaxItems {
		return nil, errors.New("batch item limit exceeded")
	}
	out := make([]any, size)
	errs := make([]error, size)
	var next atomic.Int64
	var wg sync.WaitGroup
	work := func() {
		defer wg.Done()
		for {
			i := int(next.Add(1) - 1)
			if i >= size {
				return
			}
			var v any
			var err error
			if arg.kind == "map" {
				v, err = arg.body.run(ctx, x, arg.mapEnv(env, values[i], i))
			} else {
				v, err = arg.children[i].eval(ctx, x, env)
			}
			if e.kind == "allSettled" && err == nil {
				out[i] = map[string]any{"status": "fulfilled", "value": v}
			} else if e.kind == "allSettled" {
				out[i] = map[string]any{"status": "rejected", "reason": err.Error()}
			} else {
				out[i] = v
				errs[i] = err
			}
		}
	}
	for range min(size, x.opts.Parallelism) {
		wg.Add(1)
		go work()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func member(v any, key string) (any, error) {
	switch v := v.(type) {
	case nil, undefined:
		return nil, fmt.Errorf("cannot read property %q of null or undefined", key)
	case *Object:
		return member(v.Fields, key)
	case map[string]any:
		a, ok := v[key]
		if !ok {
			return Undefined, nil
		}
		return a, nil
	case []any:
		if key == "length" {
			return float64(len(v)), nil
		}
		i, err := strconv.Atoi(key)
		if err == nil && i >= 0 && i < len(v) && strconv.Itoa(i) == key {
			return v[i], nil
		}
		return Undefined, nil
	case string:
		units := utf16.Encode([]rune(v))
		if key == "length" {
			return float64(len(units)), nil
		}
		i, err := strconv.Atoi(key)
		if err == nil && i >= 0 && i < len(units) && strconv.Itoa(i) == key {
			return string(utf16.Decode([]uint16{units[i]})), nil
		}
		return Undefined, nil
	default:
		return Undefined, nil
	}
}

// Marshal converts JSON data plus Undefined to JSON with bounded traversal.
// It prevents exponential expansion through repeated references in a plan.
func Marshal(v any) ([]byte, error) {
	nodes := 0
	clean, err := jsonValue(v, 0, &nodes, true)
	if err != nil {
		return nil, err
	}
	return json.Marshal(clean)
}
func jsonValue(v any, depth int, nodes *int, omitUndefined bool) (any, error) {
	*nodes++
	if *nodes > 100000 || depth > 64 {
		return nil, errors.New("JSON value limit exceeded")
	}
	switch v := v.(type) {
	case undefined:
		return nil, nil
	case *Object:
		return jsonValue(v.Export, depth+1, nodes, omitUndefined)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, a := range v {
			if _, ok := a.(undefined); ok && omitUndefined {
				continue
			}
			b, err := jsonValue(a, depth+1, nodes, omitUndefined)
			if err != nil {
				return nil, err
			}
			out[k] = b
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, a := range v {
			b, err := jsonValue(a, depth+1, nodes, omitUndefined)
			if err != nil {
				return nil, err
			}
			out[i] = b
		}
		return out, nil
	case nil, string, bool, float64:
		return v, nil
	default:
		return nil, fmt.Errorf("non-JSON host value %T", v)
	}
}

// MarshalExport matches Goja Value.Export followed by encoding/json, including
// undefined object properties encoded as null (unlike JSON.stringify).
func MarshalExport(v any) ([]byte, error) {
	nodes := 0
	clean, err := jsonValue(v, 0, &nodes, false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(clean)
}
