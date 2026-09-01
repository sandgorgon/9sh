package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/value"
)

func TestScriptArgsList(t *testing.T) {
	lst := scriptArgsList([]string{"foo", "bar"})
	if len(lst.Elems) != 2 {
		t.Fatalf("got %d elements, want 2", len(lst.Elems))
	}
	if lst.Elems[0].(value.String) != "foo" || lst.Elems[1].(value.String) != "bar" {
		t.Errorf("got %v, want [foo bar]", lst)
	}
	if empty := scriptArgsList(nil); len(empty.Elems) != 0 {
		t.Errorf("no script args should give an empty list, got %v", empty)
	}
}

// TestRunSourceSeesScriptArgs is the same "args" wiring run() itself
// does before calling runSource, exercised end-to-end through the real
// eval path (not just scriptArgsList in isolation).
func TestRunSourceSeesScriptArgs(t *testing.T) {
	env := eval.NewGlobalEnv(nil)
	env.Define("args", scriptArgsList([]string{"hello", "world"}))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	ok := runSource(`args | join(" ")`, env)
	os.Stdout = origStdout
	w.Close()
	if !ok {
		t.Fatal("runSource reported failure")
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if got := string(out); got != "hello world\n" {
		t.Fatalf("got %q, want %q", got, "hello world\n")
	}
}

// TestBootstrapExportsUnixSocketEnv checks that an active -listen-unix
// gets exported into this process's own environment as _9SH_UNIX_SOCK —
// the mechanism a job 9sh spawns relies on to auto-discover its parent's
// namespace socket, mirroring SSH_AUTH_SOCK (see bootstrap's doc comment
// at the -listen-unix branch). Named with a leading underscore rather
// than a leading digit deliberately: "9SH_UNIX_SOCK" isn't a valid POSIX
// shell identifier, so $9SH_UNIX_SOCK would silently fail to expand in
// any spawned sh/bash job.
func TestBootstrapExportsUnixSocketEnv(t *testing.T) {
	t.Setenv("_9SH_UNIX_SOCK", "") // isolate from any ambient value
	sockPath := filepath.Join(t.TempDir(), "9sh.sock")

	bootstrap("", sockPath)

	got := os.Getenv("_9SH_UNIX_SOCK")
	if got != sockPath {
		t.Fatalf("_9SH_UNIX_SOCK = %q, want %q", got, sockPath)
	}
}

// TestBootstrapLeavesUnixSocketEnvUnsetWhenNotListening checks the
// opposite: without -listen-unix, bootstrap must not export a stale or
// empty _9SH_UNIX_SOCK that a spawned job could mistake for a real,
// dialable socket.
func TestBootstrapLeavesUnixSocketEnvUnsetWhenNotListening(t *testing.T) {
	t.Setenv("_9SH_UNIX_SOCK", "")
	os.Unsetenv("_9SH_UNIX_SOCK")

	bootstrap("", "")

	if _, ok := os.LookupEnv("_9SH_UNIX_SOCK"); ok {
		t.Fatal("expected _9SH_UNIX_SOCK to remain unset when -listen-unix wasn't passed")
	}
}
