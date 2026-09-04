package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestVarsEmptyOnFreshEnv(t *testing.T) {
	v := run(t, `vars()`)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 0 {
		t.Fatalf("got %d entries on a fresh env (should exclude all builtins), want 0: %v", len(lst.Elems), v)
	}
}

func TestVarsListsUserDefinedVariable(t *testing.T) {
	v := run(t, `h := /local/some/dir
vars()`)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 1 {
		t.Fatalf("got %d entries, want 1: %v", len(lst.Elems), v)
	}
	rec, ok := lst.Elems[0].(*value.Record)
	if !ok {
		t.Fatalf("want Record, got %#v", lst.Elems[0])
	}
	name, _ := rec.Get("name")
	if name.(value.String) != "h" {
		t.Errorf("name = %v, want h", name)
	}
	kind, _ := rec.Get("kind")
	if kind.(value.String) != "path" {
		t.Errorf("kind = %v, want path", kind)
	}
	val, _ := rec.Get("value")
	if val.(value.Path) != "/local/some/dir" {
		t.Errorf("value = %v, want /local/some/dir", val)
	}
}

func TestVarsSortedByName(t *testing.T) {
	v := run(t, `z := 1
a := 2
m := 3
vars()`)
	lst := v.(*value.List)
	if len(lst.Elems) != 3 {
		t.Fatalf("got %d entries, want 3: %v", len(lst.Elems), v)
	}
	var names []string
	for _, e := range lst.Elems {
		n, _ := e.(*value.Record).Get("name")
		names = append(names, string(n.(value.String)))
	}
	want := []string{"a", "m", "z"}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}

func TestVarsIncludesUserClosures(t *testing.T) {
	v := run(t, `greet := { |x| x }
vars()`)
	lst := v.(*value.List)
	if len(lst.Elems) != 1 {
		t.Fatalf("got %d entries, want 1: %v", len(lst.Elems), v)
	}
	kind, _ := lst.Elems[0].(*value.Record).Get("kind")
	if kind.(value.String) != "function" {
		t.Errorf("kind = %v, want function", kind)
	}
}

func TestVarsRejectsArguments(t *testing.T) {
	runErr(t, `vars(5)`)
}

func TestUnsetRemovesVariable(t *testing.T) {
	runErr(t, `h := 1
unset("h")
h`)
}

func TestUnsetReturnsWhetherFound(t *testing.T) {
	v := run(t, `h := 1
unset("h")`)
	if v.(value.Bool) != true {
		t.Errorf("unset of a defined variable = %v, want true", v)
	}
	v2 := run(t, `unset("never_defined")`)
	if v2.(value.Bool) != false {
		t.Errorf("unset of an undefined name = %v, want false", v2)
	}
}

func TestUnsetShrinksVarsList(t *testing.T) {
	v := run(t, `h := 1
unset("h")
vars()`)
	lst := v.(*value.List)
	if len(lst.Elems) != 0 {
		t.Fatalf("got %d entries after unset, want 0: %v", len(lst.Elems), v)
	}
}

func TestUnsetRejectsBuiltin(t *testing.T) {
	runErr(t, `unset("dial")`)
}

func TestUnsetRejectsNonStringArg(t *testing.T) {
	runErr(t, `unset(5)`)
}

func TestUnsetRejectsWrongArgCount(t *testing.T) {
	runErr(t, `unset()`)
	runErr(t, `unset("a", "b")`)
}
