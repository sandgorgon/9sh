package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
