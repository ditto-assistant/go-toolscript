package toolscript

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func hostOptions() CompileOptions {
	return CompileOptions{Batches: true, HostFunctions: map[string]HostFunction{"bash": {MinArgs: 1, MaxArgs: 3, LiteralStringArgs: []int{0}}}}
}
func TestHostFunctions(t *testing.T) {
	for _, source := range []string{
		`return bash("echo", {a:1}, undefined);`,
		`const r=await bash("echo", {a:1}, undefined); return r;`,
		`return await Promise.all([bash("echo",{a:1},undefined)]);`,
		`return [1].map(x=>bash("echo",{a:x},undefined));`,
	} {
		p, err := Compile(source, hostOptions())
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		r, err := p.Execute(t.Context(), ExecuteOptions{HostDispatch: func(_ context.Context, n string, args []any) (any, error) {
			calls++
			if n != "bash" || !reflect.DeepEqual(args, []any{"echo", map[string]any{"a": float64(1)}, Undefined}) {
				t.Fatalf("%s %#v", n, args)
			}
			return map[string]any{"stdout": "ok"}, nil
		}})
		if err != nil || calls != 1 || r.HostCalls != 1 || r.Calls != 0 {
			t.Fatalf("%+v calls=%d %v", r, calls, err)
		}
	}
}
func TestHostCompileBoundary(t *testing.T) {
	for _, source := range []string{
		`return bash();`, `return bash(42);`, `const s="echo";return bash(s);`,
		`return bash("echo",1,2,3);`, `return bash.apply(null,["echo"]);`,
		`return other("echo");`, `return bash(...["echo"]);`, `return typeof bash.x;`,
	} {
		if _, err := Compile(source, hostOptions()); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("accepted %s: %v", source, err)
		}
	}
	if _, err := Compile(`return bash("echo");`, CompileOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	// A local binding named like a host function shadows it lexically.
	for _, source := range []string{`const bash=1;return bash("echo");`, `return bash("echo");var bash;`} {
		p, err := Compile(source, hostOptions())
		if err != nil {
			t.Fatal(err)
		}
		r, err := p.Execute(t.Context(), ExecuteOptions{HostDispatch: func(context.Context, string, []any) (any, error) { t.Fatal("host called"); return nil, nil }})
		if err == nil || r.HostCalls != 0 || !strings.Contains(err.Error(), "TypeError") {
			t.Fatalf("%s: %+v %v", source, r, err)
		}
	}
}
func TestHostBudgetAndFailures(t *testing.T) {
	p, err := Compile(`bash("one");return bash("two");`, hostOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"limit", "error", "panic", "cancel"} {
		calls := 0
		ctx, cancel := context.WithCancel(t.Context())
		if mode == "cancel" {
			cancel()
		}
		r, err := p.Execute(ctx, ExecuteOptions{MaxHostCalls: 1, HostDispatch: func(context.Context, string, []any) (any, error) {
			calls++
			if mode == "panic" {
				panic("boom")
			}
			if mode == "error" {
				return nil, errors.New("boom")
			}
			return nil, nil
		}})
		cancel()
		want := 1
		if mode == "cancel" {
			want = 0
		}
		if err == nil || calls != want || r.HostCalls != want || r.Calls != 0 {
			t.Fatalf("%s %+v calls=%d %v", mode, r, calls, err)
		}
	}
	p, err = Compile(`return Promise.allSettled([bash("a"),bash("b")]);`, hostOptions())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r, err := p.Execute(t.Context(), ExecuteOptions{Fatal: func(error) bool { return true }, HostDispatch: func(context.Context, string, []any) (any, error) { calls++; return nil, errors.New("fatal") }})
	if err == nil || !strings.Contains(err.Error(), "fatal") || !r.Aborted || calls != 1 {
		t.Fatalf("%+v calls=%d %v", r, calls, err)
	}
}
