package toolscript

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// intlGolden is a Node-generated Intl golden file (see internal/intlgen).
type intlGolden struct {
	Node     string `json:"node"`
	ICU      string `json:"icu"`
	TZ       string `json:"tz"`
	Template string `json:"template"` // script for cases given as loc/opts/dates
	Cases    []struct {
		Name   string `json:"name"`
		Script string `json:"script"`
		Loc    string `json:"loc"`
		Opts   string `json:"opts"`
		Dates  string `json:"dates"`
		Want   string `json:"want"`
	} `json:"cases"`
}

// runIntlGolden replays every case natively and compares with V8's output.
// A case the engine declines (ErrUnsupported / ErrRuntimeUnsupported) is
// counted, not failed; mustRun names cases that may not decline.
func runIntlGolden(t *testing.T, path string, mustRun func(name string) bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var g intlGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(g.TZ)
	if err != nil {
		t.Skipf("no time zone data for %s: %v", g.TZ, err)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	var passed, declined, failed int
	var declinedNames []string
	for _, c := range g.Cases {
		if c.Script == "" {
			c.Script = strings.NewReplacer("$LOC", c.Loc, "$OPTS", c.Opts, "$DATES", c.Dates).Replace(g.Template)
		}
		got, err := runIntlScript(c.Script, loc, now)
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrRuntimeUnsupported) {
			declined++
			declinedNames = append(declinedNames, c.Name+": "+err.Error())
			if mustRun != nil && mustRun(c.Name) {
				t.Errorf("%s: declined: %v", c.Name, err)
			}
			continue
		}
		if err != nil {
			got = "UNCAUGHT " + err.Error()
		}
		if got != c.Want {
			failed++
			if failed <= 40 {
				t.Errorf("%s:\n got: %s\nwant: %s\nscript: %s", c.Name, got, c.Want, c.Script)
			}
			continue
		}
		passed++
	}
	if testing.Verbose() {
		for _, d := range declinedNames {
			t.Log("declined", d)
		}
	}
	t.Logf("%s (Node %s, ICU %s): %d cases, %d identical, %d declined, %d different", path, g.Node, g.ICU, len(g.Cases), passed, declined, failed)
}

func runIntlScript(script string, loc *time.Location, now time.Time) (string, error) {
	p, err := Compile(script, CompileOptions{})
	if err != nil {
		return "", err
	}
	res, err := p.Execute(context.Background(), ExecuteOptions{Location: loc, Now: func() time.Time { return now }})
	if err != nil {
		var th *Throw
		if errors.As(err, &th) {
			if o, ok := th.Value.(*object); ok && o.errName != "" {
				return "", errors.New(errorToString(o))
			}
		}
		return "", err
	}
	if s, ok := res.Value.(string); ok {
		return s, nil
	}
	out, err := MarshalExport(res.Value)
	return string(out), err
}

func TestIntlDateTimeGolden(t *testing.T) {
	runIntlGolden(t, "testdata/intl/datetime.json", func(name string) bool {
		return strings.HasPrefix(name, "prod_") || strings.HasPrefix(name, "agent_") || strings.HasPrefix(name, "style_")
	})
}

// The engine has English data only: other locales resolve (as ECMA-402's
// lookup matcher specifies) to "en" or the default "en-US", and say so in
// resolvedOptions; settings whose output would differ are declined.
func TestIntlLocaleFallbackAndDeclines(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ script, want string }{
		{`const f = new Intl.DateTimeFormat("de-DE", {timeZone: "UTC", dateStyle: "long"}); return f.format(0) + " " + f.resolvedOptions().locale;`, "January 1, 1970 en-US"},
		{`return new Intl.DateTimeFormat("en-GB", {timeZone: "UTC"}).resolvedOptions().locale;`, "en"},
		{`return new Intl.DateTimeFormat(["de", "en-US-u-hc-h23"], {timeZone: "UTC", hour: "numeric"}).format(0);`, "00"},
		{`return new Date(0).toLocaleString();`, "01/01/1970, 00:00:00"}, // no arguments: Goja's layout
		{`return new Intl.DateTimeFormat().resolvedOptions().timeZone;`, "UTC"},
	} {
		got, err := runIntlScript(tc.script, utc, now)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.script, got, err, tc.want)
		}
	}
	for _, script := range []string{
		`return new Intl.DateTimeFormat("en-US", {calendar: "japanese"}).format(0);`,
		`return new Intl.DateTimeFormat("en-US", {numberingSystem: "arab"}).format(0);`,
		`return new Intl.DateTimeFormat("en-US-u-ca-buddhist").format(0);`,
		`return new Intl.DateTimeFormat("en-US").formatRange(0, 1);`,
	} {
		if _, err := runIntlScript(script, utc, now); !errors.Is(err, ErrRuntimeUnsupported) {
			t.Errorf("%s: want ErrRuntimeUnsupported, got %v", script, err)
		}
	}
	// A host zone without an IANA name cannot be reported.
	if _, err := runIntlScript(`return new Intl.DateTimeFormat().resolvedOptions().timeZone;`, time.FixedZone("X", 3600), now); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Errorf("fixed host zone: %v", err)
	}
	// Unknown Intl services are compile-time declines, so hosts fall back.
	if _, err := Compile(`return new Intl.Collator("en").compare("a", "b");`, CompileOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Intl.Collator: %v", err)
	}
}
