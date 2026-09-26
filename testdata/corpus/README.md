# Code-mode script corpus

Each `.txtar` file is one JavaScript program body of the kind a model sends to a
`run_code` tool, plus deterministic mocks for every tool it calls. The shapes and
idioms mirror real model output; all names, ids and content are invented.

## File format
```
One or more comment lines describing what the script exercises.
-- script.js --
<program body; the runner wraps it as (function(){\n<code>\n})()>
-- tools.json --
{ "<binding path>": <response>, ... }
-- bash.json --          (optional)
{ "<exact bash script>": {"stdout": "...", "stderr": "", "exit_code": 0, "json": ...} }
-- workspace.json --     (optional)
{ "mount_result": {"<ref>": {...file...}}, "workspace_files": [ {...file...} ] }
```

### tools.json keys
Keys are the binding path without the leading `mcp.` / `tools.`:

| Script call                               | Key                        |
|-------------------------------------------|----------------------------|
| `mcp.search_memories(a)`, `tools.search_memories(a)`, `tools["search_memories"](a)` | `search_memories` |
| `mcp.github.list_pull_requests(a)`        | `github.list_pull_requests`|
| `mcp["team-backroom"].get_review(a)`      | `team-backroom.get_review` |
| `mcp.call("mcp__acme-tracker__list_tickets", a)` | `mcp__acme-tracker__list_tickets` |

Every tool a script calls has a key. Values:
- any JSON value: returned (as parsed JSON) on every call;
- `null`: the default echo response `{"tool": <name>, "args": <args>}`;
- `{"$sequence": [a, b, ...]}`: successive values per call, the last one repeating;
- `{"$error": "message"}`: the call fails with a catchable JS error carrying that message.

### bash.json / workspace.json
`bash(script, stdin?, options?)` looks the script string up exactly; unlisted
scripts return `{"stdout": "", "stderr": "", "exit_code": 0, "json": null}`.
`mount_result(ref)` returns the listed file object; `workspace_files()` returns
the listed array.

## Categories
| Directory      | Contents |
|----------------|----------|
| `simple/`      | direct calls, projections, destructuring, raw/bracket/alias bindings, `JSON.stringify(r).slice(0, N)` |
| `console/`     | captured `console.*` output with and without a return value |
| `trycatch/`    | `$error` tools caught, finally, rethrow, optional catch binding |
| `control/`     | branches, loops, switch, `?.`, `??`, `\|\|`, early returns |
| `strings/`     | template literals and string methods (no regex) |
| `collections/` | array and object helpers, grouping, dedupe, sorting with total comparators |
| `functions/`   | arrows, hoisting, closures, recursion, default/rest parameters, IIFEs |
| `json/`        | stringify indent/replacers, parse of tool text and bash stdout |
| `batches/`     | async dialect: `await`, `Promise.all`, `Promise.allSettled` (run wrapped in an async function) |
| `workspace/`   | `bash`, `mount_result`, `workspace_files` |
| `crazy/`       | long multi-source pipelines (30-150 lines) |
| `errors/`      | scripts that end in an uncaught error |
| `unsupported/` | valid JS a restricted engine may reject: regex, Date, Math.random, classes, generators, `this`, labels, `with`, `eval`, Map/Set, `arguments`, tagged templates |

Outside `unsupported/`, scripts are deterministic (no `Date`, `Math.random`, timers,
`Intl` or locale APIs; sort comparators are total) and only `errors/` scripts end in an error.
