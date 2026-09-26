package toolscript

import (
	"errors"
	"fmt"

	"github.com/dop251/goja/ast"
)

// The async dialect models promises as the values they settle to: an async
// function call runs to completion and returns its result, await returns its
// operand, and Promise.all/allSettled gather values. That is exact wherever a
// promise is only ever awaited, gathered by Promise.all/allSettled or returned
// (which awaits it too). Where a script can observe the promise itself, the
// shape declines at compile time:
//
//   - a call to a known async function (or Promise.all) that is not awaited,
//     returned or gathered: `const p = load(); typeof p`, `load();`,
//     `load().x`;
//   - .then/.catch/.finally in a script that uses the async dialect;
//   - an async callback handed to a built-in that drops or tests its result
//     (forEach, filter, some, find, reduce, sort, ...), or to a .map whose
//     result is not gathered by Promise.all/allSettled or a variable.
//
// Tool calls are exempt: the dialect documents them as returning their result
// directly. Calls through values the compiler cannot resolve (a parameter, an
// array element) are not judged.

var errAsyncShape = errors.New("async result used as a value")

type asyncShapes struct {
	names     map[string]bool              // bindings ever assigned an async function
	keys      map[string]bool              // object literal keys holding an async function
	okCall    map[*ast.CallExpression]bool // async-valued calls in an awaiting position
	okMap     map[*ast.CallExpression]bool // .map calls whose promises are gathered
	usesAsync bool
	err       error
}

// resultDroppingMethods are built-ins that ignore or test a callback's
// result, so an async callback's promise changes what they do.
var resultDroppingMethods = map[string]bool{
	"forEach": true, "filter": true, "some": true, "every": true, "find": true, "findIndex": true,
	"findLast": true, "findLastIndex": true, "reduce": true, "reduceRight": true, "sort": true,
	"toSorted": true, "flatMap": true, "from": true,
}

// checkAsyncShapes rejects async-dialect shapes whose promise is observable.
// main is the program wrapper (itself async).
func checkAsyncShapes(main *ast.FunctionLiteral) error {
	a := &asyncShapes{
		names:  map[string]bool{},
		keys:   map[string]bool{},
		okCall: map[*ast.CallExpression]bool{},
		okMap:  map[*ast.CallExpression]bool{},
	}
	walkAST(main.Body, a.collect)
	walkAST(main.Body, a.check)
	return a.err
}

func isAsyncLiteral(e ast.Node) bool {
	switch f := e.(type) {
	case *ast.FunctionLiteral:
		return f.Async
	case *ast.ArrowFunctionLiteral:
		return f.Async
	}
	return false
}

// awaited marks an expression whose value is awaited (await, return, a
// concise arrow body or a Promise.all element), looking through ?:.
func (a *asyncShapes) awaited(e ast.Expression) {
	switch e := e.(type) {
	case *ast.CallExpression:
		a.okCall[e] = true
	case *ast.ConditionalExpression:
		a.awaited(e.Consequent)
		a.awaited(e.Alternate)
	}
}

func isMapCall(e ast.Expression) (*ast.CallExpression, bool) {
	call, ok := e.(*ast.CallExpression)
	if !ok {
		return nil, false
	}
	dot, ok := call.Callee.(*ast.DotExpression)
	return call, ok && dot.Identifier.Name.String() == "map"
}

func isPromiseBatch(call *ast.CallExpression) bool {
	path, ok := staticPath(call.Callee)
	return ok && len(path) == 2 && path[0] == "Promise" && (path[1] == "all" || path[1] == "allSettled")
}

// collect records async bindings and every position where a promise is
// consumed the way the dialect models. Parents are visited before children.
func (a *asyncShapes) collect(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.FunctionDeclaration:
		if n.Function.Async && n.Function.Name != nil {
			a.names[n.Function.Name.Name.String()] = true
		}
	case *ast.FunctionLiteral:
		a.usesAsync = a.usesAsync || n.Async
	case *ast.ArrowFunctionLiteral:
		a.usesAsync = a.usesAsync || n.Async
		if body, ok := n.Body.(*ast.ExpressionBody); ok {
			a.awaited(body.Expression)
		}
	case *ast.Identifier:
		a.usesAsync = a.usesAsync || n.Name == "Promise"
	case *ast.Binding:
		if id, ok := n.Target.(*ast.Identifier); ok {
			if isAsyncLiteral(n.Initializer) {
				a.names[id.Name.String()] = true
			}
			if call, ok := isMapCall(n.Initializer); ok {
				a.okMap[call] = true // const ps = xs.map(async ...); await Promise.all(ps)
			}
		}
	case *ast.AssignExpression:
		if id, ok := n.Left.(*ast.Identifier); ok && isAsyncLiteral(n.Right) {
			a.names[id.Name.String()] = true
		}
	case *ast.PropertyKeyed:
		if key, ok := propertyKey(n.Key); ok && !n.Computed && isAsyncLiteral(n.Value) {
			a.keys[key] = true
		}
	case *ast.AwaitExpression:
		a.usesAsync = true
		a.awaited(n.Argument)
	case *ast.ReturnStatement:
		if n.Argument != nil {
			a.awaited(n.Argument)
		}
	case *ast.CallExpression:
		if isPromiseBatch(n) && len(n.ArgumentList) == 1 {
			switch arg := n.ArgumentList[0].(type) {
			case *ast.ArrayLiteral:
				for _, e := range arg.Value {
					a.awaited(e)
				}
			case *ast.CallExpression:
				if call, ok := isMapCall(arg); ok {
					a.okMap[call] = true
				}
			}
		}
	}
	return true
}

func (a *asyncShapes) fail(what string) bool {
	if a.err == nil {
		a.err = fmt.Errorf("%w: %s", errAsyncShape, what)
	}
	return false
}

// asyncCall reports a call that yields a promise in JavaScript.
func (a *asyncShapes) asyncCall(call *ast.CallExpression) bool {
	switch callee := call.Callee.(type) {
	case *ast.Identifier:
		return a.names[callee.Name.String()]
	case *ast.FunctionLiteral, *ast.ArrowFunctionLiteral:
		return isAsyncLiteral(callee)
	case *ast.DotExpression:
		if isPromiseBatch(call) {
			return true
		}
		if path, ok := staticPath(callee); ok && len(path) > 1 && (path[0] == "mcp" || path[0] == "tools") {
			return false // tool calls return their result directly
		}
		return a.keys[callee.Identifier.Name.String()]
	}
	return false
}

func (a *asyncShapes) asyncValue(e ast.Expression) bool {
	if id, ok := e.(*ast.Identifier); ok {
		return a.names[id.Name.String()]
	}
	return isAsyncLiteral(e)
}

func (a *asyncShapes) check(n ast.Node) bool {
	if a.err != nil {
		return false
	}
	call, ok := n.(*ast.CallExpression)
	if !ok {
		return true
	}
	if a.asyncCall(call) && !a.okCall[call] {
		return a.fail("un-awaited async call")
	}
	dot, ok := call.Callee.(*ast.DotExpression)
	if !ok {
		return true
	}
	method := dot.Identifier.Name.String()
	switch {
	case method == "then" || method == "catch" || method == "finally":
		if a.usesAsync {
			return a.fail("promise ." + method)
		}
	case resultDroppingMethods[method] || (method == "map" && !a.okMap[call]):
		for _, arg := range call.ArgumentList {
			if a.asyncValue(arg) {
				return a.fail("async callback passed to ." + method)
			}
		}
	}
	return true
}
