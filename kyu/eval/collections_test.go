package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestRange(t *testing.T) {
	cases := map[string]string{
		`range(3)`:        "[0, 1, 2]",
		`range(2, 5)`:     "[2, 3, 4]",
		`range(0, 10, 4)`: "[0, 4, 8]",
		`range(5, 0, -2)`: "[5, 3, 1]",
		`range(0)`:        "[]",
		`range(3, 3)`:     "[]",
		`range(5, 2)`:     "[]", // wrong direction for a positive step: empty, not an error
	}
	for src, want := range cases {
		if got := run(t, src).String(); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
	for _, bad := range []string{`range()`, `range(1, 2, 3, 4)`, `range(0, 5, 0)`, `range("a")`, `range(2000000)`} {
		runErr(t, bad)
	}
}

func TestZipPairsInPipeOrderAndTruncates(t *testing.T) {
	if got := run(t, `[1, 2, 3] | zip(["a", "b"])`).String(); got != `[[1, a], [2, b]]` {
		t.Errorf("zip = %s", got)
	}
	runErr(t, `[1] | zip(2)`)
	runErr(t, `[1] | zip()`)
}

func TestKeysAndValues(t *testing.T) {
	env := NewGlobalEnv(nil)
	runEnv(t, `r := {a: 1, b: "x"}`, env)
	if got := runEnv(t, `r | keys`, env).String(); got != `[a, b]` {
		t.Errorf("keys = %s", got)
	}
	if got := runEnv(t, `r | values`, env).String(); got != `[1, x]` {
		t.Errorf("values = %s", got)
	}
	runErr(t, `[1] | keys`)
	runErr(t, `[1] | values`)
}

func TestKeysOnLiveJobRecordDoesNotBlock(t *testing.T) {
	skipUnlessOnPath(t, "sleep")
	env, _ := jobsEnvWithManager(t)
	runEnv(t, `j := %sleep "30" &`, env)
	got := runEnv(t, `j | keys`, env).(*value.List)
	found := false
	for _, k := range got.Elems {
		if k == value.String("wait") {
			found = true
		}
	}
	if !found {
		t.Fatalf("keys of a job record = %v, expected to include wait", got)
	}
	runEnv(t, `write(/jobs/1/ctl, "kill")`, env)
}

func TestRegexBuiltins(t *testing.T) {
	cases := map[string]string{
		`"hello" | match("l+")`:                                "true",
		`"hello" | match("^l")`:                                "false",
		`"hello" | capture("(h)(e)(x)?")`:                      "[he, h, e, null]",
		`"hello" | capture("z")`:                               "null",
		`"a1b22" | replace_re("[0-9]+", "#")`:                  "a#b#",
		`"john smith" | replace_re("(\\w+) (\\w+)", "$2, $1")`: "smith, john",
	}
	for src, want := range cases {
		if got := run(t, src).String(); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
	for _, bad := range []string{`"x" | match("(")`, `"x" | match(1)`, `1 | match("a")`, `"x" | capture()`, `"x" | replace_re("a")`} {
		runErr(t, bad)
	}
}

func TestRegexComposesWithPipelines(t *testing.T) {
	got := run(t, `["a1", "b", "c3"] | where { |s| s | match("[0-9]") } | count`).String()
	if got != "2" {
		t.Errorf("filtered count = %s, want 2", got)
	}
}
