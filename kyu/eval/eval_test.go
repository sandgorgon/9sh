package eval

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9p/examples/dirfs"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func run(t *testing.T, src string) value.Value {
	t.Helper()
	return runEnv(t, src, NewGlobalEnv(nil))
}

func runErr(t *testing.T, src string) error {
	t.Helper()
	return runEnvErr(t, src, NewGlobalEnv(nil))
}

// jobsEnv is a global env with /jobs bound, the same bootstrap
// cmd/9sh's main does — for tests exercising bind/background/live fields.
func jobsEnv(t *testing.T) *Env {
	t.Helper()
	namespace := ns.New()
	if err := namespace.BindFS(job.New(job.NewManager()), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrap bind /jobs: %v", err)
	}
	return NewGlobalEnv(namespace)
}

// jobsAndEnvVarsEnv is jobsEnv plus /env bound over a fresh scratch
// directory (dirfs, the same mechanism cmd/9sh's bootstrap uses for
// /env — see main.go) — for tests exercising getenv/setenv/unsetenv and
// their propagation into %cmd/%cmd &/$cmd. Deliberately starts empty
// (unlike bootstrap, which seeds it from os.Environ()): tests want a
// known, controlled set of variables, not whatever happens to be in the
// process running `go test`.
func jobsAndEnvVarsEnv(t *testing.T) *Env {
	t.Helper()
	env := jobsEnv(t)
	fs, err := dirfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	if err := env.Namespace().BindFS(fs, "", "/env", ns.Replace); err != nil {
		t.Fatalf("bind /env: %v", err)
	}
	return env
}

func runEnv(t *testing.T, src string, env *Env) value.Value {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	v, err := Eval(prog, env)
	if err != nil {
		t.Fatalf("src %q: eval error: %v", src, err)
	}
	return v
}

func runEnvErr(t *testing.T, src string, env *Env) error {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	_, err := Eval(prog, env)
	if err == nil {
		t.Fatalf("src %q: expected an eval error, got none", src)
	}
	return err
}

func TestArithmetic(t *testing.T) {
	if v := run(t, `1 + 2 * 3`); v.(value.Int) != 7 {
		t.Errorf("got %v, want 7", v)
	}
	if v := run(t, `(1 + 2) * 3`); v.(value.Int) != 9 {
		t.Errorf("got %v, want 9", v)
	}
	if v := run(t, `7 / 2`); v.(value.Int) != 3 {
		t.Errorf("got %v, want 3", v)
	}
	if v := run(t, `7.0 / 2`); v.(value.Float) != 3.5 {
		t.Errorf("got %v, want 3.5", v)
	}
	if v := run(t, `-5 + 2`); v.(value.Int) != -3 {
		t.Errorf("got %v, want -3", v)
	}
	if v := run(t, `"hello, " + "world"`); v.(value.String) != "hello, world" {
		t.Errorf("got %v, want %q", v, "hello, world")
	}
	if v := run(t, `"" + ""`); v.(value.String) != "" {
		t.Errorf("got %v, want empty string", v)
	}
}

func TestStringPlusNonStringStillErrors(t *testing.T) {
	// + concatenates String+String only — no implicit stringification
	// of other kinds, matching kyu's general preference for explicit
	// conversions over silently guessing what a mixed-type + should do.
	runErr(t, `"n = " + 5`)
}

func TestComparisonAndLogic(t *testing.T) {
	if v := run(t, `1 < 2 && 2 < 3`); v != value.Bool(true) {
		t.Errorf("got %v, want true", v)
	}
	if v := run(t, `1 > 2 || 3 == 3`); v != value.Bool(true) {
		t.Errorf("got %v, want true", v)
	}
	if v := run(t, `!(1 == 2)`); v != value.Bool(true) {
		t.Errorf("got %v, want true", v)
	}
}

func TestDefineAndAssign(t *testing.T) {
	if v := run(t, "x := 5\nx = x + 1\nx"); v.(value.Int) != 6 {
		t.Errorf("got %v, want 6", v)
	}
}

func TestAssignUndefinedErrors(t *testing.T) {
	runErr(t, `x = 5`)
}

func TestFieldAccessAndAssign(t *testing.T) {
	v := run(t, `j := { status: "pending" }
j.status = "running"
j.status`)
	if v.(value.String) != "running" {
		t.Errorf("got %v, want running", v)
	}
}

func TestIfElse(t *testing.T) {
	if v := run(t, `if 1 < 2 { "yes" } else { "no" }`); v.(value.String) != "yes" {
		t.Errorf("got %v, want yes", v)
	}
	if v := run(t, `if 1 > 2 { "yes" } else { "no" }`); v.(value.String) != "no" {
		t.Errorf("got %v, want no", v)
	}
}

func TestWhileLoop(t *testing.T) {
	v := run(t, `i := 0
sum := 0
while i < 5 {
  sum = sum + i
  i = i + 1
}
sum`)
	if v.(value.Int) != 10 {
		t.Errorf("got %v, want 10 (0+1+2+3+4)", v)
	}
}

func TestWhileBreak(t *testing.T) {
	v := run(t, `i := 0
while i < 100 {
  if i == 3 { break }
  i = i + 1
}
i`)
	if v.(value.Int) != 3 {
		t.Errorf("got %v, want 3", v)
	}
}

func TestWhileContinue(t *testing.T) {
	v := run(t, `i := 0
sum := 0
while i < 5 {
  i = i + 1
  if i == 3 { continue }
  sum = sum + i
}
sum`)
	if v.(value.Int) != 12 {
		t.Errorf("got %v, want 12 (1+2+4+5, 3 skipped)", v)
	}
}

func TestNestedWhileBreakOnlyExitsInner(t *testing.T) {
	v := run(t, `outer := 0
inner_total := 0
while outer < 3 {
  j := 0
  while true {
    if j == 2 { break }
    inner_total = inner_total + 1
    j = j + 1
  }
  outer = outer + 1
}
inner_total`)
	if v.(value.Int) != 6 {
		t.Errorf("got %v, want 6 (3 outer iterations x 2 inner each)", v)
	}
}

func TestClosureCall(t *testing.T) {
	if v := run(t, `add := { |a, b| a + b }
add(2, 3)`); v.(value.Int) != 5 {
		t.Errorf("got %v, want 5", v)
	}
}

func TestClosureDefaultParamUsedWhenOmitted(t *testing.T) {
	if v := run(t, `add := { |a, b = 10| a + b }
add(2)`); v.(value.Int) != 12 {
		t.Errorf("got %v, want 12 (2 + default 10)", v)
	}
}

func TestClosureDefaultParamOverriddenWhenSupplied(t *testing.T) {
	if v := run(t, `add := { |a, b = 10| a + b }
add(2, 5)`); v.(value.Int) != 7 {
		t.Errorf("got %v, want 7 (2 + explicit 5)", v)
	}
}

func TestClosureDefaultParamCanReferenceEarlierParam(t *testing.T) {
	if v := run(t, `f := { |a, b = a| a + b }
f(4)`); v.(value.Int) != 8 {
		t.Errorf("got %v, want 8 (4 + default b=a=4)", v)
	}
}

func TestClosureTooFewArgsWithoutDefaultsErrors(t *testing.T) {
	runErr(t, `f := { |a, b = 10| a + b }
f()`)
}

func TestClosureRecursionStillWorksWithDefaults(t *testing.T) {
	v := run(t, `fact := { |n, acc = 1| if n <= 1 { acc } else { fact(n - 1, n * acc) } }
fact(5)`)
	if v.(value.Int) != 120 {
		t.Errorf("got %v, want 120", v)
	}
}

func TestFormat(t *testing.T) {
	if v := run(t, `format("hello {}, you are {}", "world", 42)`); v.(value.String) != "hello world, you are 42" {
		t.Errorf("got %v, want %q", v, "hello world, you are 42")
	}
	if v := run(t, `"world" | format("hello {}")`); v.(value.String) != "hello world" {
		t.Errorf("piped got %v, want %q", v, "hello world")
	}
}

func TestFormatPlaceholderCountMismatchErrors(t *testing.T) {
	runErr(t, `format("hello {}", "a", "b")`)
	runErr(t, `format("hello {} {}", "a")`)
}

func TestExitCodeNilBeforeAnyForegroundCommand(t *testing.T) {
	if v := run(t, `exit_code()`); v.Kind() != "null" {
		t.Errorf("got %v, want null", v)
	}
}

func TestExitCodeAfterForegroundExternalCall(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "exit 3"
exit_code()`, env)
	if v.(value.Int) != 3 {
		t.Errorf("got %v, want 3", v)
	}
}

func TestExitCodeAfterDirectExecFallback(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := NewGlobalEnv(nil) // no namespace: runExternalDirect path
	v := runEnv(t, `%sh "-c" "exit 7"
exit_code()`, env)
	if v.(value.Int) != 7 {
		t.Errorf("got %v, want 7", v)
	}
}

func TestExitCodeAfterPassthrough(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	runEnv(t, `$sh "-c" "exit 5"`, env)
	v := runEnv(t, `exit_code()`, env)
	if v.(value.Int) != 5 {
		t.Errorf("got %v, want 5", v)
	}
}

func TestLogicalAndRunsRightOnlyIfLeftCommandSucceeded(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	// left succeeds (exit 0) -> right runs, its own exit code (0) is
	// the chain's result
	if v := runEnv(t, `%sh "-c" "exit 0" && %sh "-c" "exit 0"`, env); v.(value.Bool) != true {
		t.Errorf("got %v, want true (both succeeded)", v)
	}
	// left fails -> right must NOT run at all; use a marker file to
	// prove it, not just the chain's own boolean result
	dir := t.TempDir()
	marker := dir + "/ran"
	runEnv(t, `%sh "-c" "exit 1" && %sh "-c" "touch `+marker+`"`, env)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("right side ran even though the left command failed")
	}
}

func TestLogicalAndFailsIfRightCommandFails(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "exit 0" && %sh "-c" "exit 1"`, env)
	if v.(value.Bool) != false {
		t.Errorf("got %v, want false -- the chain's own final command failed", v)
	}
}

func TestLogicalOrRunsRightOnlyIfLeftCommandFailed(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	if v := runEnv(t, `%sh "-c" "exit 0" || %sh "-c" "exit 1"`, env); v.(value.Bool) != true {
		t.Errorf("got %v, want true (left already succeeded, right never ran)", v)
	}
	if v := runEnv(t, `%sh "-c" "exit 1" || %sh "-c" "exit 0"`, env); v.(value.Bool) != true {
		t.Errorf("got %v, want true (left failed, right ran and succeeded)", v)
	}
	if v := runEnv(t, `%sh "-c" "exit 1" || %sh "-c" "exit 1"`, env); v.(value.Bool) != false {
		t.Errorf("got %v, want false (both failed)", v)
	}
}

func TestLogicalChainOfThreeCommands(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "exit 0" && %sh "-c" "exit 0" && %sh "-c" "exit 1"`, env)
	if v.(value.Bool) != false {
		t.Errorf("got %v, want false -- third command in the chain failed", v)
	}
}

func TestLogicalAndBadCommandCountsAsFailure(t *testing.T) {
	env := jobsEnv(t)
	dir := t.TempDir()
	marker := dir + "/ran"
	runEnv(t, `%this-command-does-not-exist-9sh && %sh "-c" "touch `+marker+`"`, env)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("right side ran even though the left command failed to start")
	}
}

// TestLogicalAndStoredCommandResultUsesPlainTruthiness locks in the
// "storing a %cmd result first is an explicit opt-out" design: once a
// %cmd's result is captured in a variable, && / || see it as an
// ordinary (always-truthy-as-Bytes) value, not exit-status-aware --
// only a bare %cmd written directly as an operand gets that treatment.
func TestLogicalAndStoredCommandResultUsesPlainTruthiness(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	v := runEnv(t, `x := %sh "-c" "exit 1"
x && true`, env)
	if v.(value.Bool) != true {
		t.Errorf("got %v, want true -- x is just Bytes (always truthy), exit code not consulted once stored", v)
	}
}

func TestLogicalAndPlainValuesUnaffected(t *testing.T) {
	if v := run(t, `1 < 2 && 2 < 3`); v.(value.Bool) != true {
		t.Errorf("got %v, want true", v)
	}
	if v := run(t, `false && true`); v.(value.Bool) != false {
		t.Errorf("got %v, want false", v)
	}
}

func TestWherePipeline(t *testing.T) {
	src := `jobs := [{name: "a", ok: true}, {name: "b", ok: false}, {name: "c", ok: true}]
jobs | where { |j| j.ok } | count()`
	if v := run(t, src); v.(value.Int) != 2 {
		t.Errorf("got %v, want 2", v)
	}
}

func TestSelectPipeline(t *testing.T) {
	src := `t := [{a: 1, b: 2}, {a: 3, b: 4}]
t | select("a")`
	v := run(t, src)
	lst := v.(*value.List)
	if len(lst.Elems) != 2 {
		t.Fatalf("want 2 rows, got %d", len(lst.Elems))
	}
	rec := lst.Elems[0].(*value.Record)
	if len(rec.Keys()) != 1 || rec.Keys()[0] != "a" {
		t.Errorf("want only field a, got %v", rec.Keys())
	}
}

func TestSortByFieldName(t *testing.T) {
	src := `t := [{n: 3}, {n: 1}, {n: 2}]
t | sort_by("n")`
	v := run(t, src)
	lst := v.(*value.List)
	want := []int64{1, 2, 3}
	for i, w := range want {
		rec := lst.Elems[i].(*value.Record)
		n, _ := rec.Get("n")
		if int64(n.(value.Int)) != w {
			t.Errorf("index %d: got %v, want %d", i, n, w)
		}
	}
}

func TestSortByClosure(t *testing.T) {
	src := `t := [{n: 3}, {n: 1}, {n: 2}]
t | sort_by({ |r| 0 - r.n })`
	v := run(t, src)
	lst := v.(*value.List)
	n, _ := lst.Elems[0].(*value.Record).Get("n")
	if int64(n.(value.Int)) != 3 {
		t.Errorf("want first=3 (descending via negated key), got %v", n)
	}
}

func TestGroupBy(t *testing.T) {
	src := `t := [{k: "a", v: 1}, {k: "b", v: 2}, {k: "a", v: 3}]
t | group_by("k")`
	v := run(t, src)
	lst := v.(*value.List)
	if len(lst.Elems) != 2 {
		t.Fatalf("want 2 groups, got %d", len(lst.Elems))
	}
	g0 := lst.Elems[0].(*value.Record)
	key, _ := g0.Get("key")
	if key.(value.String) != "a" {
		t.Errorf("want first group key 'a', got %v", key)
	}
	items, _ := g0.Get("items")
	if len(items.(*value.List).Elems) != 2 {
		t.Errorf("want group 'a' to have 2 items, got %d", len(items.(*value.List).Elems))
	}
}

func TestEachMutatesRecordsInPlace(t *testing.T) {
	src := `jobs := [{status: "pending"}, {status: "pending"}]
jobs | each { |j| j.status = "done" }
jobs | where { |j| j.status == "done" } | count()`
	if v := run(t, src); v.(value.Int) != 2 {
		t.Errorf("got %v, want 2 (records are reference types, each's mutation is visible via the original binding)", v)
	}
}

func TestTakeAndFirst(t *testing.T) {
	src := `t := [1, 2, 3, 4, 5]
t | take(2)`
	v := run(t, src)
	if len(v.(*value.List).Elems) != 2 {
		t.Errorf("want 2 elements, got %d", len(v.(*value.List).Elems))
	}
	if v := run(t, `[10, 20] | first`); v.(value.Int) != 10 {
		t.Errorf("got %v, want 10", v)
	}
	if v := run(t, `[] | first`); v.Kind() != "null" {
		t.Errorf("got %v, want null", v)
	}
}

func TestLast(t *testing.T) {
	if v := run(t, `[10, 20, 30] | last`); v.(value.Int) != 30 {
		t.Errorf("got %v, want 30", v)
	}
	if v := run(t, `[] | last`); v.Kind() != "null" {
		t.Errorf("got %v, want null", v)
	}
}

func TestSkip(t *testing.T) {
	v := run(t, `[1, 2, 3, 4, 5] | skip(2)`)
	elems := v.(*value.List).Elems
	if len(elems) != 3 || elems[0].(value.Int) != 3 {
		t.Errorf("got %v, want [3, 4, 5]", v)
	}
}

func TestReverse(t *testing.T) {
	v := run(t, `[1, 2, 3] | reverse`)
	elems := v.(*value.List).Elems
	if len(elems) != 3 || elems[0].(value.Int) != 3 || elems[2].(value.Int) != 1 {
		t.Errorf("got %v, want [3, 2, 1]", v)
	}
}

func TestUniq(t *testing.T) {
	v := run(t, `[1, 2, 2, 3, 1, 1] | uniq`)
	elems := v.(*value.List).Elems
	if len(elems) != 3 {
		t.Fatalf("got %v, want 3 unique elements", v)
	}
	want := []int64{1, 2, 3}
	for i, w := range want {
		if int64(elems[i].(value.Int)) != w {
			t.Errorf("elem %d = %v, want %d (first-occurrence order)", i, elems[i], w)
		}
	}
}

func TestFlatten(t *testing.T) {
	v := run(t, `[[1, 2], 3, [4, [5, 6]]] | flatten`)
	elems := v.(*value.List).Elems
	// one level only: [1,2] splices to 1,2; 3 passes through; [4,[5,6]]
	// splices to 4,[5,6] -- the nested [5,6] stays a List, not further
	// flattened. 5 top-level elements: 1, 2, 3, 4, [5,6].
	if len(elems) != 5 {
		t.Fatalf("got %d elements, want 5: %v", len(elems), v)
	}
	if _, ok := elems[4].(*value.List); !ok {
		t.Errorf("last element should still be a nested List, got %T", elems[4])
	}
}

func TestJoin(t *testing.T) {
	if v := run(t, `["a", "b", "c"] | join(",")`); v.(value.String) != "a,b,c" {
		t.Errorf("got %v, want a,b,c", v)
	}
	// join uses .String() on any element kind, not just strings
	if v := run(t, `[1, 2, 3] | join("-")`); v.(value.String) != "1-2-3" {
		t.Errorf("got %v, want 1-2-3", v)
	}
}

func TestSumMinMaxAvg(t *testing.T) {
	if v := run(t, `[1, 2, 3, 4] | sum`); v.(value.Int) != 10 {
		t.Errorf("sum got %v, want 10", v)
	}
	if v := run(t, `[3, 1, 4, 1, 5] | min`); v.(value.Int) != 1 {
		t.Errorf("min got %v, want 1", v)
	}
	if v := run(t, `[3, 1, 4, 1, 5] | max`); v.(value.Int) != 5 {
		t.Errorf("max got %v, want 5", v)
	}
	if v := run(t, `[1, 2] | avg`); v.(value.Float) != 1.5 {
		t.Errorf("avg got %v, want 1.5 (not integer-divided)", v)
	}
	runErr(t, `[] | avg`)
	if v := run(t, `[] | min`); v.Kind() != "null" {
		t.Errorf("min of empty = %v, want null", v)
	}
}

func TestAnyAll(t *testing.T) {
	if v := run(t, `[1, 2, 3] | any({ |x| x > 2 })`); v.(value.Bool) != true {
		t.Errorf("any got %v, want true", v)
	}
	if v := run(t, `[1, 2, 3] | any({ |x| x > 5 })`); v.(value.Bool) != false {
		t.Errorf("any got %v, want false", v)
	}
	if v := run(t, `[1, 2, 3] | all({ |x| x > 0 })`); v.(value.Bool) != true {
		t.Errorf("all got %v, want true", v)
	}
	if v := run(t, `[1, 2, 3] | all({ |x| x > 1 })`); v.(value.Bool) != false {
		t.Errorf("all got %v, want false", v)
	}
	// vacuously true for an empty list
	if v := run(t, `[] | all({ |x| x > 0 })`); v.(value.Bool) != true {
		t.Errorf("all of empty = %v, want true", v)
	}
}

func TestToJSON(t *testing.T) {
	v := run(t, `{name: "a", count: 3, ok: true} | to_json`)
	s := string(v.(value.String))
	if !strings.Contains(s, `"name":"a"`) || !strings.Contains(s, `"count":3`) {
		t.Fatalf("to_json output = %q, missing expected fields", s)
	}
}

func TestFromJSON(t *testing.T) {
	v := run(t, `"{\"name\": \"a\", \"count\": 3, \"ok\": true}" | from_json`)
	rec := v.(*value.Record)
	if v, _ := rec.Get("name"); v.(value.String) != "a" {
		t.Errorf("name = %v, want a", v)
	}
	if v, _ := rec.Get("count"); v.(value.Int) != 3 {
		t.Errorf("count = %v, want 3", v)
	}
	if v, _ := rec.Get("ok"); v.(value.Bool) != true {
		t.Errorf("ok = %v, want true", v)
	}
}

func TestFromJSONBadInputIsErrorVal(t *testing.T) {
	v := run(t, `"not json" | from_json`)
	// invalid JSON's raw decode target is `any`, and "not json" isn't
	// valid JSON at all, so json.Unmarshal itself fails -> ErrorVal.
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal, got %#v", v)
	}
}

func TestSplitTrimReplaceContains(t *testing.T) {
	v := run(t, `"a,b,c" | split(",")`)
	elems := v.(*value.List).Elems
	if len(elems) != 3 || elems[1].(value.String) != "b" {
		t.Errorf("split got %v, want [a b c]", v)
	}
	if v := run(t, `"  hi  " | trim`); v.(value.String) != "hi" {
		t.Errorf("trim got %q, want %q", v, "hi")
	}
	if v := run(t, `"hello world" | replace("world", "there")`); v.(value.String) != "hello there" {
		t.Errorf("replace got %q, want %q", v, "hello there")
	}
	if v := run(t, `"hello world" | contains("wor")`); v.(value.Bool) != true {
		t.Errorf("contains got %v, want true", v)
	}
	if v := run(t, `"hello world" | contains("xyz")`); v.(value.Bool) != false {
		t.Errorf("contains got %v, want false", v)
	}
}

func TestErrCheckAborts(t *testing.T) {
	err := runErr(t, `error("boom")?`)
	if err.Error() != "boom" {
		t.Errorf("got %v, want boom", err)
	}
}

func TestErrorValueIsFalsyInWhere(t *testing.T) {
	src := `t := [1, 2, 3]
t | where { |x| if x == 2 { error("nope") } else { true } }`
	v := run(t, src)
	lst := v.(*value.List)
	if len(lst.Elems) != 2 {
		t.Fatalf("want 2 (row 2 excluded via falsy ErrorVal, no abort), got %d: %v", len(lst.Elems), lst)
	}
}

func TestExternalCallCat(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not on PATH")
	}
	v := run(t, `"hello" | %cat`)
	b := v.(value.Bytes)
	if string(b) != "hello\n" {
		t.Errorf("got %q, want %q", string(b), "hello\n")
	}
}

func TestExternalCallBadCommand(t *testing.T) {
	v := run(t, `%this-command-does-not-exist-9sh`)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal for a missing command, got %#v", v)
	}
}

// TestPassthroughInheritsRealStdio locks in $cmd's whole reason for
// existing: unlike %cmd (always job-tracked, output buffered into a
// growBuf and only returned after the job finishes — see
// runExternalViaJob), $cmd connects the subprocess directly to 9sh's
// own stdout/stderr, with no job created at all.
func TestPassthroughInheritsRealStdio(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env, mgr := jobsEnvWithManager(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	v := runEnv(t, `$echo "hello-passthrough"`, env)
	os.Stdout = origStdout
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if string(captured) != "hello-passthrough\n" {
		t.Fatalf("stdout = %q, want %q", captured, "hello-passthrough\n")
	}
	if _, ok := v.(value.Null); !ok {
		t.Fatalf("want value.Null (nothing to capture), got %#v", v)
	}
	if len(mgr.List()) != 0 {
		t.Fatalf("$cmd must not create a job, got %d", len(mgr.List()))
	}
}

// TestPassthroughBlockedByEnv locks in cmd/9sh's TUI guard: SetPassthroughBlocked
// makes $cmd fail with an ErrorVal instead of touching os.Stdin/Stdout/
// Stderr at all -- see Env.SetPassthroughBlocked's doc comment for why
// the TUI needs this (a real subprocess sharing the terminal would race
// tui.App.Run's own raw-mode stdin reader).
func TestPassthroughBlockedByEnv(t *testing.T) {
	env := jobsEnv(t)
	env.SetPassthroughBlocked("not supported here")
	v := runEnv(t, `$echo "should not run"`, env)
	ev, ok := v.(value.ErrorVal)
	if !ok {
		t.Fatalf("want ErrorVal, got %#v", v)
	}
	if !strings.Contains(ev.Msg, "not supported here") {
		t.Fatalf("error = %q, want it to mention the blocked reason", ev.Msg)
	}
}

func TestPassthroughBadCommandIsErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `$this-command-does-not-exist-9sh`, env)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal for a missing command, got %#v", v)
	}
}

func skipUnlessOnPath(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

func TestKyuNamespaceUnionExpr(t *testing.T) {
	v := run(t, `a := /local/bin
b := /n/host/bin
a + b`)
	u, ok := v.(value.NSUnion)
	if !ok {
		t.Fatalf("want NSUnion, got %T", v)
	}
	if len(u.Paths) != 2 || u.Paths[0] != "/local/bin" || u.Paths[1] != "/n/host/bin" {
		t.Fatalf("got %v", u)
	}
}

func TestBackgroundWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `%true &`)
}

func TestBindWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `bind /a, /b`)
}

func TestKyuBindGraftsExistingNamespacePath(t *testing.T) {
	env := jobsEnv(t)
	runEnv(t, `bind /jobs, /j2`, env)

	namespace := env.Namespace()
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "u", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := walkAll(ctx, root, []string{"j2", "clone"}); err != nil {
		t.Fatalf("walk /j2/clone after bind: %v", err)
	}
}

func TestUnbindWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `unbind /a`)
}

func TestUnbindNothingBoundIsError(t *testing.T) {
	env := jobsEnv(t)
	runEnvErr(t, `unbind /nothing-here`, env)
}

func TestUnbindRemovesWhatWasBound(t *testing.T) {
	env := jobsEnv(t)
	runEnv(t, `bind /jobs, /j2`, env)

	namespace := env.Namespace()
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "u", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := walkAll(ctx, root, []string{"j2", "clone"}); err != nil {
		t.Fatalf("walk /j2/clone after bind: %v", err)
	}

	runEnv(t, `unbind /j2`, env)
	if _, err := walkAll(ctx, root, []string{"j2", "clone"}); err == nil {
		t.Fatal("expected /j2/clone to be unreachable after unbind, but walk succeeded")
	}
	// unbinding again (nothing left there now) is an error, not a no-op
	runEnvErr(t, `unbind /j2`, env)
}

func TestBackgroundJobLifecycle(t *testing.T) {
	skipUnlessOnPath(t, "true")
	env := jobsEnv(t)
	v := runEnv(t, `j := %true &
j | wait
j`, env)
	rec, ok := v.(*value.Record)
	if !ok {
		t.Fatalf("want a job record, got %T", v)
	}
	status, ok := rec.Get("status")
	if !ok {
		t.Fatal("job record missing status field")
	}
	st, ok := status.(*value.Record)
	if !ok {
		t.Fatalf("want status to decode as a record, got %#v", status)
	}
	state, _ := st.Get("state")
	if state.(value.String) != "done" {
		t.Fatalf("state = %v, want done", state)
	}
}

func TestBackgroundJobKillViaCtlField(t *testing.T) {
	skipUnlessOnPath(t, "sleep")
	env := jobsEnv(t)
	// "start" returns only once the process has actually been exec'd (or
	// failed to start) — see job.go's start(), which calls cmd.Start()
	// synchronously — so there's no race needing a retry/sleep here.
	runEnv(t, `j := %sleep 5 &`, env)
	runEnv(t, `j.ctl = "kill"`, env)
	v := runEnv(t, `j | wait
j.status`, env)
	st := v.(*value.Record)
	state, _ := st.Get("state")
	if state.(value.String) != "killed" {
		t.Fatalf("state = %v, want killed", state)
	}
}

func TestJobCtlFieldRejectsNonString(t *testing.T) {
	skipUnlessOnPath(t, "true")
	env := jobsEnv(t)
	runEnv(t, `j := %true &`, env)
	runEnvErr(t, `j.ctl = 5`, env)
}

func TestJobStatusFieldIsReadOnly(t *testing.T) {
	skipUnlessOnPath(t, "true")
	env := jobsEnv(t)
	runEnv(t, `j := %true &`, env)
	runEnvErr(t, `j.status = "done"`, env)
}

// jobsEnvWithManager is jobsEnv but also returns the *job.Manager, for
// tests that need to observe job creation directly (e.g. via List())
// rather than only through kyu-visible effects.
func jobsEnvWithManager(t *testing.T) (*Env, *job.Manager) {
	t.Helper()
	mgr := job.NewManager()
	namespace := ns.New()
	if err := namespace.BindFS(job.New(mgr), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrap bind /jobs: %v", err)
	}
	return NewGlobalEnv(namespace), mgr
}

func TestForegroundExternalCallRoutesThroughJobsWhenNamespacePresent(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env, mgr := jobsEnvWithManager(t)
	if len(mgr.List()) != 0 {
		t.Fatalf("expected 0 jobs before any %%cmd, got %d", len(mgr.List()))
	}
	v := runEnv(t, `%echo "hello job-routed"`, env)
	if string(v.(value.Bytes)) != "hello job-routed\n" {
		t.Fatalf("stdout = %q, want %q", v, "hello job-routed\n")
	}
	if len(mgr.List()) != 1 {
		t.Fatalf("expected 1 job after a foreground %%cmd, got %d", len(mgr.List()))
	}
	st := mgr.List()[0].Status()
	if st.State != job.StateDone {
		t.Fatalf("job state = %v, want done", st.State)
	}
}

// TestForegroundExternalCallInheritsProcessEnv locks in the os/exec
// default this repo's cmd/9sh relies on for -listen-unix's _9SH_UNIX_SOCK
// export (see cmd/9sh/main.go's bootstrap): a job whose kyu script never
// calls SetEnv leaves Cmd.Env nil, which os/exec defines as "inherit the
// current process's environment" -- so anything 9sh itself has in its own
// environment (a namespace socket path, PATH, etc.) reaches every %cmd
// job for free, with no per-job wiring.
func TestForegroundExternalCallInheritsProcessEnv(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	t.Setenv("NINESH_TEST_INHERITED_VAR", "inherited-value")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "echo -n $NINESH_TEST_INHERITED_VAR"`, env)
	if string(v.(value.Bytes)) != "inherited-value" {
		t.Fatalf("stdout = %q, want %q", v, "inherited-value")
	}
}

func TestForegroundExternalCallStdinRoutedThroughJob(t *testing.T) {
	skipUnlessOnPath(t, "cat")
	env := jobsEnv(t)
	v := runEnv(t, `"piped in" | %cat`, env)
	if string(v.(value.Bytes)) != "piped in\n" {
		t.Fatalf("stdout = %q, want %q", v, "piped in\n")
	}
}

func TestForegroundExternalCallBadCommandStillErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := run(t, `%this-command-does-not-exist-9sh`) // no namespace: direct-exec fallback path
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("direct-exec path: want ErrorVal, got %#v", v)
	}
	v2 := runEnv(t, `%this-command-does-not-exist-9sh`, env) // job-routed path
	if _, ok := v2.(value.ErrorVal); !ok {
		t.Fatalf("job-routed path: want ErrorVal, got %#v", v2)
	}
}

// TestForegroundExternalCallForwardsStderr locks in the fix for stderr
// silently vanishing on a bare foreground %cmd: job.go captures a
// subprocess's stderr into its own growBuf (same as stdout), so unlike
// the no-namespace direct-exec fallback (which wires cmd.Stderr =
// os.Stderr live), the job-routed path has to explicitly read it back
// and forward it after the job finishes -- see runExternalViaJob.
func TestCdBadPathIsErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `cd("/this/path/does/not/exist/anywhere")`, env)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal for a nonexistent path, got %#v", v)
	}
	if env.Cwd() != "" {
		t.Fatalf("Cwd should be unchanged after a failed cd, got %q", env.Cwd())
	}
}

func TestCdAffectsForegroundExternalCall(t *testing.T) {
	skipUnlessOnPath(t, "pwd")
	dir := t.TempDir()
	env := jobsEnv(t)
	runEnv(t, `cd("`+dir+`")`, env)
	v := runEnv(t, `%pwd`, env)
	if string(v.(value.Bytes)) != dir+"\n" {
		t.Fatalf("pwd output = %q, want %q", v, dir+"\n")
	}
}

func TestCdAffectsDirectExecFallback(t *testing.T) {
	skipUnlessOnPath(t, "pwd")
	dir := t.TempDir()
	env := NewGlobalEnv(nil) // no namespace: %pwd goes through runExternalDirect
	runEnv(t, `cd("`+dir+`")`, env)
	v := runEnv(t, `%pwd`, env)
	if string(v.(value.Bytes)) != dir+"\n" {
		t.Fatalf("pwd output = %q, want %q", v, dir+"\n")
	}
}

func TestCdAffectsPassthrough(t *testing.T) {
	skipUnlessOnPath(t, "pwd")
	dir := t.TempDir()
	env := jobsEnv(t)
	runEnv(t, `cd("`+dir+`")`, env)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	runEnv(t, `$pwd`, env)
	os.Stdout = origStdout
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if string(captured) != dir+"\n" {
		t.Fatalf("pwd output = %q, want %q", captured, dir+"\n")
	}
}

func TestCdAffectsBackgroundJob(t *testing.T) {
	skipUnlessOnPath(t, "pwd")
	dir := t.TempDir()
	env := jobsEnv(t)
	runEnv(t, `cd("`+dir+`")`, env)
	v := runEnv(t, `j := %pwd &
j | wait
j.stdout`, env)
	if string(v.(value.Bytes)) != dir+"\n" {
		t.Fatalf("pwd output = %q, want %q", v, dir+"\n")
	}
}

func TestPwdFallsBackToOsGetwdBeforeCd(t *testing.T) {
	env := NewGlobalEnv(nil)
	wantDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	v := runEnv(t, `pwd()`, env)
	if string(v.(value.String)) != wantDir {
		t.Fatalf("pwd() = %q, want %q (os.Getwd fallback)", v, wantDir)
	}
}

func TestPwdReflectsCd(t *testing.T) {
	dir := t.TempDir()
	env := NewGlobalEnv(nil)
	runEnv(t, `cd("`+dir+`")`, env)
	v := runEnv(t, `pwd()`, env)
	if string(v.(value.String)) != dir {
		t.Fatalf("pwd() = %q, want %q", v, dir)
	}
}

// TestCdRelativePathResolvesAgainstCwd locks in the fix for a relative
// cd(...) argument being os.Stat-ed (and stored) unresolved -- it used
// to check the real OS process's own working directory instead of
// kyu's virtual cwd, and on success stored the bare relative string, so
// a later pwd() reported a fragment like "sub" instead of a real path.
func TestCdRelativePathResolvesAgainstCwd(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewGlobalEnv(nil)
	runEnv(t, `cd("`+base+`")`, env)
	runEnv(t, `cd("sub")`, env)
	v := runEnv(t, `pwd()`, env)
	if string(v.(value.String)) != sub {
		t.Fatalf("pwd() = %q, want %q", v, sub)
	}
}

// TestCdRelativePathResolvesAgainstOsGetwdBeforeAnyCd covers the same
// resolution when cd() has never been called yet, so effectiveCwd falls
// back to the real process's os.Getwd() as the base.
func TestCdRelativePathResolvesAgainstOsGetwdBeforeAnyCd(t *testing.T) {
	start, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(start, "testdata_cd_relative")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(sub) })
	env := NewGlobalEnv(nil)
	runEnv(t, `cd("testdata_cd_relative")`, env)
	v := runEnv(t, `pwd()`, env)
	if string(v.(value.String)) != sub {
		t.Fatalf("pwd() = %q, want %q", v, sub)
	}
}

func TestGetenvSetenvUnsetenvRoundTrip(t *testing.T) {
	env := jobsAndEnvVarsEnv(t)
	if v := runEnv(t, `getenv("NINESH_TEST_VAR")`, env); v.(value.String) != "" {
		t.Fatalf("unset getenv = %q, want empty string", v)
	}
	runEnv(t, `setenv("NINESH_TEST_VAR", "hello")`, env)
	if v := runEnv(t, `getenv("NINESH_TEST_VAR")`, env); v.(value.String) != "hello" {
		t.Fatalf("getenv after setenv = %q, want %q", v, "hello")
	}
	runEnv(t, `unsetenv("NINESH_TEST_VAR")`, env)
	if v := runEnv(t, `getenv("NINESH_TEST_VAR")`, env); v.(value.String) != "" {
		t.Fatalf("getenv after unsetenv = %q, want empty string", v)
	}
}

func TestSetenvWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `setenv("X", "y")`)
}

func TestSetenvAffectsForegroundExternalCall(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsAndEnvVarsEnv(t)
	runEnv(t, `setenv("NINESH_TEST_VAR", "from-setenv")`, env)
	v := runEnv(t, `%sh "-c" "echo -n $NINESH_TEST_VAR"`, env)
	if string(v.(value.Bytes)) != "from-setenv" {
		t.Fatalf("stdout = %q, want %q", v, "from-setenv")
	}
}

func TestSetenvAffectsBackgroundJob(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsAndEnvVarsEnv(t)
	runEnv(t, `setenv("NINESH_TEST_VAR", "from-setenv-bg")`, env)
	v := runEnv(t, `j := %sh "-c" "echo -n $NINESH_TEST_VAR" &
j | wait
j.stdout`, env)
	if string(v.(value.Bytes)) != "from-setenv-bg" {
		t.Fatalf("stdout = %q, want %q", v, "from-setenv-bg")
	}
}

func TestSetenvAffectsPassthrough(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsAndEnvVarsEnv(t)
	runEnv(t, `setenv("NINESH_TEST_VAR", "from-setenv-dollar")`, env)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	runEnv(t, `$sh "-c" "echo -n $NINESH_TEST_VAR"`, env)
	os.Stdout = origStdout
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if string(captured) != "from-setenv-dollar" {
		t.Fatalf("stdout = %q, want %q", captured, "from-setenv-dollar")
	}
}

// writeProbeScript creates a tiny executable shell script named
// "probe-tool" in dir, printing marker to stdout -- a command that
// exists ONLY in dir, nowhere on this test process's own real PATH, so
// finding it at all proves setenv("PATH", ...) actually changed command
// *resolution*, not just what a child sees about its own environment
// (the gap pathresolve.LookPath exists to close -- see its doc comment).
func writeProbeScript(t *testing.T, dir, marker string) {
	t.Helper()
	path := dir + "/probe-tool"
	script := "#!/bin/sh\necho -n " + marker + "\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestSetenvPathAffectsForegroundCommandResolution(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	dir := t.TempDir()
	writeProbeScript(t, dir, "found-it")
	env := jobsAndEnvVarsEnv(t)

	// Without the override, the tool genuinely can't be found -- proves
	// the later success isn't a coincidence of it already being on this
	// test process's real PATH.
	if v := runEnv(t, `%probe-tool`, env); !isErrorVal(v) {
		t.Fatalf("expected ErrorVal before setenv(PATH), got %#v", v)
	}

	runEnv(t, `setenv("PATH", "`+dir+`")`, env)
	v := runEnv(t, `%probe-tool`, env)
	if string(v.(value.Bytes)) != "found-it" {
		t.Fatalf("stdout = %q, want %q", v, "found-it")
	}
}

func TestSetenvPathAffectsBackgroundJobResolution(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	dir := t.TempDir()
	writeProbeScript(t, dir, "found-it-bg")
	env := jobsAndEnvVarsEnv(t)
	runEnv(t, `setenv("PATH", "`+dir+`")`, env)

	v := runEnv(t, `j := %probe-tool &
j | wait
j.stdout`, env)
	if string(v.(value.Bytes)) != "found-it-bg" {
		t.Fatalf("stdout = %q, want %q", v, "found-it-bg")
	}
}

func TestSetenvPathAffectsPassthroughResolution(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	dir := t.TempDir()
	writeProbeScript(t, dir, "found-it-dollar")
	env := jobsAndEnvVarsEnv(t)
	runEnv(t, `setenv("PATH", "`+dir+`")`, env)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	runEnv(t, `$probe-tool`, env)
	os.Stdout = origStdout
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if string(captured) != "found-it-dollar" {
		t.Fatalf("stdout = %q, want %q", captured, "found-it-dollar")
	}
}

func isErrorVal(v value.Value) bool {
	_, ok := v.(value.ErrorVal)
	return ok
}

// globEnv is a jobsEnv plus /testdir bound (dirfs) over a fresh scratch
// directory -- for tests exercising glob(pattern).
func globEnv(t *testing.T) (*Env, string) {
	t.Helper()
	env := jobsEnv(t)
	dir := t.TempDir()
	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	if err := env.Namespace().BindFS(fs, "", "/testdir", ns.Replace); err != nil {
		t.Fatalf("bind /testdir: %v", err)
	}
	return env, dir
}

func TestGlobMatchesFiles(t *testing.T) {
	env, dir := globEnv(t)
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(dir+"/"+name, []byte("x"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	v := runEnv(t, `glob("/testdir/*.go")`, env)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 2 {
		t.Fatalf("got %d matches, want 2: %v", len(lst.Elems), v)
	}
	if lst.Elems[0].(value.Path) != "/testdir/a.go" || lst.Elems[1].(value.Path) != "/testdir/b.go" {
		t.Errorf("got %v, want sorted [/testdir/a.go /testdir/b.go]", v)
	}
}

func TestGlobNoMatchesReturnsEmptyList(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `glob("/testdir/*.nonexistent")`, env)
	lst, ok := v.(*value.List)
	if !ok || len(lst.Elems) != 0 {
		t.Fatalf("want empty List, got %#v", v)
	}
}

func TestGlobIsPipeable(t *testing.T) {
	env, dir := globEnv(t)
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(dir+"/"+name, []byte("x"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	v := runEnv(t, `glob("/testdir/*.go") | count`, env)
	if v.(value.Int) != 2 {
		t.Fatalf("got %v, want 2", v)
	}
}

func TestGlobBadDirectoryIsErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `glob("/nonexistent-dir/*.go")`, env)
	if !isErrorVal(v) {
		t.Fatalf("want ErrorVal, got %#v", v)
	}
}

func TestGlobWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `glob("/local/*.go")`)
}

// TestForegroundExternalCallInheritsProcessEnv above already locks in
// that a job without /env bound (jobsEnv, not jobsAndEnvVarsEnv) still
// inherits this test process's own environment unchanged — envSlice
// returning nil for "no /env bound" must not regress that.

func TestInterruptHandlerSetAndCleared(t *testing.T) {
	env := jobsEnv(t)
	if env.InterruptHandler() != nil {
		t.Fatal("InterruptHandler should start nil")
	}
	called := false
	env.SetInterruptHandler(func() { called = true })
	h := env.InterruptHandler()
	if h == nil {
		t.Fatal("InterruptHandler should return the registered handler")
	}
	h()
	if !called {
		t.Fatal("the registered handler should have run")
	}
	env.SetInterruptHandler(nil)
	if env.InterruptHandler() != nil {
		t.Fatal("InterruptHandler should be nil after clearing")
	}
}

func TestForegroundExternalCallSetsAndClearsInterruptHandler(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env := jobsEnv(t)
	runEnv(t, `%echo "hi"`, env)
	if env.InterruptHandler() != nil {
		t.Fatal("InterruptHandler should be cleared once the foreground command returns")
	}
}

func TestForegroundExternalCallForwardsStderr(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	v := runEnv(t, `%sh "-c" "echo -n out-text; echo -n err-text 1>&2"`, env)
	os.Stderr = origStderr
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stderr: %v", err)
	}

	if string(v.(value.Bytes)) != "out-text" {
		t.Fatalf("stdout = %q, want %q", v, "out-text")
	}
	if string(captured) != "err-text" {
		t.Fatalf("stderr forwarded = %q, want %q", captured, "err-text")
	}
}

func TestForegroundExternalCallNonzeroExitIsNotAnError(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "exit 3"`, env)
	if _, ok := v.(value.ErrorVal); ok {
		t.Fatalf("a nonzero exit should be ordinary data, not ErrorVal, got %#v", v)
	}
	if _, ok := v.(value.Bytes); !ok {
		t.Fatalf("want value.Bytes, got %T", v)
	}
}

func TestHostBuiltin(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	v := run(t, `host()`)
	s, ok := v.(value.String)
	if !ok {
		t.Fatalf("host() = %#v, want a string", v)
	}
	if string(s) != want {
		t.Fatalf("host() = %q, want %q", s, want)
	}
}
