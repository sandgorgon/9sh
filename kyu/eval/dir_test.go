package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9sh/kyu/value"
)

// TestDirBindsIntoNamespace exercises dir(path), biDial's local sibling:
// an arbitrary host directory (not under wherever 9sh was launched from,
// so not already reachable via /local) becomes bindable via a
// MountHandle, exactly like a dial() result — see biDir's doc comment.
func TestDirBindsIntoNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "greeting"), []byte("hello from dir()\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := jobsEnv(t)
	src := `h := dir("` + dir + `")
bind h, /std/x`
	v := runEnv(t, src, env)
	if v.Kind() == "error" {
		t.Fatalf("bind failed: %s", v.String())
	}

	ctx := context.Background()
	root, err := env.Namespace().Attach(ctx, "test", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := root.Walk(ctx, "std")
	if err != nil {
		t.Fatalf("walk std: %v", err)
	}
	f, err = f.Walk(ctx, "x")
	if err != nil {
		t.Fatalf("walk std/x: %v", err)
	}
	f, err = f.Walk(ctx, "greeting")
	if err != nil {
		t.Fatalf("walk std/x/greeting: %v", err)
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	got, err := readAllFile(ctx, f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello from dir()\n" {
		t.Fatalf("got %q", got)
	}
}

func TestDirRejectsRelativePath(t *testing.T) {
	env := jobsEnv(t)
	err := runEnvErr(t, `dir("relative/path")`, env)
	if err == nil {
		t.Fatal("expected an error for a relative path, got none")
	}
}

func TestDirRejectsWrongArgCount(t *testing.T) {
	env := jobsEnv(t)
	err := runEnvErr(t, `dir()`, env)
	if err == nil {
		t.Fatal("expected an error for 0 arguments, got none")
	}
}

func TestDirRejectsNonStringArg(t *testing.T) {
	env := jobsEnv(t)
	err := runEnvErr(t, `dir(5)`, env)
	if err == nil {
		t.Fatal("expected an error for a non-string argument, got none")
	}
}

// TestDirNonexistentPathIsErrorVal matches dial's own convention: a bad
// target is an ordinary in-stream ErrorVal, not a hard eval error — see
// biDir's doc comment.
func TestDirNonexistentPathIsErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `dir("/nonexistent/9sh-dir-test-path")`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("got %T, want value.ErrorVal", v)
	}
}
