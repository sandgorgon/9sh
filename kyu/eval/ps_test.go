package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestPsListsJobsAfterForegroundCall(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env, _ := jobsEnvWithManager(t)
	runEnv(t, `%echo "hi"`, env)

	v := runEnv(t, `ps()`, env)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 1 {
		t.Fatalf("expected 1 job, got %d", len(lst.Elems))
	}
	rec, ok := lst.Elems[0].(*value.Record)
	if !ok {
		t.Fatalf("want Record, got %#v", lst.Elems[0])
	}
	if id, _ := rec.Get("id"); id.(value.Int) != 1 {
		t.Errorf("id = %v, want 1", id)
	}
	if state, _ := rec.Get("state"); state.(value.String) != "done" {
		t.Errorf("state = %v, want done", state)
	}
	if kind, _ := rec.Get("kind"); kind.(value.String) != "subprocess" {
		t.Errorf("kind = %v, want subprocess", kind)
	}
	ec, _ := rec.Get("exit_code")
	if ec.(value.Int) != 0 {
		t.Errorf("exit_code = %v, want 0", ec)
	}
}

func TestPsOnEmptyJobsIsEmptyList(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `ps()`, env)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(lst.Elems))
	}
}
