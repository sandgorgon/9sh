package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

// sourceEnv is a bindsEnv with dir bound at /src, so files written under
// dir are reachable as source(/src/<name>).
func sourceEnv(t *testing.T, dir string) *Env {
	t.Helper()
	env := bindsEnv(t)
	runEnv(t, `bind dir("`+dir+`"), /src`, env)
	return env
}

func writeKy(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRunsAgainstGlobalEnv(t *testing.T) {
	dir := t.TempDir()
	writeKy(t, dir, "a.ky", "x := 41 + 1\nbind /src, /alias\n")
	env := sourceEnv(t, dir)
	if v := runEnv(t, `source(/src/a.ky)`, env); v != (value.Null{}) {
		t.Fatalf("source returned %#v, want null", v)
	}
	if v := runEnv(t, `x`, env); v != value.Int(42) {
		t.Fatalf("x = %#v, want 42", v)
	}
	found := false
	for _, el := range runEnv(t, `binds()`, env).(*value.List).Elems {
		if d, _ := el.(*value.Record).Get("dst"); d == value.Path("/alias") {
			found = true
		}
	}
	if !found {
		t.Fatal("bind from sourced file didn't take effect")
	}
}

func TestSourceParseErrorRunsNothing(t *testing.T) {
	dir := t.TempDir()
	writeKy(t, dir, "bad.ky", "y := 1\nz := := :=\n")
	env := sourceEnv(t, dir)
	v := runEnv(t, `source(/src/bad.ky)`, env)
	ev, ok := v.(value.ErrorVal)
	if !ok || !strings.Contains(ev.Msg, "bad.ky") {
		t.Fatalf("want ErrorVal naming the file, got %#v", v)
	}
	if _, defined := env.Get("y"); defined {
		t.Error("a file with a parse error must run nothing, but y was defined")
	}
}

func TestSourceRuntimeErrorAbortsWithPath(t *testing.T) {
	dir := t.TempDir()
	writeKy(t, dir, "boom.ky", "unbind /nothing/here\n")
	env := sourceEnv(t, dir)
	p := runEnvErr(t, `source(/src/boom.ky)`, env)
	if !strings.Contains(p.Error(), "/src/boom.ky") {
		t.Fatalf("error should name the path, got %v", p)
	}
}

func TestSourceMissingAndDirectoryAreErrorVals(t *testing.T) {
	dir := t.TempDir()
	env := sourceEnv(t, dir)
	for _, src := range []string{`source(/src/nope.ky)`, `source(/src)`} {
		if _, ok := runEnv(t, src, env).(value.ErrorVal); !ok {
			t.Errorf("%s: want ErrorVal", src)
		}
	}
}

func TestSourceSelfRecursionIsBoundedError(t *testing.T) {
	dir := t.TempDir()
	writeKy(t, dir, "loop.ky", "source(/src/loop.ky)\n")
	env := sourceEnv(t, dir)
	err := runEnvErr(t, `source(/src/loop.ky)`, env)
	if !strings.Contains(err.Error(), "deep") {
		t.Fatalf("want a depth error, got %v", err)
	}
	// The counter must unwind, or every later source() would be refused.
	writeKy(t, dir, "ok.ky", "k := 1\n")
	runEnv(t, `source(/src/ok.ky)`, env)
}

func TestSourceReplaysNamespaceBinds(t *testing.T) {
	dir := t.TempDir()
	env := sourceEnv(t, dir)
	runEnv(t, `bind dir("`+dir+`"), /std`, env)
	runEnv(t, `bind /src, /std, after`, env)
	text := string(runEnv(t, `cat(/ns/binds)`, env).(value.String))
	writeKy(t, dir, "saved.ky", text)

	// A fresh namespace replays the saved list. Its own bootstrap /ns and
	// /src are needed to reach the file, so compare only the user binds.
	env2 := sourceEnv(t, dir)
	runEnv(t, `source(/src/saved.ky)`, env2)
	pick := func(e *Env) string {
		var out []string
		for _, el := range runEnv(t, `binds()`, e).(*value.List).Elems {
			r := el.(*value.Record)
			dst, _ := r.Get("dst")
			if d := string(dst.(value.Path)); d == "/std" {
				src, _ := r.Get("src")
				disp, _ := r.Get("disp")
				out = append(out, src.String()+"|"+disp.String())
			}
		}
		return strings.Join(out, ",")
	}
	if a, b := pick(env), pick(env2); a != b || a == "" {
		t.Fatalf("replayed /std layers = %q, original = %q", b, a)
	}
}
