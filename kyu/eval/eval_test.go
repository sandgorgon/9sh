package eval

import (
	"os/exec"
	"testing"

	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/value"
)

func run(t *testing.T, src string) value.Value {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	v, err := Eval(prog, NewGlobalEnv())
	if err != nil {
		t.Fatalf("src %q: eval error: %v", src, err)
	}
	return v
}

func runErr(t *testing.T, src string) error {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	_, err := Eval(prog, NewGlobalEnv())
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

func TestClosureCall(t *testing.T) {
	if v := run(t, `add := { |a, b| a + b }
add(2, 3)`); v.(value.Int) != 5 {
		t.Errorf("got %v, want 5", v)
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
