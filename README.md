# go-toolscript

Compile simple JavaScript-shaped tool pipelines to a bounded Go execution plan.
No JavaScript VM, bytecode, `eval`, regex matching, ambient network, filesystem,
or package imports. MIT licensed.

The lexer/parser and AST come from [Goja](https://github.com/dop251/goja).
The production package imports only its parsing packages; the VM is used in
**tests only** as a differential oracle. This is a small tool orchestration
language, not a replacement JavaScript implementation.

```go
plan, err := toolscript.Compile(`
  const r = mcp.search_memories({queries:["conversation"], limit:30});
  return r;
`, toolscript.CompileOptions{
  Resolve: func(path []string) (string, bool) {
    if len(path) == 2 && path[0] == "mcp" && path[1] == "search_memories" {
      return "search_memories", true
    }
    return "", false
  },
})
if errors.Is(err, toolscript.ErrUnsupported) {
  // Safe to fall back to your normal runtime: no tool has executed.
}
// Handle other errors before using plan.
result, err := plan.Execute(ctx, toolscript.ExecuteOptions{
  Dispatch: dispatch, // func(context.Context, string, any) (any, error)
})
// NEVER fall back after Execute, even on failure: effects may already exist.
output, err := toolscript.Marshal(result.Value)
```

`Resolve` sees static paths: `mcp.foo`, `tools.foo`, `mcp.server.foo`, and
string-bracket equivalents. `mcp.call("raw-name", args)` uses the separate `ResolveCall(rawName)` callback,
so it cannot collide with a server literally named `call`. The host decides
the mapping and permissions.
Calls accept zero or one data argument. Null/undefined normalization, access
control, approvals, accounting, credentials, result storage and audit remain
host responsibilities. `Compile` never dispatches.

## Supported patterns

- Direct calls, assignment then return, multiple declarations, `const`/`let`/`var`.
- JSON-style object/array literals, unquoted keys, shorthand fields, trailing
  commas, JS string escapes, numeric literals and unary numeric signs.
- Earlier bindings as arguments; property and literal-index result projection.
- Nested object/array destructuring without defaults or rest.
- Array `.map(x => ...)`, `.map((x, i) => ...)`, and arrow block bodies with
  local declarations and `return`. A normal map runs in input order.
- Comments, whitespace, semicolon insertion, redundant parentheses and bare
  return follow the parser, rather than source-text heuristics.
- With `CompileOptions.Batches`, `await`, `Promise.all` and `Promise.allSettled`
  over **inline arrays or inline maps**. Async callbacks are allowed inside
  those batches. Nested batches are rejected.

```js
const {items} = mcp.list({limit:10});
return items.map(item => mcp.get({id:item.id}));
```

```js
const [profile, messages] = await Promise.all([
  mcp.profile({id:"example"}),
  mcp.messages({id:"example"})
]);
return {profile, messages};
```

```js
const inputs = [{id:"a"}, {id:"b"}];
return await Promise.allSettled(inputs.map(async input => {
  const result = await mcp.get(input);
  return {id:input.id, result};
}));
```

Batch mode is an explicit **async-tool dialect**. Tool calls return values to
Go, not JS Promise objects. Only the batch schedules work concurrently;
`await` otherwise unwraps a value. `Parallelism` defaults to 1; a thread-safe
host may choose 2–64 workers. Output order always follows input order. Ordinary
`all` failures join already-started workers and return the first error in input
order. `allSettled` uses `{status:"fulfilled",value:...}` or
`{status:"rejected",reason:"error text"}`. This intentionally defines error
serialization and cleanup rather than emulating the JS microtask queue.

Fatal host errors cannot be swallowed by `allSettled`. Already-running tools
may finish; queued work checks the fatal flag before dispatch. No execution
error causes a retry. Programs and input data are immutable; each execution
gets its own bindings, counters and budget.

## Deliberate fallback boundary

The **entire program**, including unreachable code, must compile before any
execution begins. Unknown identifiers, shadowed/redeclared names, dynamic tool
names, assignments/mutation, spreads, getters, classes, loops, general function
calls, arbitrary promises, regexes, imports, templates, operators, directives,
optional chains and prototype access all decline optimization. A host can then
run the original source in its existing runtime.

This subset operates on JSON data, not JS prototypes/coercion. Invalid data
shapes (such as mapping a non-array) fail at execution time, not via fallback.
Numeric property keys are limited to nonnegative safe integers; other numeric
keys decline compilation rather than approximate JS string coercion.
Property reads use own JSON properties, array indexes/length and string UTF-16
indexes/length. Destructuring arrays requires an array or string.

`Dispatch` receives and returns JSON-compatible Go data: `map[string]any`,
`[]any`, `float64`, strings, booleans, nil and the `Undefined` sentinel.
`Marshal` omits undefined object fields and emits null for undefined array
entries. `MarshalExport` instead matches Goja's `Value.Export` followed by
`encoding/json`, including null object fields. `Object` supports host values
whose property view differs from their JSON export (such as Go structs with
optional JSON fields). The host must not mutate shared input/result data.

## Limits and cancellation

- Source: 32 KiB; expression nesting: 64; destructuring nesting: 32.
- Defaults: 64 calls, 100,000 evaluation steps, 1,024 items per map/batch.
- Maximum 64 batch workers; no goroutine per input item and no nested batches.
- JSON traversal: 100,000 nodes, depth 64; repeated-reference expansion is bounded.
- Context is checked before evaluation and dispatch; every worker is joined.
- Host dispatch panics become errors, including inside batch workers.

A Go host function cannot be forcibly killed. It must honor cancellation.
For hard caller deadlines, run the plan in a worker and **retain the admission
slot until that worker and all its children exit**. Never release admission just
because a caller timed out. Hosts must separately cap external response sizes
and encoded output bytes. The library does not cache user programs or data.

## Validation and performance

```sh
go test -race ./...
go test -run '^$' -fuzz FuzzCompile -fuzztime=30s -parallel=4
go test -run '^$' -bench . -benchmem
go vet ./...
```

Tests cover parsing permutations, full-program fallback, dispatch traces versus
Goja, ordered parallel results, concurrency and call caps, cancellation, fatal
errors, host panics, JSON expansion and fuzzed input.

On an Apple M4 Pro, the included no-op-tool microbenchmark measured approximately
2.1 µs / 3.8 KB / 64 allocations for compile+execute versus 4.3 µs / 13.2 KB /
150 allocations for a fresh minimal Goja VM. This measures orchestration overhead,
not network latency or end-to-end application speed. Re-run on your host.
