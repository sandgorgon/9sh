package pathresolve

import (
	"os"
	"path/filepath"
	"testing"
)

func makeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLookPathUsesGivenPathOverride(t *testing.T) {
	dir := t.TempDir()
	want := makeExecutable(t, dir, "mytool")

	got, err := LookPath("mytool", []string{"PATH=" + dir})
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLookPathNotFoundInOverride(t *testing.T) {
	dir := t.TempDir()
	if _, err := LookPath("does-not-exist", []string{"PATH=" + dir}); err == nil {
		t.Fatal("expected an error for a missing executable, got nil")
	}
}

func TestLookPathFallsBackToRealPathWhenNoOverride(t *testing.T) {
	// no "PATH=" entry in env at all -- should behave like exec.LookPath
	got, err := LookPath("sh", nil)
	if err != nil {
		t.Skip("sh not on this system's real PATH")
	}
	if got == "" {
		t.Fatal("expected a resolved path")
	}
}

func TestLookPathWithSlashSkipsSearch(t *testing.T) {
	dir := t.TempDir()
	want := makeExecutable(t, dir, "mytool")

	got, err := LookPath(want, []string{"PATH=/nonexistent"})
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestLookPathEmptyPathSegmentMeansCwd locks in the empty-string branch
// of LookPath's dir loop. Note this is about an empty *segment* within
// PATH (e.g. a trailing/leading/doubled colon, POSIX's "search cwd
// here") -- an entirely empty PATH value has zero segments at all
// (filepath.SplitList("") returns no elements), so it searches nothing,
// not cwd.
func TestLookPathEmptyPathSegmentMeansCwd(t *testing.T) {
	dir := t.TempDir()
	makeExecutable(t, dir, "mytool")

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer os.Chdir(orig)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got, err := LookPath("mytool", []string{"PATH=/nonexistent:"})
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	// filepath.Join(".", "mytool") cleans away the "./" prefix.
	if got != "mytool" {
		t.Fatalf("got %q, want %q", got, "mytool")
	}
}
