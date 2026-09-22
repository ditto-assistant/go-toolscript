package toolscript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func options() CompileOptions {
	return CompileOptions{ResolveCall: func(name string) (string, bool) { return name, true }, Resolve: func(p []string) (string, bool) {
		if len(p) < 2 || (p[0] != "mcp" && p[0] != "tools") {
			return "", false
		}
		return strings.Join(p[1:], "."), true
	}, Batches: true}
}
func compile(t testing.TB, s string) *Program {
	t.Helper()
	p, e := Compile(s, options())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func encoded(t testing.TB, v any) string {
	t.Helper()
	b, e := Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func echo(_ context.Context, _ string, a any) (any, error) { return a, nil }
func TestExamples(t *testing.T) {
	cases := []struct{ code, want string }{
		{`const r = mcp.search_memories({queries:["Alex Jared Slack conversation thread", "Alex and Jared Slack", "author Alex author Jared"], since:"2026-09-15", until:"2026-09-21T23:59:59-04:00", limit:30, timezone:"America/New_York"}); return r;`, `{"limit":30,"queries":["Alex Jared Slack conversation thread","Alex and Jared Slack","author Alex author Jared"],"since":"2026-09-15","timezone":"America/New_York","until":"2026-09-21T23:59:59-04:00"}`},
		{`/* ; return bad */ const a=tools['echo']({str:'a\'b', x:-1.5, yes:true, no:null,}); return mcp.echo(a);`, `{"no":null,"str":"a'b","x":-1.5,"yes":true}`},
		{"const r=mcp.echo({x:1})\nreturn\nr", `null`},
		{`const a=1,b=2; let {x:renamed} = mcp.echo({x:[a,b]}); var [one,two]=renamed; return {one,two};`, `{"one":1,"two":2}`},
		{`return [1,2,3].map(x => mcp.echo({x}));`, `[{"x":1},{"x":2},{"x":3}]`},
		{`const inputs=[{id:1},{id:2}]; return await Promise.all(inputs.map(async (x,i) => {const r=await mcp.echo(x); return {id:r.id,i};}));`, `[{"i":0,"id":1},{"i":1,"id":2}]`},
		{`const [a,b]=await Promise.all([mcp.echo({a:1}),tools.echo({b:2})]); return {a,b};`, `{"a":{"a":1},"b":{"b":2}}`},
		{`const r=mcp.echo({}); return {missing:r.nope, array:[r.nope]};`, `{"array":[null]}`},
		{`return mcp.call('raw-name', {ok:true});`, `{"ok":true}`},
		{`return await Promise.allSettled([]);`, `[]`},
	}
	for _, tt := range cases {
		t.Run(tt.code, func(t *testing.T) {
			r, e := compile(t, tt.code).Execute(context.Background(), ExecuteOptions{Dispatch: echo, Parallelism: 4})
			if e != nil {
				t.Fatal(e)
			}
			if got := encoded(t, r.Value); got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}
func TestUnsupportedBeforeDispatch(t *testing.T) {
	for _, s := range []string{`mcp.echo({}); for (;;) {}`, `mcp.echo({}); return Date.now()`, `mcp.echo({}); throw new Error('x')`, `mcp.echo({}); return eval('1')`, `const mcp={};return mcp.echo({})`, `const Promise={};return Promise.all([])`, `return mcp.echo({...x})`, `return mcp.echo({get x(){return 1}})`, `return mcp.echo({__proto__:{x:1}})`, `return mcp.echo({}).constructor`, `return mcp.echo({}).toString`, `return mcp.echo({}).x()`, `let a=1;a=2;return a`, `return Promise.all([Promise.all([])])`, `return mcp.echo({}); try {} catch(e){}`, `const a=a;return a`, `const a=1;const a=2`, `return [1].map(async x=>mcp.echo(x))`, `return mcp.echo(/x/)`, `return mcp.echo(1,2)`, `return mcp.call(name,{})`, `const r=mcp.echo({});return r[1e21]`, `return {0.1:1}`} {
		if _, err := Compile(s, options()); !errors.Is(err, ErrUnsupported) {
			t.Errorf("accepted %q (%v)", s, err)
		}
	}
	opts := options()
	opts.Batches = false
	if _, err := Compile(`return await mcp.echo({})`, opts); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
func TestDifferentialSynchronous(t *testing.T) {
	for _, source := range []string{
		`const r=mcp.echo({a:1,b:"hi",c:[true,null]});return r;`,
		`const a=mcp.echo({x:[{id:2}]});return mcp.echo({id:a.x[0].id});`,
		`const a=[1,2].map((x,i)=>{const r=mcp.echo({x,i});return r;});return a;`,
		`const {a:x,b:y}=mcp.echo({a:1,b:2});return [x,y];`,
		`return mcp.echo({omit:undefined,a:[undefined]});`,
		`const [a,b]="😀x";return {a,b};`,
		`const {0:x}="hello";return x;`,
		`const r={0x10:"yes"};return r[0x10];`,
		"return\n mcp.echo({a:1})", `return {x:0x10,y:1e2,z:-0};`,
	} {
		t.Run(source, func(t *testing.T) {
			var fastCalls, jsCalls []string
			r, err := compile(t, source).Execute(context.Background(), ExecuteOptions{Dispatch: func(ctx context.Context, n string, a any) (any, error) {
				b, err := MarshalExport(a)
				if err != nil {
					return nil, err
				}
				fastCalls = append(fastCalls, n+string(b))
				var v any
				err = json.Unmarshal(b, &v)
				return v, err
			}})
			if err != nil {
				t.Fatal(err)
			}
			vm := goja.New()
			_ = vm.Set("mcp", map[string]any{"echo": func(call goja.FunctionCall) goja.Value {
				b, err := json.Marshal(call.Argument(0).Export())
				if err != nil {
					panic(err)
				}
				jsCalls = append(jsCalls, "echo"+string(b))
				var v any
				_ = json.Unmarshal(b, &v)
				return vm.ToValue(v)
			}})
			v, err := vm.RunString("(function(){\n" + source + "\n})()")
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if !goja.IsUndefined(v) {
				want = v.Export()
			}
			got := encoded(t, r.Value)
			wb, _ := json.Marshal(want)
			var gv, wv any
			_ = json.Unmarshal([]byte(got), &gv)
			_ = json.Unmarshal(wb, &wv)
			if !reflect.DeepEqual(gv, wv) || !reflect.DeepEqual(fastCalls, jsCalls) {
				t.Fatalf("fast=%s calls=%v JS=%s calls=%v", got, fastCalls, wb, jsCalls)
			}
		})
	}
}
func TestParallelAndLimits(t *testing.T) {
	p := compile(t, `return Promise.all([mcp.echo(0),mcp.echo(1),mcp.echo(2),mcp.echo(3)]);`)
	var active, peak atomic.Int64
	r, err := p.Execute(context.Background(), ExecuteOptions{Parallelism: 2, Dispatch: func(ctx context.Context, n string, a any) (any, error) {
		v := active.Add(1)
		for {
			p := peak.Load()
			if v <= p || peak.CompareAndSwap(p, v) {
				break
			}
		}
		time.Sleep(time.Millisecond * 5)
		active.Add(-1)
		return a, nil
	}})
	if err != nil || peak.Load() != 2 || encoded(t, r.Value) != "[0,1,2,3]" {
		t.Fatalf("%+v %v peak=%d", r, err, peak.Load())
	}
	r, err = p.Execute(context.Background(), ExecuteOptions{Parallelism: 4, MaxCalls: 2, Dispatch: echo})
	if err == nil || r.Calls != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = p.Execute(context.Background(), ExecuteOptions{MaxItems: 2, Dispatch: echo})
	if err == nil {
		t.Fatal("no item limit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err = p.Execute(ctx, ExecuteOptions{Dispatch: echo})
	if !errors.Is(err, context.Canceled) || r.Calls != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestFailureAndFatal(t *testing.T) {
	p := compile(t, `return Promise.allSettled([mcp.echo(1),mcp.echo(2)]);`)
	boom := errors.New("revoked")
	r, err := p.Execute(context.Background(), ExecuteOptions{Dispatch: func(context.Context, string, any) (any, error) { return nil, boom }})
	if err != nil || encoded(t, r.Value) != `[{"reason":"revoked","status":"rejected"},{"reason":"revoked","status":"rejected"}]` {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = p.Execute(context.Background(), ExecuteOptions{Dispatch: func(context.Context, string, any) (any, error) { return nil, boom }, Fatal: func(err error) bool { return errors.Is(err, boom) }})
	if !errors.Is(err, boom) || !r.Aborted || r.Calls != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = compile(t, `return mcp.echo(1)`).Execute(context.Background(), ExecuteOptions{Dispatch: func(context.Context, string, any) (any, error) { panic("host") }})
	if err == nil {
		t.Fatal("panic not contained")
	}
}
func TestBudgets(t *testing.T) {
	if _, err := Compile(strings.Repeat(" ", 33<<10), options()); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := Compile("return "+strings.Repeat("[", 100)+"0"+strings.Repeat("]", 100), options()); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	p := compile(t, `return [1,2,3].map(x=>[1,2,3].map(y=>mcp.echo({x,y})));`)
	if _, err := p.Execute(context.Background(), ExecuteOptions{Dispatch: echo, MaxSteps: 4}); err == nil {
		t.Fatal("no step limit")
	}
	var v any = float64(1)
	for range 20 {
		v = []any{v, v}
	}
	if _, err := Marshal(v); err == nil {
		t.Fatal("no expansion limit")
	}
}
func FuzzCompile(f *testing.F) {
	for _, s := range []string{`return mcp.echo({x:1});`, `const [a,b]=await Promise.all([mcp.a(),mcp.b()]);return {a,b};`, "return\n1", "/*comment*/", `return bash("printf hi",{items:[1]},undefined);`, `return Promise.all([bash("one"),bash("two")]);`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		opts := options()
		opts.HostFunctions = hostOptions().HostFunctions
		p, err := Compile(s, opts)
		if err != nil {
			return
		}
		r, _ := p.Execute(context.Background(), ExecuteOptions{Dispatch: echo, HostDispatch: func(_ context.Context, _ string, args []any) (any, error) { return args, nil }, MaxHostCalls: 10, MaxSteps: 100, MaxCalls: 10, MaxItems: 10})
		_, _ = Marshal(r.Value)
	})
}
func BenchmarkCompileExecute(b *testing.B) {
	s := `const r=mcp.echo({queries:["hello"],limit:30});return r;`
	b.ReportAllocs()
	for b.Loop() {
		p, err := Compile(s, options())
		if err != nil {
			b.Fatal(err)
		}
		_, err = p.Execute(context.Background(), ExecuteOptions{Dispatch: echo})
		if err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkGoja(b *testing.B) {
	s := `(function(){const r=mcp.echo({queries:["hello"],limit:30});return r;})()`
	b.ReportAllocs()
	for b.Loop() {
		vm := goja.New()
		_ = vm.Set("mcp", map[string]any{"echo": func(c goja.FunctionCall) goja.Value { return c.Argument(0) }})
		if _, err := vm.RunString(s); err != nil {
			b.Fatal(fmt.Sprint(err))
		}
	}
}

func TestRawCallDoesNotCollideWithPropertyPath(t *testing.T) {
	opts := CompileOptions{Resolve: func(parts []string) (string, bool) { return strings.Join(parts, "/"), true }, ResolveCall: func(name string) (string, bool) { return "raw/" + name, true }}
	for _, tc := range []struct{ source, want string }{{`return mcp.call("foo",{});`, "raw/foo"}, {`return mcp.call.foo({});`, "mcp/call/foo"}, {`return mcp["x.y"]({});`, "mcp/x.y"}, {`return mcp.x.y({});`, "mcp/x/y"}} {
		p, err := Compile(tc.source, opts)
		if err != nil {
			t.Fatal(err)
		}
		r, err := p.Execute(context.Background(), ExecuteOptions{Dispatch: func(_ context.Context, name string, _ any) (any, error) { return name, nil }})
		if err != nil || r.Value != tc.want {
			t.Fatalf("%s: %+v %v", tc.source, r, err)
		}
	}
}
