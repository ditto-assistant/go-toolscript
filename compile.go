// Package toolscript compiles a deliberately small JavaScript-shaped language
// into a bounded tool execution plan. Parsing uses Goja's lexer and AST, never
// its VM. Unsupported programs are rejected before any host function is called.
package toolscript

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

// ErrUnsupported means the caller may safely fall back to another executor.
// Only Compile returns it; execution errors must NEVER trigger fallback.
var ErrUnsupported = errors.New("unsupported tool script")

// HostFunction explicitly admits a bare global function with positional arguments.
// LiteralStringArgs requires those argument positions to be string literals;
// hosts can use this to avoid approximating JavaScript coercion. No implementation
// or ambient capability is installed by declaring a function.
type HostFunction struct {
	MinArgs           int
	MaxArgs           int
	LiteralStringArgs []int
}

// CompileOptions selects the host's exposed namespace and language extensions.
type CompileOptions struct {
	HostFunctions map[string]HostFunction
	// Resolve maps a static property path (e.g. ["mcp","search"]) to a
	// dispatcher name. Return false for unknown bindings. Never executes a tool.
	Resolve func([]string) (string, bool)
	// ResolveCall maps mcp.call("raw-name", args) separately from property
	// bindings, so it cannot collide with mcp.call.some_tool(args).
	ResolveCall func(string) (string, bool)
	// Batches admits await and Promise.all/allSettled over arrays and .map.
	// This is an explicit asynchronous-tool dialect, not arbitrary JS promises.
	Batches bool
}

// Program is immutable and safe to execute concurrently with separate hosts.
type Program struct{ statements []statement }
type statement struct {
	value    *expr
	bindings []binding
	ret      bool
}
type binding struct {
	name    string
	keys    []selection
	require string
}
type selection struct {
	key      string
	iterable bool
}

type field struct {
	name  string
	value *expr
}
type expr struct {
	kind     string
	value    any
	name     string
	children []*expr
	fields   []field
	body     *Program
	params   []string
}
type compiler struct {
	opts       CompileOptions
	names      map[string]bool
	depth      int
	batchDepth int
}

// Compile validates the WHOLE source before producing an executable plan.
// Source and nesting limits also bound parser/compiler work on untrusted input.
func Compile(source string, opts CompileOptions) (*Program, error) {
	if len(source) > 32<<10 {
		return nil, fmt.Errorf("%w: source exceeds 32 KiB", ErrUnsupported)
	}
	// Async wrapper admits top-level await only when explicitly opted in below.
	tree, err := parser.ParseFile(nil, "toolscript.js", "(async function(){\n"+source+"\n})", 0, parser.WithDisableSourceMaps)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if len(tree.Body) != 1 {
		return nil, ErrUnsupported
	}
	s, ok := tree.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return nil, ErrUnsupported
	}
	fn, ok := s.Expression.(*ast.FunctionLiteral)
	if !ok {
		return nil, ErrUnsupported
	}
	c := &compiler{opts: opts, names: map[string]bool{}}
	p, err := c.program(fn.Body.List)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return p, nil
}
func unsupported(n any) error { return fmt.Errorf("unsupported syntax %T", n) }
func (c *compiler) program(list []ast.Statement) (*Program, error) {
	p := &Program{}
	for _, node := range list {
		switch n := node.(type) {
		case *ast.EmptyStatement:
		case *ast.ReturnStatement:
			e, err := c.expression(n.Argument)
			if err != nil {
				return nil, err
			}
			p.statements = append(p.statements, statement{value: e, ret: true})
		case *ast.ExpressionStatement:
			if _, directive := n.Expression.(*ast.StringLiteral); directive {
				return nil, unsupported(n)
			}
			e, err := c.expression(n.Expression)
			if err != nil {
				return nil, err
			}
			p.statements = append(p.statements, statement{value: e})
		case *ast.LexicalDeclaration:
			if err := c.declarations(p, n.List); err != nil {
				return nil, err
			}
		case *ast.VariableStatement:
			if err := c.declarations(p, n.List); err != nil {
				return nil, err
			}
		default:
			return nil, unsupported(n)
		}
	}
	return p, nil
}
func (c *compiler) declarations(p *Program, list []*ast.Binding) error {
	for _, b := range list {
		// Initializer must precede registration: disallows self references/TDZ.
		e, err := c.expression(b.Initializer)
		if err != nil {
			return err
		}
		bindings, err := c.pattern(b.Target, nil, 0)
		if err != nil {
			return err
		}
		p.statements = append(p.statements, statement{value: e, bindings: bindings})
	}
	return nil
}
func reserved(s string) bool {
	switch s {
	case "mcp", "tools", "Promise", "undefined", "NaN", "Infinity", "eval", "arguments":
		return true
	}
	return false
}
func (c *compiler) reserved(name string) bool {
	_, host := c.opts.HostFunctions[name]
	return host || reserved(name)
}
func (c *compiler) pattern(n ast.Expression, keys []selection, depth int) ([]binding, error) {
	if depth > 32 {
		return nil, fmt.Errorf("binding nesting limit")
	}
	switch n := n.(type) {
	case *ast.Identifier:
		name := n.Name.String()
		if c.reserved(name) || c.names[name] {
			return nil, fmt.Errorf("shadowed/redeclared binding %s", name)
		}
		c.names[name] = true
		return []binding{{name: name, keys: keys}}, nil
	case *ast.ArrayPattern:
		if n.Rest != nil {
			return nil, unsupported(n)
		}
		out := []binding{{keys: keys, require: "array"}}
		for i, v := range n.Elements {
			if v == nil {
				continue
			}
			b, err := c.pattern(v, appendKey(keys, strconv.Itoa(i), true), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil
	case *ast.ObjectPattern:
		if n.Rest != nil {
			return nil, unsupported(n)
		}
		out := []binding{{keys: keys, require: "object"}}
		for _, v := range n.Properties {
			var key string
			var target ast.Expression
			switch v := v.(type) {
			case *ast.PropertyShort:
				if v.Initializer != nil {
					return nil, unsupported(v)
				}
				key = v.Name.Name.String()
				target = &v.Name
			case *ast.PropertyKeyed:
				if v.Computed || v.Kind != ast.PropertyKindValue {
					return nil, unsupported(v)
				}
				var ok bool
				key, ok = propertyKey(v.Key)
				if !ok {
					return nil, unsupported(v)
				}
				target = v.Value
			default:
				return nil, unsupported(v)
			}
			if unsafeKey(key) {
				return nil, unsupported(v)
			}
			b, err := c.pattern(target, appendKey(keys, key, false), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil
	}
	return nil, unsupported(n)
}
func appendKey(keys []selection, k string, iterable bool) []selection {
	return append(append([]selection(nil), keys...), selection{k, iterable})
}
func propertyKey(n ast.Expression) (string, bool) {
	switch n := n.(type) {
	case *ast.Identifier:
		return n.Name.String(), true
	case *ast.StringLiteral:
		return n.Value.String(), true
	case *ast.NumberLiteral:
		var number float64
		switch v := n.Value.(type) {
		case int64:
			number = float64(v)
		case float64:
			number = v
		default:
			return "", false
		}
		// JS ToPropertyKey switches to exponent notation at 1e21 and below
		// 1e-6. Decline uncommon numeric keys rather than approximate coercion.
		if number < 0 || number > 9007199254740991 || math.Trunc(number) != number {
			return "", false
		}
		return strconv.FormatFloat(number, 'f', -1, 64), true
	}
	return "", false
}
func staticPath(n ast.Expression) ([]string, bool) {
	switch n := n.(type) {
	case *ast.Identifier:
		return []string{n.Name.String()}, true
	case *ast.DotExpression:
		p, ok := staticPath(n.Left)
		return append(p, n.Identifier.Name.String()), ok
	case *ast.BracketExpression:
		p, ok := staticPath(n.Left)
		s, yes := n.Member.(*ast.StringLiteral)
		if ok && yes {
			return append(p, s.Value.String()), true
		}
	}
	return nil, false
}
func (c *compiler) expression(node ast.Expression) (*expr, error) {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > 64 {
		return nil, fmt.Errorf("expression nesting limit")
	}
	if node == nil {
		return &expr{kind: "literal", value: Undefined}, nil
	}
	switch n := node.(type) {
	case *ast.NullLiteral:
		return &expr{kind: "literal"}, nil
	case *ast.BooleanLiteral:
		return &expr{kind: "literal", value: n.Value}, nil
	case *ast.StringLiteral:
		return &expr{kind: "literal", value: n.Value.String()}, nil
	case *ast.NumberLiteral:
		var f float64
		switch v := n.Value.(type) {
		case int64:
			f = float64(v)
		case float64:
			f = v
		default:
			return nil, unsupported(n)
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, unsupported(n)
		}
		return &expr{kind: "literal", value: f}, nil
	case *ast.UnaryExpression:
		if n.Operator != token.MINUS && n.Operator != token.PLUS {
			return nil, unsupported(n)
		}
		num, ok := n.Operand.(*ast.NumberLiteral)
		if !ok {
			return nil, unsupported(n)
		}
		e, err := c.expression(num)
		if err != nil {
			return nil, err
		}
		if n.Operator == token.MINUS {
			e.value = -e.value.(float64)
		}
		return e, nil
	case *ast.Identifier:
		name := n.Name.String()
		if name == "undefined" {
			return &expr{kind: "literal", value: Undefined}, nil
		}
		if !c.names[name] {
			return nil, fmt.Errorf("unbound identifier %s", name)
		}
		return &expr{kind: "ref", name: name}, nil
	case *ast.AwaitExpression:
		if !c.opts.Batches {
			return nil, unsupported(n)
		}
		return c.expression(n.Argument)
	case *ast.ArrayLiteral:
		e := &expr{kind: "array"}
		for _, v := range n.Value {
			if v == nil {
				return nil, unsupported(n)
			}
			a, err := c.expression(v)
			if err != nil {
				return nil, err
			}
			e.children = append(e.children, a)
		}
		return e, nil
	case *ast.ObjectLiteral:
		e := &expr{kind: "object"}
		seen := map[string]bool{}
		for _, v := range n.Value {
			var name string
			var value ast.Expression
			switch v := v.(type) {
			case *ast.PropertyShort:
				if v.Initializer != nil {
					return nil, unsupported(v)
				}
				name = v.Name.Name.String()
				value = &v.Name
			case *ast.PropertyKeyed:
				if v.Computed || v.Kind != ast.PropertyKindValue {
					return nil, unsupported(v)
				}
				var ok bool
				name, ok = propertyKey(v.Key)
				if !ok {
					return nil, unsupported(v)
				}
				value = v.Value
			default:
				return nil, unsupported(v)
			}
			if name == "__proto__" || seen[name] {
				return nil, fmt.Errorf("special or duplicate key %s", name)
			}
			seen[name] = true
			a, err := c.expression(value)
			if err != nil {
				return nil, err
			}
			e.fields = append(e.fields, field{name, a})
		}
		return e, nil
	case *ast.DotExpression:
		if unsafeKey(n.Identifier.Name.String()) {
			return nil, unsupported(n)
		}
		a, err := c.expression(n.Left)
		if err != nil {
			return nil, err
		}
		return &expr{kind: "member", name: n.Identifier.Name.String(), children: []*expr{a}}, nil
	case *ast.BracketExpression:
		key, ok := propertyKey(n.Member)
		if _, id := n.Member.(*ast.Identifier); id {
			ok = false
		}
		if !ok || unsafeKey(key) {
			return nil, unsupported(n)
		}
		a, err := c.expression(n.Left)
		if err != nil {
			return nil, err
		}
		return &expr{kind: "member", name: key, children: []*expr{a}}, nil
	case *ast.CallExpression:
		return c.call(n)
	}
	return nil, unsupported(node)
}
func (c *compiler) call(n *ast.CallExpression) (*expr, error) {
	path, static := staticPath(n.Callee)
	if static && len(path) == 1 && !c.names[path[0]] && !reserved(path[0]) {
		if spec, ok := c.opts.HostFunctions[path[0]]; ok {
			if spec.MinArgs < 0 || spec.MaxArgs < spec.MinArgs || len(n.ArgumentList) < spec.MinArgs || len(n.ArgumentList) > spec.MaxArgs {
				return nil, unsupported(n)
			}
			for _, index := range spec.LiteralStringArgs {
				if index < 0 || index >= len(n.ArgumentList) {
					return nil, unsupported(n)
				}
				if _, ok := n.ArgumentList[index].(*ast.StringLiteral); !ok {
					return nil, unsupported(n)
				}
			}
			args := make([]*expr, len(n.ArgumentList))
			for i, arg := range n.ArgumentList {
				e, err := c.expression(arg)
				if err != nil {
					return nil, err
				}
				args[i] = e
			}
			return &expr{kind: "host", name: path[0], children: args}, nil
		}
	}

	if static && len(path) == 2 && path[0] == "Promise" && (path[1] == "all" || path[1] == "allSettled") {
		if !c.opts.Batches || len(n.ArgumentList) != 1 || c.batchDepth > 0 {
			return nil, unsupported(n)
		}
		c.batchDepth++
		arg, err := c.expression(n.ArgumentList[0])
		c.batchDepth--
		if err != nil {
			return nil, err
		}
		if arg.kind != "array" && arg.kind != "map" {
			return nil, unsupported(n)
		}
		return &expr{kind: path[1], children: []*expr{arg}}, nil
	}
	if dot, ok := n.Callee.(*ast.DotExpression); ok && dot.Identifier.Name.String() == "map" {
		if len(n.ArgumentList) != 1 {
			return nil, unsupported(n)
		}
		source, err := c.expression(dot.Left)
		if err != nil {
			return nil, err
		}
		arrow, ok := n.ArgumentList[0].(*ast.ArrowFunctionLiteral)
		if !ok || arrow.Async && (!c.opts.Batches || c.batchDepth == 0) || arrow.ParameterList.Rest != nil || len(arrow.ParameterList.List) > 2 {
			return nil, unsupported(n)
		}
		child := &compiler{opts: c.opts, names: map[string]bool{}, depth: c.depth, batchDepth: c.batchDepth}
		for k, v := range c.names {
			child.names[k] = v
		}
		var params []string
		for _, p := range arrow.ParameterList.List {
			id, ok := p.Target.(*ast.Identifier)
			if !ok || p.Initializer != nil || child.reserved(id.Name.String()) || child.names[id.Name.String()] {
				return nil, unsupported(p)
			}
			params = append(params, id.Name.String())
			child.names[id.Name.String()] = true
		}
		var body *Program
		switch b := arrow.Body.(type) {
		case *ast.ExpressionBody:
			e, er := child.expression(b.Expression)
			err = er
			body = &Program{statements: []statement{{value: e, ret: true}}}
		case *ast.BlockStatement:
			body, err = child.program(b.List)
		default:
			return nil, unsupported(b)
		}
		if err != nil {
			return nil, err
		}
		return &expr{kind: "map", children: []*expr{source}, params: params, body: body}, nil
	}
	if !static || len(path) < 2 || c.names[path[0]] {
		return nil, unsupported(n)
	}
	var name string
	var ok bool
	if len(path) == 2 && (path[0] == "mcp" || path[0] == "tools") && path[1] == "call" {
		if c.opts.ResolveCall == nil || len(n.ArgumentList) < 1 || len(n.ArgumentList) > 2 {
			return nil, unsupported(n)
		}
		raw, literal := n.ArgumentList[0].(*ast.StringLiteral)
		if !literal {
			return nil, unsupported(n)
		}
		name, ok = c.opts.ResolveCall(raw.Value.String())
		n = &ast.CallExpression{ArgumentList: n.ArgumentList[1:]}
	} else {
		if c.opts.Resolve == nil {
			return nil, unsupported(n)
		}
		name, ok = c.opts.Resolve(path)
	}
	if !ok {
		return nil, fmt.Errorf("unknown binding %s", strings.Join(path, "."))
	}
	if len(n.ArgumentList) > 1 {
		return nil, unsupported(n)
	}
	var arg ast.Expression
	if len(n.ArgumentList) == 1 {
		arg = n.ArgumentList[0]
	}
	e, err := c.expression(arg)
	if err != nil {
		return nil, err
	}
	return &expr{kind: "call", name: name, children: []*expr{e}}, nil
}

func unsafeKey(k string) bool {
	switch k {
	case "__proto__", "prototype", "constructor", "toString", "toLocaleString", "valueOf", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable", "__defineGetter__", "__defineSetter__", "__lookupGetter__", "__lookupSetter__":
		return true
	}
	return false
}
