package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestHelpWithNameReturnsRecord(t *testing.T) {
	v := run(t, `help("bind")`)
	rec, ok := v.(*value.Record)
	if !ok {
		t.Fatalf("want Record, got %#v", v)
	}
	if n, _ := rec.Get("name"); n.(value.String) != "bind" {
		t.Errorf("name = %v, want bind", n)
	}
	sig, _ := rec.Get("signature")
	if sig.(value.String) == "" {
		t.Error("signature is empty")
	}
	desc, _ := rec.Get("description")
	if desc.(value.String) == "" {
		t.Error("description is empty")
	}
}

func TestHelpUnknownNameIsErrorVal(t *testing.T) {
	v := run(t, `help("not_a_real_builtin")`)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestHelpNoArgsListsEveryEntry(t *testing.T) {
	v := run(t, `help()`)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != len(Docs()) {
		t.Fatalf("got %d entries, want %d", len(lst.Elems), len(Docs()))
	}
	if _, ok := lst.Elems[0].(*value.Record); !ok {
		t.Fatalf("want Record elements, got %#v", lst.Elems[0])
	}
}

func TestHelpRejectsTooManyArgs(t *testing.T) {
	runErr(t, `help("a", "b")`)
}

func TestHelpRejectsNonStringArg(t *testing.T) {
	runErr(t, `help(5)`)
}

// TestDocsEveryEntryHasNonEmptyFields guards against a copy-paste gap
// in the hand-maintained table (docs.go) — every entry should have a
// real signature and description, not an accidentally-empty one.
func TestDocsEveryEntryHasNonEmptyFields(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Docs() {
		if d.Name == "" || d.Signature == "" || d.Description == "" {
			t.Errorf("entry %+v has an empty field", d)
		}
		if seen[d.Name] {
			t.Errorf("duplicate entry for %q", d.Name)
		}
		seen[d.Name] = true
	}
}
