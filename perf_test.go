package toolscript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// perfFixture pre-parses every fixture response so neither engine pays JSON
// decoding inside the measurement (the host decodes once in both runtimes).
type perfFixture struct {
	tools map[string][]any
	errs  map[string]error
	bash  map[string]any
}

func newPerfFixture(c *corpusCase) *perfFixture {
	f := &perfFixture{tools: map[string][]any{}, errs: map[string]error{}, bash: map[string]any{}}
	for name, raw := range c.tools {
		if string(raw) == "null" {
			var v any
			if c.generic {
				_ = json.Unmarshal([]byte(genericResult), &v)
			} else {
				v = map[string]any{"tool": name}
			}
			f.tools[name] = []any{v}
			continue
		}
		var special struct {
			Sequence []json.RawMessage `json:"$sequence"`
			Error    *string           `json:"$error"`
		}
		if json.Unmarshal(raw, &special) == nil && special.Error != nil {
			f.errs[name] = errors.New(*special.Error)
			continue
		}
		if json.Unmarshal(raw, &special) == nil && len(special.Sequence) > 0 {
			for _, s := range special.Sequence {
				var v any
				_ = json.Unmarshal(s, &v)
				f.tools[name] = append(f.tools[name], v)
			}
			continue
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		f.tools[name] = []any{v}
	}
	for script, raw := range c.bash {
		var v any
		_ = json.Unmarshal(raw, &v)
		f.bash[script] = v
	}
	return f
}

func (f *perfFixture) respond(calls map[string]int, name string) (any, error) {
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	seq := f.tools[name]
	if len(seq) == 0 {
		return map[string]any{"tool": name}, nil
	}
	i := min(calls[name], len(seq)-1)
	calls[name]++
	return seq[i], nil
}

func (f *perfFixture) bashResult(script string) any {
	if v, ok := f.bash[script]; ok {
		return v
	}
	return map[string]any{"stdout": "", "stderr": "", "exit_code": float64(0), "json": nil}
}

func perfNative(c *corpusCase, f *perfFixture) error {
	p, err := Compile(c.script, c.compileOptions())
	if err != nil {
		return err
	}
	calls := map[string]int{}
	_, err = p.Execute(context.Background(), ExecuteOptions{
		Console: func(string, string) {},
		Dispatch: func(_ context.Context, name string, arg any) (any, error) {
			if _, err := MarshalExport(arg); err != nil {
				return nil, err
			}
			return f.respond(calls, name)
		},
		HostDispatch: func(_ context.Context, name string, args []any) (any, error) {
			s, _ := args[0].(string)
			return f.bashResult(s), nil
		},
	})
	var thrown *Throw
	if errors.As(err, &thrown) {
		err = nil
	}
	return err
}

// perfGoja mirrors the backend runner's per-run setup: fresh VM, stripped
// globals, console, one binding per catalog tool and the workspace globals.
func perfGoja(c *corpusCase, f *perfFixture, catalog int) error {
	vm := goja.New()
	vm.SetMaxCallStackSize(2000)
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	for _, g := range []string{"require", "process", "fetch", "XMLHttpRequest", "setTimeout", "setInterval", "setImmediate", "WebAssembly", "Worker"} {
		_ = vm.GlobalObject().Delete(g)
	}
	async := strings.Contains(c.script, "await") || strings.Contains(c.script, "Promise")
	if !async {
		_ = vm.GlobalObject().Delete("Promise")
	}
	calls := map[string]int{}
	root := vm.NewObject()
	servers := map[string]*goja.Object{}
	bind := func(name string) {
		fn := func(call goja.FunctionCall) goja.Value {
			if a := call.Argument(0); !goja.IsUndefined(a) {
				if _, err := json.Marshal(a.Export()); err != nil {
					panic(vm.NewTypeError("invalid arguments"))
				}
			}
			v, err := f.respond(calls, name)
			if err != nil {
				panic(vm.NewGoError(err))
			}
			return vm.ToValue(v)
		}
		server, local, nested := strings.Cut(name, ".")
		if !nested {
			_ = root.Set(name, fn)
			return
		}
		if servers[server] == nil {
			servers[server] = vm.NewObject()
			_ = root.Set(server, servers[server])
		}
		_ = servers[server].Set(local, fn)
	}
	for name := range c.tools {
		bind(name)
	}
	for i := len(c.tools); i < catalog; i++ {
		bind(fmt.Sprintf("catalog_tool_%d", i))
	}
	_ = root.Set("call", func(call goja.FunctionCall) goja.Value {
		v, err := f.respond(calls, call.Argument(0).String())
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(v)
	})
	_ = vm.Set("mcp", root)
	_ = vm.Set("tools", root)
	console := vm.NewObject()
	write := func(call goja.FunctionCall) goja.Value {
		for _, a := range call.Arguments {
			_ = gojaConsoleArg(a)
		}
		return goja.Undefined()
	}
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(name, write)
	}
	_ = vm.Set("console", console)
	_ = vm.Set("bash", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(f.bashResult(call.Argument(0).String()))
	})
	_ = vm.Set("mount_result", func(call goja.FunctionCall) goja.Value { return vm.ToValue(map[string]any{}) })
	_ = vm.Set("workspace_files", func(call goja.FunctionCall) goja.Value { return vm.ToValue([]any{}) })
	wrapper := "(function(){\n" + c.script + "\n})()"
	if async {
		wrapper = "(async function(){\n" + c.script + "\n})()"
	}
	v, err := vm.RunString(wrapper)
	if err == nil && v != nil {
		_, _ = json.Marshal(v.Export())
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		err = nil
	}
	return err
}

// cpuTime is this process's user+system CPU time. The workstation running
// these measurements is usually oversubscribed, so wall time is noise; CPU
// time (including GC work on other threads) is the cost we care about.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

type sample struct {
	cpu    time.Duration
	bytes  uint64
	allocs uint64
}

func timeIt(budget time.Duration, f func() error) (sample, error) {
	if err := f(); err != nil { // warm-up
		return sample{}, err
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n := 0
	start := cpuTime()
	for cpuTime()-start < budget || n < 3 {
		if err := f(); err != nil {
			return sample{}, err
		}
		n++
	}
	elapsed := cpuTime() - start
	runtime.ReadMemStats(&after)
	return sample{cpu: elapsed / time.Duration(n), bytes: (after.TotalAlloc - before.TotalAlloc) / uint64(n), allocs: (after.Mallocs - before.Mallocs) / uint64(n)}, nil
}

// TestCorpusPerf compares native execution with a backend-shaped Goja run per
// corpus script. Enable with TOOLSCRIPT_PERF=1; TOOLSCRIPT_PERF_CATALOG sets
// how many tool bindings the Goja side installs (default 40).
func TestCorpusPerf(t *testing.T) {
	if os.Getenv("TOOLSCRIPT_PERF") == "" {
		t.Skip("set TOOLSCRIPT_PERF=1")
	}
	catalog := 40
	if v := os.Getenv("TOOLSCRIPT_PERF_CATALOG"); v != "" {
		fmt.Sscan(v, &catalog)
	}
	type row struct {
		name         string
		native, goja time.Duration
		nb, gb       uint64
		steps        int64
	}
	var rows []row
	for _, c := range loadCorpus(t) {
		c := c
		if _, err := Compile(c.script, c.compileOptions()); err != nil {
			continue
		}
		f := newPerfFixture(&c)
		ns, err := timeIt(20*time.Millisecond, func() error { return perfNative(&c, f) })
		if err != nil {
			continue // step limits etc.; not a timing sample
		}
		gs, err := timeIt(20*time.Millisecond, func() error { return perfGoja(&c, f, catalog) })
		if err != nil {
			continue
		}
		rows = append(rows, row{name: c.name, native: ns.cpu, goja: gs.cpu, nb: ns.bytes, gb: gs.bytes, steps: countSteps(&c, f)})
	}
	sort.Slice(rows, func(i, j int) bool {
		return float64(rows[i].goja)/float64(rows[i].native) < float64(rows[j].goja)/float64(rows[j].native)
	})
	var totalN, totalG time.Duration
	var bytesN, bytesG uint64
	slower := 0
	for _, r := range rows {
		totalN += r.native
		totalG += r.goja
		bytesN += r.nb
		bytesG += r.gb
		if r.native > r.goja {
			slower++
		}
	}
	var report strings.Builder
	fmt.Fprintf(&report, "scripts=%d catalog=%d cpu: native_total=%v goja_total=%v aggregate_speedup=%.2fx native_slower=%d\n", len(rows), catalog, totalN, totalG, float64(totalG)/float64(totalN), slower)
	fmt.Fprintf(&report, "alloc bytes: native_total=%d goja_total=%d ratio=%.2fx\n", bytesN, bytesG, float64(bytesG)/float64(bytesN))
	ratios := make([]float64, len(rows))
	for i, r := range rows {
		ratios[i] = float64(r.goja) / float64(r.native)
	}
	for _, q := range []float64{0, 0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 1} {
		i := min(int(q*float64(len(ratios))), len(ratios)-1)
		fmt.Fprintf(&report, "p%-3.0f speedup %.2fx\n", q*100, ratios[i])
	}
	fmt.Fprintf(&report, "\nslowest relative to goja:\n")
	for _, r := range rows[:min(25, len(rows))] {
		fmt.Fprintf(&report, "  %-45s native=%-10v goja=%-10v speedup=%.2fx steps=%d bytes=%d/%d\n", r.name, r.native, r.goja, float64(r.goja)/float64(r.native), r.steps, r.nb, r.gb)
	}
	t.Log("\n" + report.String())
	if path := os.Getenv("TOOLSCRIPT_PERF_REPORT"); path != "" {
		var all strings.Builder
		all.WriteString(report.String())
		all.WriteString("\nall:\n")
		for _, r := range rows {
			fmt.Fprintf(&all, "%s\t%d\t%d\t%.3f\t%d\t%d\t%d\n", r.name, r.native.Nanoseconds(), r.goja.Nanoseconds(), float64(r.goja)/float64(r.native), r.steps, r.nb, r.gb)
		}
		_ = os.WriteFile(path, []byte(all.String()), 0o644)
	}
}

// countSteps reports interpreter steps as a complexity measure.
func countSteps(c *corpusCase, f *perfFixture) int64 {
	p, err := Compile(c.script, c.compileOptions())
	if err != nil {
		return 0
	}
	calls := map[string]int{}
	opts := ExecuteOptions{
		Dispatch: func(_ context.Context, name string, _ any) (any, error) { return f.respond(calls, name) },
		HostDispatch: func(_ context.Context, name string, args []any) (any, error) {
			s, _ := args[0].(string)
			return f.bashResult(s), nil
		},
	}
	res, _ := p.Execute(context.Background(), opts)
	return int64(res.Steps)
}

// scalingWorkloads grow CPU work with N while keeping a single tool call, to
// find where a bytecode VM's setup cost is amortized over interpreted work.
var scalingWorkloads = map[string]string{
	"arith":     `const r = mcp.fetch({}); let s = 0; for (let i = 0; i < __N__; i++) { s += (i * 2) % 7; } return s;`,
	"arrays":    `const r = mcp.fetch({}); const a = []; for (let i = 0; i < __N__; i++) a.push({id: i, v: i % 10}); return a.filter(x => x.v > 5).map(x => x.id).reduce((p, c) => p + c, 0);`,
	"strings":   "const r = mcp.fetch({}); let out = \"\"; for (let i = 0; i < __N__; i++) { out += `row ${i}: ${r.name}\\n`; } return out.length;",
	"grouping":  `const r = mcp.fetch({}); const g = {}; for (let i = 0; i < __N__; i++) { const k = "k" + (i % 50); g[k] = (g[k] || 0) + 1; } return Object.keys(g).length;`,
	"calls":     `const r = mcp.fetch({}); function f(x) { return x + 1; } let s = 0; for (let i = 0; i < __N__; i++) s = f(s); return s;`,
	"stringify": `const r = mcp.fetch({}); const rows = []; for (let i = 0; i < __N__; i++) rows.push({id: "item-" + i, title: r.name, tags: ["a", "b"], score: i / 3}); return JSON.stringify(rows).slice(0, 4000);`,
}

// TestScalingPerf reports native vs Goja CPU time as work grows.
func TestScalingPerf(t *testing.T) {
	if os.Getenv("TOOLSCRIPT_PERF") == "" {
		t.Skip("set TOOLSCRIPT_PERF=1")
	}
	names := make([]string, 0, len(scalingWorkloads))
	for name := range scalingWorkloads {
		names = append(names, name)
	}
	sort.Strings(names)
	var report strings.Builder
	for _, name := range names {
		for _, n := range []int{1, 10, 100, 1000, 10000, 100000} {
			c := corpusCase{name: name, script: strings.ReplaceAll(scalingWorkloads[name], "__N__", fmt.Sprint(n)), tools: map[string]json.RawMessage{"fetch": json.RawMessage(`{"name":"widget"}`)}}
			f := newPerfFixture(&c)
			ns, err := timeIt(50*time.Millisecond, func() error { return perfNative(&c, f) })
			if err != nil {
				t.Fatalf("%s/%d native: %v", name, n, err)
			}
			gs, err := timeIt(50*time.Millisecond, func() error { return perfGoja(&c, f, 40) })
			if err != nil {
				t.Fatalf("%s/%d goja: %v", name, n, err)
			}
			fmt.Fprintf(&report, "%-10s N=%-7d native=%-12v goja=%-12v speedup=%.2fx\n", name, n, ns.cpu, gs.cpu, float64(gs.cpu)/float64(ns.cpu))
		}
	}
	t.Log("\n" + report.String())
}
