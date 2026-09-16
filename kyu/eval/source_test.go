package eval

import (
	"os"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestSourceConfigNotAvailableWithoutHook(t *testing.T) {
	env := jobsEnv(t)
	if err := runEnvErr(t, `source_config()`, env); err == nil {
		t.Fatalf("expected a hard error, got none")
	}
}

func TestResetConfigNotAvailableWithoutHook(t *testing.T) {
	env := jobsEnv(t)
	if err := runEnvErr(t, `reset_config()`, env); err == nil {
		t.Fatalf("expected a hard error, got none")
	}
}

func TestSourceConfigRunsHook(t *testing.T) {
	env := jobsEnv(t)
	calls := 0
	env.SetSourceConfig(func(e *Env) {
		calls++
		e.Define("from_hook", value.Int(1))
	})
	runEnv(t, `source_config()`, env)
	if calls != 1 {
		t.Fatalf("hook called %d times, want 1", calls)
	}
	if v, ok := env.Get("from_hook"); !ok || v != value.Int(1) {
		t.Errorf("from_hook = %#v, ok=%v, want Int(1)", v, ok)
	}
}

func TestSourceConfigDoesNotClearExistingVars(t *testing.T) {
	env := jobsEnv(t)
	env.SetSourceConfig(func(e *Env) {})
	env.Define("mine", value.Int(42))
	runEnv(t, `source_config()`, env)
	if v, ok := env.Get("mine"); !ok || v != value.Int(42) {
		t.Errorf("mine = %#v, ok=%v, want Int(42) (source_config is additive)", v, ok)
	}
}

func TestResetConfigClearsUserVarsAndReruns(t *testing.T) {
	env := jobsEnv(t)
	calls := 0
	env.SetSourceConfig(func(e *Env) {
		calls++
		e.Define("fresh", value.Int(2))
	})
	env.Define("stale", value.Int(1))
	env.Define("args", value.NewList(nil)) // process-provided, must survive
	runEnv(t, `reset_config()`, env)
	if calls != 1 {
		t.Fatalf("hook called %d times, want 1", calls)
	}
	if _, ok := env.Get("stale"); ok {
		t.Errorf("stale variable survived reset_config()")
	}
	if _, ok := env.Get("args"); !ok {
		t.Errorf("args (process-provided) was cleared by reset_config()")
	}
	if v, ok := env.Get("fresh"); !ok || v != value.Int(2) {
		t.Errorf("fresh = %#v, ok=%v, want Int(2)", v, ok)
	}
}

func TestResetConfigUnbindsNonCoreEntries(t *testing.T) {
	env, dir := globEnv(t) // binds /jobs (core) and /testdir (not core)
	env.SetSourceConfig(func(e *Env) {})
	if err := os.WriteFile(dir+"/marker.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if v := runEnv(t, `glob("/testdir/*")`, env); len(v.(*value.List).Elems) != 1 {
		t.Fatalf("setup: /testdir not bound to dir: %#v", v)
	}
	runEnv(t, `reset_config()`, env)
	// unbind leaves a walkable-but-empty tree node behind (its own
	// existing, pre-reset_config semantics — see ns.Namespace.Unbind's
	// doc comment: it clears layers, not the node), so an emptied
	// directory listing, not a stat error, is what "actually unbound"
	// looks like here.
	if v := runEnv(t, `glob("/testdir/*")`, env); len(v.(*value.List).Elems) != 0 {
		t.Errorf("/testdir still has content after reset_config(): %#v", v)
	}
	if v := runEnv(t, `glob("/jobs/*")`, env); v.Kind() == "error" {
		t.Errorf("/jobs (a protected root) was unbound by reset_config(): %#v", v)
	}
}
