package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestCpCreatesNewFile(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/src.txt", []byte("copy me"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `cp(/testdir/src.txt, /testdir/dst.txt)`, env)
	got, err := os.ReadFile(dir + "/dst.txt")
	if err != nil {
		t.Fatalf("read back dst.txt: %v", err)
	}
	if string(got) != "copy me" {
		t.Errorf("dst content = %q, want %q", got, "copy me")
	}
	// src is untouched
	src, err := os.ReadFile(dir + "/src.txt")
	if err != nil {
		t.Fatalf("read back src.txt: %v", err)
	}
	if string(src) != "copy me" {
		t.Errorf("src content = %q, want unchanged %q", src, "copy me")
	}
}

func TestCpOverwritesExistingFile(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/src.txt", []byte("new content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/dst.txt", []byte("stale content that is longer"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `cp(/testdir/src.txt, /testdir/dst.txt)`, env)
	got, err := os.ReadFile(dir + "/dst.txt")
	if err != nil {
		t.Fatalf("read back dst.txt: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("dst content = %q, want %q", got, "new content")
	}
}

func TestCpDirectoryCopiesTree(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.MkdirAll(dir+"/srcdir/nested", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dir+"/srcdir/top.txt", []byte("top"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/srcdir/nested/deep.txt", []byte("deep"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `cp(/testdir/srcdir, /testdir/dstdir)`, env)
	// src is untouched
	if _, err := os.Stat(dir + "/srcdir"); err != nil {
		t.Errorf("srcdir no longer exists: %v", err)
	}
	got, err := os.ReadFile(dir + "/dstdir/top.txt")
	if err != nil {
		t.Fatalf("read back dstdir/top.txt: %v", err)
	}
	if string(got) != "top" {
		t.Errorf("dstdir/top.txt content = %q, want %q", got, "top")
	}
	got, err = os.ReadFile(dir + "/dstdir/nested/deep.txt")
	if err != nil {
		t.Fatalf("read back dstdir/nested/deep.txt: %v", err)
	}
	if string(got) != "deep" {
		t.Errorf("dstdir/nested/deep.txt content = %q, want %q", got, "deep")
	}
}

func TestCpSourceNonexistentIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `cp(/testdir/does-not-exist, /testdir/dst.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

// writeFile/readFile/exists keep the into-directory tests below readable.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func wantErrorVal(t *testing.T, v value.Value, contains string) {
	t.Helper()
	ev, ok := v.(value.ErrorVal)
	if !ok {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
	if !strings.Contains(ev.Msg, contains) {
		t.Errorf("error %q should contain %q", ev.Msg, contains)
	}
}

// An existing directory as dst means "into it", as with Unix cp.
func TestCpFileIntoExistingDirectory(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/src.txt", "copy me")
	if err := os.Mkdir(dir+"/subdir", 0755); err != nil {
		t.Fatal(err)
	}
	if v := runEnv(t, `cp(/testdir/src.txt, /testdir/subdir)`, env); v.Kind() == "error" {
		t.Fatalf("unexpected error: %s", v.String())
	}
	if got := readFile(t, dir+"/subdir/src.txt"); got != "copy me" {
		t.Errorf("subdir/src.txt = %q, want %q", got, "copy me")
	}
	if got := readFile(t, dir+"/src.txt"); got != "copy me" {
		t.Errorf("src.txt = %q, want it untouched", got)
	}
}

func TestCpFileIntoDirectoryOverwritesSameNamedFile(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/src.txt", "new")
	writeFile(t, dir+"/subdir/src.txt", "old and longer")
	runEnv(t, `cp(/testdir/src.txt, /testdir/subdir)`, env)
	if got := readFile(t, dir+"/subdir/src.txt"); got != "new" {
		t.Errorf("subdir/src.txt = %q, want %q", got, "new")
	}
}

// A file can't replace a directory, even when the directory is only
// reached because dst resolved into another one.
func TestCpFileIntoDirectoryWhereThatNameIsADirectoryIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/src.txt", "x")
	writeFile(t, dir+"/subdir/src.txt/keep", "keep")
	v := runEnv(t, `cp(/testdir/src.txt, /testdir/subdir)`, env)
	wantErrorVal(t, v, "is a directory")
	if got := readFile(t, dir+"/subdir/src.txt/keep"); got != "keep" {
		t.Errorf("the existing directory was disturbed: keep = %q", got)
	}
}

func TestCpDirectoryIntoExistingDirectory(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/srcdir/top.txt", "top")
	writeFile(t, dir+"/srcdir/nested/deep.txt", "deep")
	if err := os.Mkdir(dir+"/dstdir", 0755); err != nil {
		t.Fatal(err)
	}
	if v := runEnv(t, `cp(/testdir/srcdir, /testdir/dstdir)`, env); v.Kind() == "error" {
		t.Fatalf("unexpected error: %s", v.String())
	}
	if got := readFile(t, dir+"/dstdir/srcdir/nested/deep.txt"); got != "deep" {
		t.Errorf("dstdir/srcdir/nested/deep.txt = %q, want %q", got, "deep")
	}
	if !exists(dir + "/srcdir/top.txt") {
		t.Error("the source tree was disturbed")
	}
}

// No merge semantics yet: a directory that already has an entry of that
// name is refused, and nothing in it is touched.
func TestCpDirectoryIntoDirectoryThatAlreadyHasThatNameIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/srcdir/new.txt", "new")
	writeFile(t, dir+"/dstdir/srcdir/existing.txt", "existing")
	v := runEnv(t, `cp(/testdir/srcdir, /testdir/dstdir)`, env)
	wantErrorVal(t, v, "already exists")
	if exists(dir + "/dstdir/srcdir/new.txt") {
		t.Error("the refused copy merged into the existing directory")
	}
	if got := readFile(t, dir+"/dstdir/srcdir/existing.txt"); got != "existing" {
		t.Errorf("existing.txt = %q, want it untouched", got)
	}
}

// Copying a tree into its own subtree would never terminate.
func TestCpDirectoryIntoItselfIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/srcdir/a.txt", "a")
	if err := os.Mkdir(dir+"/srcdir/sub", 0755); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		`cp(/testdir/srcdir, /testdir/srcdir/sub)`,   // into an existing subdirectory
		`cp(/testdir/srcdir, /testdir/srcdir/fresh)`, // to a new name inside itself
		`cp(/testdir/srcdir, /testdir/srcdir)`,       // onto itself
	} {
		wantErrorVal(t, runEnv(t, src, env), "into itself")
	}
	if exists(dir+"/srcdir/sub/srcdir") || exists(dir+"/srcdir/fresh") {
		t.Error("a refused copy still created something inside the source")
	}
}

// cp(/d/f, /d) resolves to /d/f onto itself.
func TestCpFileOntoItselfViaItsParentIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/f.txt", "precious")
	wantErrorVal(t, runEnv(t, `cp(/testdir/f.txt, /testdir)`, env), "same file")
	if got := readFile(t, dir+"/f.txt"); got != "precious" {
		t.Errorf("f.txt = %q, want it untouched", got)
	}
}

// The destination named in an error is the resolved one, not the
// directory that was passed.
func TestCpErrorNamesTheResolvedDestination(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/src.txt", "x")
	writeFile(t, dir+"/subdir/src.txt/keep", "keep")
	wantErrorVal(t, runEnv(t, `cp(/testdir/src.txt, /testdir/subdir)`, env), "/testdir/subdir/src.txt")
}
