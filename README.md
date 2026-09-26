# go-toolscript

Run model-written JavaScript tool orchestration ("code mode") without a
JavaScript VM. Programs compile to a tree of Go closures over a JSON-shaped
value model, with no bytecode, `eval`, ambient network, filesystem or imports.
MIT licensed.

The lexer, parser and AST come from [Goja](https://github.com/dop251/goja).
Production code imports only Goja's parsing and number-formatting packages. The
VM is used **in tests only**, as a differential oracle. Anything outside the
supported subset is rejected before any tool runs, so a host can fall back to a
real JavaScript runtime.

```go
plan, err := toolscript.Compile(source, toolscript.CompileOptions{
	Resolve: func(path []string) (string, bool) { /* mcp.x / mcp.server.x -> tool name */ },
	ResolveCall: func(raw string) (string, bool) { /* mcp.call("raw", args) */ },
	Bindings: catalogPaths, // optional: expose mcp/tools as values
	HostFunctions: map[string]toolscript.HostFunction{
		"bash": {MinArgs: 1, MaxArgs: 3, StringArgs: []int{0}, JSONArgs: []int{1, 2}},
	},
	Batches: true, // await, async functions, Promise.all/allSettled
})
if errors.Is(err, toolscript.ErrUnsupported) {
	// Safe to fall back: nothing has run.
}
result, err := plan.Execute(ctx, toolscript.ExecuteOptions{
	Dispatch:     dispatch,     // func(ctx, tool string, arg any) (any, error)
	HostDispatch: hostDispatch, // func(ctx, name string, args []any) (any, error)
	Console:      func(level, line string) { ... },
})
if errors.Is(err, toolscript.ErrRuntimeUnsupported) && result.Calls == 0 && result.HostCalls == 0 {
	// Also safe to fall back: the unsupported operation came before any effect.
}
output, err := toolscript.MarshalExport(result.Value)
```

## What runs natively

Measured on real Ditto traffic, 974 of 981 production `run_code` scripts
(99.3%) ran natively before `Intl` support; the rest used `Intl` (which Goja
lacks entirely) or fail in Goja too (syntax errors, typos).

- **Statements:** `var`/`let`/`const` (block scoping, TDZ, per-iteration loop
  bindings), destructuring with defaults and rest, `if`, `for`, `for…of`,
  `for…in`, `while`, `do…while`, `switch`, labeled `break`/`continue`,
  `try`/`catch`/`finally`, `throw`, function declarations (hoisted), closures,
  default and rest parameters, recursion.
- **Expressions:** every operator with JavaScript coercion (`==`, `+`, `<` on
  strings, bitwise ops, `??`, `?.`, logical assignment), template literals,
  spread, computed keys, `typeof`/`in`/`delete`/`instanceof`, arrow and
  function expressions, `fn.call/apply/bind`.
- **Built-ins:** Array, String, Number, Object, Math, JSON, `Set`/`Map`,
  iterators, `Array.from/of/isArray`, `parseInt`/`parseFloat`,
  `encodeURIComponent` and related functions, `localeCompare`, `toFixed`,
  `toPrecision`, `Error` types and `Date` (ported from Goja: parsing,
  local/UTC getters and setters, every string form, `Date.now/parse/UTC`;
  the clock and zone come from `ExecuteOptions.Now`/`Location`, defaulting
  to `time.Now` and `time.Local` like Goja). Regular expressions (`test`, `exec`,
  `lastIndex`, `match`, `replace` and `replaceAll`, `split`, `search`) run on
  Go's RE2 through Goja's own JS-to-RE2 transform.
- **Intl** (ECMA-402, which Goja does not have): `Intl.DateTimeFormat`
  (a port of ICU's pattern generator with V8's option handling: components,
  `dateStyle`/`timeStyle`, hour cycles, IANA and offset time zones with ICU's
  English zone names, `format`, `formatToParts`, `resolvedOptions`) and
  `toLocale{,Date,Time}String` when given locales or options (without
  arguments they keep Goja's fixed layouts), plus `Intl.getCanonicalLocales`,
  `Intl.supportedValuesOf` and `supportedLocalesOf`. The engine carries the
  English (`en`, `en-US`) locale data only: other locales resolve through
  ECMA-402's lookup matcher to `en` or the default `en-US`, and
  `resolvedOptions().locale` says so. Non-Gregorian calendars, other
  numbering systems and `formatRange` raise `ErrRuntimeUnsupported`. Time zone
  data comes from Go's `time` package; import `time/tzdata` in hosts without a
  system zoneinfo database.
- **Tools and host functions:** `mcp.x(...)`, `tools.x(...)`,
  `mcp.server.x(...)`, `mcp["server-name"].x(...)` and
  `mcp.call("raw", args)`. Aliases such as `const gh = mcp.github` work too. With
  `Bindings`, `Object.keys(mcp)` and `typeof mcp.x` also work. Host functions
  can coerce arguments with `StringArgs`, or receive `JSONText` built by the
  engine's `JSON.stringify` with `JSONArgs`. Returning `*TypeError` from a
  dispatcher raises a JavaScript `TypeError`.
- **Async dialect** (`Batches`): `await` unwraps values, async functions
  return values, and `Promise.all`/`allSettled` run inline arrays and
  `.map(async …)` callbacks. `allSettled` reports a host failure as
  `{status:"rejected", reason:"error text"}`. With `Parallelism > 1`, items
  that cannot observe each other's state run concurrently. Within such a
  batch, host functions, `SequentialTools` and console output keep script
  order: an item's ordered effect waits until every earlier item has
  finished, so a batch mixing searches with an artifact write fetches in
  parallel and still writes in order. A failing item does not stop its
  siblings; only a failure the host marks `Fatal` (revoked, cancelled) stops
  further dispatch.

Declined at compile time: other `Intl` services (`Collator`, `Segmenter`,
`DisplayNames`, `Locale`, …), `class`, generators, getters and setters,
`this`, `with`, `eval`, `Symbol`, tagged templates, `arguments`, JSON.parse
revivers, `u`-flag regexes, backreference and lookaround regexes, `WeakMap`, and
unknown globals.

## Fidelity

`testdata/corpus` holds 480+ scripts in the shapes models actually write. They
include 140 adversarial cases (coercion, UTF-16 strings, sort, holes, key order,
error text, limits). `TestCorpusDifferential` runs each script natively and in
a Goja VM set up like Ditto's runner. It compares the return JSON, console
lines, the tool and host call trace, and the error text. Every natively
accepted script must match exactly. The exceptions are annotated in the case
itself with a `-- divergence --` section:

- **Goja bugs, where the native engine follows the spec:**
  `[-0].includes(0)`, and `ToInt32` beyond int64.
- **Async dialect:** batch items run sequentially and tool calls return
  values, so there is no microtask interleaving.
- **Lone surrogates:** strings are UTF-8 internally, so a lone UTF-16
  surrogate becomes U+FFFD. Goja makes the same conversion when it exports a
  value.
- **Functions in a returned value:** they are encoded as `null`, where Goja
  cannot encode them at all.
- **Hosts can make these fall back:** very large sparse arrays and dynamic
  `__proto__` writes raise `ErrRuntimeUnsupported`.

**Intl** cannot be compared with Goja, which has none. `testdata/intl` holds
golden files generated with Node (V8 + ICU) by the scripts in
`internal/intlgen`: thousands of seeded option combinations, time zones,
instants and error cases, replayed by `TestIntl*`. Every case must match V8
exactly or be declined (ambiguous historical zone names are).

Objects decoded from host maps enumerate in sorted key order. Goja's
enumeration of Go maps is random.

Set `TOOLSCRIPT_CORPUS_JSONL=/path/to/private.jsonl` (one `{"code": …}` per
line) to run your own private scripts through the same differential. Their
tools are answered with a generic fixture.

## Performance

`TOOLSCRIPT_PERF=1 go test -run 'TestCorpusPerf|TestScalingPerf'` measures
process CPU time for native compile+execute against a Goja VM set up per run
like Ditto's runner (40 tool bindings). Results from an idle linux/amd64 host:

- **Corpus (1,447 scripts):** native is faster on every script. It uses 3.7×
  less CPU and allocates 5.6× fewer bytes overall. The median speedup is 4.6×;
  p5 is 2.3× and p95 is 7.8×.
- **Scaling (one tool call plus N loop iterations):** native stays faster at
  every size from 1 to 100,000 iterations. At 100,000 iterations it is 1.37×
  faster on arithmetic, 1.38× on closure calls, 1.34× on grouping, 1.25× on
  array pipelines and 2.1× on `JSON.stringify` of built rows.

## Limits and cancellation

- **Source and nesting:** source is capped at 32 KiB and expression nesting
  at 90.
- **Execute defaults:** 64 tool calls, 64 host calls, 1,000,000 steps
  (statements, calls and loop iterations), 1,024 items per batch, call depth
  1,000, arrays of 2²⁴ elements and strings of 16 MiB.
- **Cancellation:** the context is checked every 64 steps and before every
  dispatch. Workers are always joined, and host panics become errors.
- **Uncatchable errors:** step limits, cancellation, fatal host errors (see
  `Fatal`) and stack overflow cannot be caught by `try`/`catch`. Tool
  failures, and exhausted tool budgets, surface as catchable Goja-style
  `GoError`s.
- **No retries:** execution never retries a call. Fall back only on
  `ErrUnsupported`, or on `ErrRuntimeUnsupported` before any effect.

## Validation

```sh
go test -race ./...
go test -run '^$' -fuzz FuzzCompile -fuzztime=30s
go vet ./...
```
