package toolscript

import (
	"errors"
	"testing"
)

func TestAsyncShapes(t *testing.T) {
	native := []string{
		`const r = await mcp.a({}); return r;`,
		`return await Promise.all([1, 2].map(async x => (await mcp.a({x})).x));`,
		`const rs = await Promise.allSettled(["a"].map(id => mcp.a({id}))); return rs;`,
		`async function load(id) { return (await mcp.a({id})).id; } const v = await load(1); return v;`,
		`async function load(id) { return mcp.a({id}); } return await Promise.all([load(1), load(2)]);`,
		`const load = async id => mcp.a({id}); return await Promise.all([1, 2].map(id => load(id)));`,
		`const load = async id => mcp.a({id}); return Promise.all([1].map(load));`,
		`const ps = [1, 2].map(async x => mcp.a({x})); return await Promise.all(ps);`,
		`const jobs = [async () => 1, async () => 2]; return await Promise.allSettled(jobs.map(j => j()));`,
		`async function retry(f) { try { return await f(); } catch (e) { return await f(); } } return await retry(async () => mcp.a({}));`,
		`const api = {async get(id) { return mcp.a({id}); }}; return await api.get(1);`,
		`async function f(x) { return x; } return (await f(1)) + 1;`,
		`async function f(x) { return x; } return true ? f(1) : f(2);`,
		`return (async () => mcp.a({}))();`,
		`const r = mcp.a({}); let out; try { out = r.then(x => x); } catch (e) { out = e.name; } return out;`, // no async dialect: plain TypeError
		`for (const id of [1, 2]) { await mcp.a({id}); } return 1;`,
	}
	for _, src := range native {
		if _, err := Compile(src, options()); err != nil {
			t.Errorf("declined %q: %v", src, err)
		}
	}
	declined := []string{
		`const r = await mcp.a({}).then(x => x.id); return r;`,
		`const r = await Promise.all([1].map(id => mcp.a({id}).catch(e => null))); return r;`,
		`[1, 2].forEach(async id => { await mcp.a({id}); }); return 1;`,
		`const ok = [1, 2].filter(async id => (await mcp.a({id})).ok); return ok;`,
		`const s = await [1, 2].reduce(async (acc, id) => (await acc) + id, 0); return s;`,
		`async function load() { return (await mcp.a({})).v; } const p = load(); return typeof p;`,
		`async function main() { return mcp.a({}); } main(); return 1;`,
		`async function f() { return {x: 1}; } return f().x;`,
		`const ps = Promise.all([mcp.a({})]); return ps;`,
		`return [1].map(async x => x);`,
		`const load = async x => x; [1].forEach(load); return 1;`,
		`const api = {async get() { return 1; }}; const p = api.get(); return p;`,
		`(async () => { await mcp.a({}); })(); return 1;`,
	}
	for _, src := range declined {
		_, err := Compile(src, options())
		if !errors.Is(err, ErrUnsupported) || !errors.Is(err, errAsyncShape) {
			t.Errorf("accepted %q: %v", src, err)
		}
	}
}
