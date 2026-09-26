package toolscript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// corpusCase is one script from testdata/corpus (txtar) or an external JSONL.
type corpusCase struct {
	divergence string // documented, intentional difference from Goja
	name       string
	script     string
	tools      map[string]json.RawMessage
	bash       map[string]json.RawMessage
	workspace  map[string]json.RawMessage
	generic    bool // unknown tools answer with a generic fixture
}

func parseTxtar(data []byte) map[string][]byte {
	files := map[string][]byte{}
	var name string
	var cur bytes.Buffer
	flush := func() {
		if name != "" {
			files[name] = append([]byte(nil), cur.Bytes()...)
		}
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\n")
		if strings.HasPrefix(trimmed, "-- ") && strings.HasSuffix(trimmed, " --") && len(trimmed) > 6 {
			flush()
			name = strings.TrimSpace(trimmed[3 : len(trimmed)-3])
			continue
		}
		if name != "" {
			cur.WriteString(line)
		}
	}
	flush()
	return files
}

func loadCorpus(t testing.TB) []corpusCase {
	var cases []corpusCase
	paths, _ := filepath.Glob("testdata/corpus/*/*.txtar")
	sort.Strings(paths)
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files := parseTxtar(data)
		c := corpusCase{name: strings.TrimSuffix(strings.TrimPrefix(p, "testdata/corpus/"), ".txtar"), script: string(files["script.js"]), divergence: strings.TrimSpace(string(files["divergence"]))}
		for section, into := range map[string]*map[string]json.RawMessage{"tools.json": &c.tools, "bash.json": &c.bash, "workspace.json": &c.workspace} {
			if raw := files[section]; len(bytes.TrimSpace(raw)) > 0 {
				if err := json.Unmarshal(raw, into); err != nil {
					t.Fatalf("%s %s: %v", p, section, err)
				}
			}
		}
		cases = append(cases, c)
	}
	// Optional private corpus (not committed): one {"code": "..."} per line.
	if path := os.Getenv("TOOLSCRIPT_CORPUS_JSONL"); path != "" {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for i := 0; sc.Scan(); i++ {
			var rec struct{ Code string }
			if json.Unmarshal(sc.Bytes(), &rec) == nil && strings.TrimSpace(rec.Code) != "" {
				c := corpusCase{name: fmt.Sprintf("jsonl/%04d", i), script: rec.Code, generic: true, tools: map[string]json.RawMessage{}}
				for _, name := range scanToolNames(rec.Code) {
					c.tools[name] = json.RawMessage("null")
				}
				cases = append(cases, c)
			}
		}
	}
	return cases
}

var genericResult = `{"items":[{"id":"a1","score":0.9,"summary":"alpha result","title":"Alpha","url":"https://example.com/a"},{"id":"b2","score":0.4,"summary":"beta result","title":"Beta","url":"https://example.com/b"}],"next_cursor":null,"ok":true,"results":[{"id":"r1","text":"first"},{"id":"r2","text":"second"}],"total":2}`

// fixture resolves a tool's scripted response. Calls advance $sequence.
type fixture struct {
	c     *corpusCase
	calls map[string]int
}

func (f *fixture) respond(name string, args any) (any, error) {
	raw, ok := f.c.tools[name]
	if !ok || string(raw) == "null" {
		if f.c.generic {
			raw = json.RawMessage(genericResult)
		} else {
			return map[string]any{"tool": name, "args": args}, nil
		}
	}
	var special struct {
		Sequence []json.RawMessage `json:"$sequence"`
		Error    *string           `json:"$error"`
	}
	if json.Unmarshal(raw, &special) == nil {
		if special.Error != nil {
			return nil, errors.New(*special.Error)
		}
		if len(special.Sequence) > 0 {
			i := min(f.calls[name], len(special.Sequence)-1)
			f.calls[name]++
			raw = special.Sequence[i]
		}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func (f *fixture) bash(script string) any {
	if raw, ok := f.c.bash[script]; ok {
		var v any
		_ = json.Unmarshal(raw, &v)
		return v
	}
	return map[string]any{"stdout": "", "stderr": "", "exit_code": float64(0), "json": nil}
}

func (f *fixture) workspace(name, ref string) any {
	var v any
	raw, ok := f.c.workspace[name]
	if !ok {
		if name == "workspace_files" {
			return []any{}
		}
		return map[string]any{"path": ref, "ref": ref}
	}
	if name == "mount_result" {
		var byRef map[string]json.RawMessage
		if json.Unmarshal(raw, &byRef) == nil {
			if r, ok := byRef[ref]; ok {
				raw = r
			} else {
				return map[string]any{"path": ref, "ref": ref}
			}
		}
	}
	_ = json.Unmarshal(raw, &v)
	return v
}

// outcome is everything observable from one run.
type outcome struct {
	async   bool // microtask interleaving makes call order unobservable
	ret     string
	err     string
	console []string
	calls   []string
}

func toolName(path []string) (string, bool) {
	if len(path) < 2 || (path[0] != "mcp" && path[0] != "tools") {
		return "", false
	}
	return strings.Join(path[1:], "."), true
}

func (c *corpusCase) sortedTools() []string {
	names := make([]string, 0, len(c.tools))
	for name := range c.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *corpusCase) compileOptions() CompileOptions {
	bindings := [][]string{}
	for _, name := range c.sortedTools() {
		server, local, nested := strings.Cut(name, ".")
		if nested {
			bindings = append(bindings, []string{server, local})
		} else {
			bindings = append(bindings, []string{name})
		}
	}
	return CompileOptions{
		Bindings:      bindings,
		Batches: true,
		HostFunctions: map[string]HostFunction{
			"bash":            {MinArgs: 1, MaxArgs: 3, StringArgs: []int{0}, JSONArgs: []int{1, 2}},
			"mount_result":    {MinArgs: 1, MaxArgs: 1, StringArgs: []int{0}},
			"workspace_files": {MaxArgs: 0},
		},
		ResolveCall: func(name string) (string, bool) {
			_, ok := c.tools[name]
			return name, ok
		},
		Resolve: func(path []string) (string, bool) {
			name, ok := toolName(path)
			if !ok {
				return "", false
			}
			_, known := c.tools[name]
			return name, known
		},
	}
}

func canonicalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func runNative(c *corpusCase, p *Program) outcome {
	var out outcome
	fx := &fixture{c: c, calls: map[string]int{}}
	res, err := p.Execute(context.Background(), ExecuteOptions{
		Console: func(_, line string) { out.console = append(out.console, line) },
		Dispatch: func(_ context.Context, name string, arg any) (any, error) {
			b, err := MarshalExport(arg)
			if err != nil {
				return nil, err
			}
			if arg == Undefined || arg == nil {
				b = []byte("{}")
			}
			out.calls = append(out.calls, name+" "+string(b))
			var args any
			_ = json.Unmarshal(b, &args)
			return fx.respond(name, args)
		},
		HostDispatch: func(_ context.Context, name string, args []any) (any, error) {
			switch name {
			case "workspace_files":
				out.calls = append(out.calls, name)
				return fx.workspace(name, ""), nil
			case "mount_result":
				ref, _ := args[0].(string)
				out.calls = append(out.calls, name+" "+ref)
				return fx.workspace(name, ref), nil
			}
			script, _ := args[0].(string)
			call := "bash " + script
			if len(args) > 1 {
				if stdin, ok := args[1].(JSONText); ok {
					call += " <<< " + string(stdin)
				}
			}
			out.calls = append(out.calls, call)
			return fx.bash(script), nil
		},
	})
	if err != nil {
		var thrown *Throw
		switch {
		case errors.As(err, &thrown) && thrown.cause != nil:
			out.err = "GoError: " + thrown.cause.Error()
		default:
			out.err = err.Error()
		}
		return out
	}
	b, err := MarshalExport(res.Value)
	if err != nil {
		out.err = "marshal: " + err.Error()
		return out
	}
	out.ret = string(b)
	return out
}

var gojaPosition = regexp.MustCompile(` at .*$`)

// runGoja is the oracle: a Goja VM set up like Ditto's runner (sloppy IIFE,
// tool results as parsed JSON, console formatted from exported values).
func runGoja(c *corpusCase) outcome {
	var out outcome
	fx := &fixture{c: c, calls: map[string]int{}}
	vm := goja.New()
	vm.SetMaxCallStackSize(2000)
	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	toJS := func(v any) goja.Value {
		b, _ := json.Marshal(v) // sorted keys, like native fromGo
		r, err := parse(goja.Undefined(), vm.ToValue(string(b)))
		if err != nil {
			panic(err)
		}
		return r
	}
	async := strings.Contains(c.script, "await") || strings.Contains(c.script, "Promise")
	dispatch := func(name string, arg goja.Value) goja.Value {
		argsJSON := "{}"
		if arg != nil && !goja.IsUndefined(arg) && !goja.IsNull(arg) {
			b, err := json.Marshal(arg.Export())
			if err != nil {
				panic(vm.NewTypeError("invalid arguments for %s: %v", name, err))
			}
			argsJSON = string(b)
		}
		out.calls = append(out.calls, name+" "+argsJSON)
		var args any
		_ = json.Unmarshal([]byte(argsJSON), &args)
		v, err := fx.respond(name, args)
		if async {
			// The async dialect's tools behave like promise-returning functions.
			p, resolve, reject := vm.NewPromise()
			if err != nil {
				_ = reject(vm.NewGoError(err))
			} else {
				_ = resolve(toJS(v))
			}
			return vm.ToValue(p)
		}
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return toJS(v)
	}
	root := vm.NewObject()
	// Ditto's installBindings sets `call` first, then each catalog tool.
	_ = root.Set("call", func(call goja.FunctionCall) goja.Value {
		return dispatch(call.Argument(0).String(), call.Argument(1))
	})
	servers := map[string]*goja.Object{}
	for _, name := range c.sortedTools() {
		name := name
		fn := func(call goja.FunctionCall) goja.Value { return dispatch(name, call.Argument(0)) }
		server, local, nested := strings.Cut(name, ".")
		if !nested {
			_ = root.Set(name, fn)
			continue
		}
		if servers[server] == nil {
			servers[server] = vm.NewObject()
			_ = root.Set(server, servers[server])
		}
		_ = servers[server].Set(local, fn)
	}
	_ = vm.Set("mcp", root)
	_ = vm.Set("tools", root)
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	_ = vm.Set("bash", func(call goja.FunctionCall) goja.Value {
		script := call.Argument(0).String()
		entry := "bash " + script
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) {
			encoded, err := stringify(goja.Undefined(), call.Argument(1))
			if err != nil {
				panic(err)
			}
			entry += " <<< " + encoded.String()
		}
		out.calls = append(out.calls, entry)
		return toJS(fx.bash(script))
	})
	_ = vm.Set("mount_result", func(call goja.FunctionCall) goja.Value {
		ref := call.Argument(0).String()
		out.calls = append(out.calls, "mount_result "+ref)
		return toJS(fx.workspace("mount_result", ref))
	})
	_ = vm.Set("workspace_files", func(call goja.FunctionCall) goja.Value {
		out.calls = append(out.calls, "workspace_files")
		return toJS(fx.workspace("workspace_files", ""))
	})
	write := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = gojaConsoleArg(a)
		}
		out.console = append(out.console, strings.Join(parts, " "))
		return goja.Undefined()
	}
	console := vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, write)
	}
	_ = vm.Set("console", console)
	wrapper := "(function(){\n" + c.script + "\n})()"
	if async {
		wrapper = "(async function(){\n" + c.script + "\n})()"
		// Documented dialect: host failures settle with their error text.
		_, _ = vm.RunString(`Promise.allSettled = (orig => function (ps) {
			return orig.call(Promise, ps).then(rs => rs.map(r => r.status === "rejected" && r.reason && r.reason.name === "GoError" ? {status: "rejected", reason: r.reason.message} : r));
		})(Promise.allSettled);`)
	} else {
		_ = vm.GlobalObject().Delete("Promise")
	}
	timer := time.AfterFunc(5*time.Second, func() { vm.Interrupt("timeout") })
	defer timer.Stop()
	v, err := vm.RunString(wrapper)
	if err == nil && async {
		if p, ok := v.Export().(*goja.Promise); ok {
			switch p.State() {
			case goja.PromiseStateFulfilled:
				v = p.Result()
			case goja.PromiseStateRejected:
				err = &goja.Exception{}
				out.err = gojaErrorText(p.Result())
				return out
			default:
				out.err = "pending promise"
				return out
			}
		}
	}
	if err != nil {
		var ex *goja.Exception
		if errors.As(err, &ex) && ex.Value() != nil {
			out.err = gojaErrorText(ex.Value())
		} else {
			out.err = gojaPosition.ReplaceAllString(err.Error(), "")
		}
		return out
	}
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		out.ret = "null"
		return out
	}
	b, err := json.Marshal(clampDepthForTest(v.Export(), 0))
	if err != nil {
		out.err = "marshal: " + err.Error()
		return out
	}
	out.ret = string(b)
	return out
}

func gojaErrorText(v goja.Value) string {
	if o, ok := v.(*goja.Object); ok {
		if name := o.Get("name"); name != nil && !goja.IsUndefined(name) {
			return name.String() + ": " + o.Get("message").String()
		}
	}
	return v.String()
}

func gojaConsoleArg(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if goja.IsNull(v) {
		return "null"
	}
	exported := v.Export()
	switch exported.(type) {
	case map[string]any, []any:
		if b, err := json.Marshal(clampDepthForTest(exported, 0)); err == nil {
			return string(b)
		}
	}
	return v.String()
}

func clampDepthForTest(v any, depth int) any {
	if depth >= maxExportDepth {
		return maxDepthExceeded
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = clampDepthForTest(val, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = clampDepthForTest(val, depth+1)
		}
		return out
	}
	return v
}

// nativeErrText normalizes an uncaught native error like the oracle's.
func (o outcome) equal(other outcome) (bool, string) {
	if o.ret != other.ret {
		return false, "return"
	}
	if (o.err == "") != (other.err == "") {
		return false, "error presence"
	}
	if o.async {
		a, b := append([]string(nil), o.calls...), append([]string(nil), other.calls...)
		sort.Strings(a)
		sort.Strings(b)
		if strings.Join(a, "\n") != strings.Join(b, "\n") {
			return false, "calls"
		}
	} else if strings.Join(o.calls, "\n") != strings.Join(other.calls, "\n") {
		return false, "calls"
	}
	if strings.Join(o.console, "\n") != strings.Join(other.console, "\n") {
		return false, "console"
	}
	if o.err != other.err {
		return false, "error text"
	}
	return true, ""
}

// TestCorpusDifferential runs every corpus script natively and in Goja and
// requires identical observable behavior when the native engine accepts it.
func TestCorpusDifferential(t *testing.T) {
	cases := loadCorpus(t)
	if len(cases) == 0 {
		t.Skip("no corpus")
	}
	native, mismatched := 0, map[string]int{}
	var report []string
	for i := range cases {
		c := &cases[i]
		p, err := Compile(c.script, c.compileOptions())
		if err != nil {
			if !errors.Is(err, ErrUnsupported) {
				t.Errorf("%s: compile error without ErrUnsupported: %v", c.name, err)
			}
			report = append(report, fmt.Sprintf("%-50s fallback  %s", c.name, firstLine(err.Error())))
			continue
		}
		native++
		got, want := runNative(c, p), runGoja(c)
		got.async = strings.Contains(c.script, "await") || strings.Contains(c.script, "Promise")
		if ok, what := got.equal(want); !ok && c.divergence != "" {
			report = append(report, fmt.Sprintf("%-50s divergent %s", c.name, firstLine(c.divergence)))
			continue
		} else if !ok {
			mismatched[what]++
			report = append(report, fmt.Sprintf("%-50s MISMATCH  %s", c.name, what))
			if os.Getenv("TOOLSCRIPT_CORPUS_VERBOSE") != "" || !c.generic {
				t.Errorf("%s: %s differs\nnative: ret=%s err=%q console=%q calls=%q\ngoja:   ret=%s err=%q console=%q calls=%q",
					c.name, what, trunc(got.ret), got.err, got.console, got.calls, trunc(want.ret), want.err, want.console, want.calls)
			}
			continue
		}
		report = append(report, fmt.Sprintf("%-50s native", c.name))
	}
	t.Logf("corpus: %d scripts, %d native (%.1f%%), mismatches %v", len(cases), native, 100*float64(native)/float64(len(cases)), mismatched)
	if path := os.Getenv("TOOLSCRIPT_CORPUS_REPORT"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(report, "\n")+"\n"), 0o644)
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 90 {
		s = s[:90]
	}
	return s
}

func trunc(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// scanToolNames finds static tool paths and mcp.call names in a script.
func scanToolNames(src string) []string {
	tree, err := parser.ParseFile(nil, "x.js", "(async function(){\n"+src+"\n})", 0)
	if err != nil {
		return nil
	}
	var names []string
	walkAST(tree, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpression)
		if !ok {
			return true
		}
		path, ok := staticPath(call.Callee)
		if !ok {
			return true
		}
		if len(path) == 2 && (path[0] == "mcp" || path[0] == "tools") && path[1] == "call" && len(call.ArgumentList) > 0 {
			if lit, ok := call.ArgumentList[0].(*ast.StringLiteral); ok {
				names = append(names, lit.Value.String())
			}
			return true
		}
		if name, ok := toolName(path); ok && len(path) <= 3 {
			names = append(names, name)
		}
		return true
	})
	return names
}
