package eval

import (
	"os"
	"testing"
)

func TestMvRenamesWithinSameDirectory(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/old.txt", []byte("rename me"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `mv(/testdir/old.txt, /testdir/new.txt)`, env)
	if _, err := os.Stat(dir + "/old.txt"); !os.IsNotExist(err) {
		t.Errorf("old.txt still exists (or unexpected stat error): %v", err)
	}
	got, err := os.ReadFile(dir + "/new.txt")
	if err != nil {
		t.Fatalf("read back new.txt: %v", err)
	}
	if string(got) != "rename me" {
		t.Errorf("new.txt content = %q, want %q", got, "rename me")
	}
}

func TestMvAcrossDirectoriesFallsBackToCopyThenRemove(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/src.txt", []byte("move me"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(dir+"/subdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	runEnv(t, `mv(/testdir/src.txt, /testdir/subdir/dst.txt)`, env)
	if _, err := os.Stat(dir + "/src.txt"); !os.IsNotExist(err) {
		t.Errorf("src.txt still exists (or unexpected stat error): %v", err)
	}
	got, err := os.ReadFile(dir + "/subdir/dst.txt")
	if err != nil {
		t.Fatalf("read back subdir/dst.txt: %v", err)
	}
	if string(got) != "move me" {
		t.Errorf("subdir/dst.txt content = %q, want %q", got, "move me")
	}
}

func TestMvRenamesDirectoryWithinSameParent(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/subdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(dir+"/subdir/inside.txt", []byte("still here"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `mv(/testdir/subdir, /testdir/renamed)`, env)
	if _, err := os.Stat(dir + "/subdir"); !os.IsNotExist(err) {
		t.Errorf("subdir still exists (or unexpected stat error): %v", err)
	}
	got, err := os.ReadFile(dir + "/renamed/inside.txt")
	if err != nil {
		t.Fatalf("read back renamed/inside.txt: %v", err)
	}
	if string(got) != "still here" {
		t.Errorf("renamed/inside.txt content = %q, want %q", got, "still here")
	}
}

func TestMvDirectoryAcrossDirectoriesCopiesThenRemoves(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.MkdirAll(dir+"/subdir/nested", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dir+"/subdir/top.txt", []byte("top"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/subdir/nested/deep.txt", []byte("deep"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(dir+"/otherdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	runEnv(t, `mv(/testdir/subdir, /testdir/otherdir/subdir2)`, env)
	if _, err := os.Stat(dir + "/subdir"); !os.IsNotExist(err) {
		t.Errorf("subdir still exists (or unexpected stat error): %v", err)
	}
	got, err := os.ReadFile(dir + "/otherdir/subdir2/top.txt")
	if err != nil {
		t.Fatalf("read back otherdir/subdir2/top.txt: %v", err)
	}
	if string(got) != "top" {
		t.Errorf("otherdir/subdir2/top.txt content = %q, want %q", got, "top")
	}
	got, err = os.ReadFile(dir + "/otherdir/subdir2/nested/deep.txt")
	if err != nil {
		t.Fatalf("read back otherdir/subdir2/nested/deep.txt: %v", err)
	}
	if string(got) != "deep" {
		t.Errorf("otherdir/subdir2/nested/deep.txt content = %q, want %q", got, "deep")
	}
}

func TestMvSourceNonexistentIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `mv(/testdir/does-not-exist, /testdir/dst.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestMvFileIntoExistingDirectory(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/a.txt", "move me")
	if err := os.Mkdir(dir+"/subdir", 0755); err != nil {
		t.Fatal(err)
	}
	if v := runEnv(t, `mv(/testdir/a.txt, /testdir/subdir)`, env); v.Kind() == "error" {
		t.Fatalf("unexpected error: %s", v.String())
	}
	if got := readFile(t, dir+"/subdir/a.txt"); got != "move me" {
		t.Errorf("subdir/a.txt = %q, want %q", got, "move me")
	}
	if exists(dir + "/a.txt") {
		t.Error("the source is still there after the move")
	}
}

func TestMvDirectoryIntoExistingDirectory(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/subdir/nested/deep.txt", "deep")
	if err := os.Mkdir(dir+"/otherdir", 0755); err != nil {
		t.Fatal(err)
	}
	if v := runEnv(t, `mv(/testdir/subdir, /testdir/otherdir)`, env); v.Kind() == "error" {
		t.Fatalf("unexpected error: %s", v.String())
	}
	if got := readFile(t, dir+"/otherdir/subdir/nested/deep.txt"); got != "deep" {
		t.Errorf("otherdir/subdir/nested/deep.txt = %q, want %q", got, "deep")
	}
	if exists(dir + "/subdir") {
		t.Error("the source directory is still there after the move")
	}
}

// Nothing is moved, merged or removed when the destination directory
// already has an entry of that name.
func TestMvDirectoryIntoDirectoryThatAlreadyHasThatNameIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/subdir/new.txt", "new")
	writeFile(t, dir+"/otherdir/subdir/existing.txt", "existing")
	wantErrorVal(t, runEnv(t, `mv(/testdir/subdir, /testdir/otherdir)`, env), "already exists")
	if got := readFile(t, dir+"/subdir/new.txt"); got != "new" {
		t.Errorf("the source was disturbed: new.txt = %q", got)
	}
	if exists(dir + "/otherdir/subdir/new.txt") {
		t.Error("the refused move merged into the existing directory")
	}
}

// mv(/d/f, /d) resolves to /d/f onto itself. Copy-then-remove of a file
// onto itself would delete the only copy, so this must be refused.
func TestMvFileOntoItselfViaItsParentIsErrorValAndKeepsTheFile(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/f.txt", "the only copy")
	wantErrorVal(t, runEnv(t, `mv(/testdir/f.txt, /testdir)`, env), "same file")
	if got := readFile(t, dir+"/f.txt"); got != "the only copy" {
		t.Errorf("f.txt = %q, want it untouched", got)
	}
}

func TestMvDirectoryIntoItselfIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/srcdir/a.txt", "a")
	if err := os.Mkdir(dir+"/srcdir/sub", 0755); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		`mv(/testdir/srcdir, /testdir/srcdir/sub)`,
		`mv(/testdir/srcdir, /testdir/srcdir/fresh)`,
		`mv(/testdir/srcdir, /testdir/srcdir)`,
	} {
		wantErrorVal(t, runEnv(t, src, env), "into itself")
	}
	if got := readFile(t, dir+"/srcdir/a.txt"); got != "a" {
		t.Errorf("a.txt = %q, want the source untouched", got)
	}
}

// The plain same-parent rename and a fresh destination are unaffected.
func TestMvStillRenamesWhenDestinationIsNotADirectory(t *testing.T) {
	env, dir := globEnv(t)
	writeFile(t, dir+"/a.txt", "x")
	runEnv(t, `mv(/testdir/a.txt, /testdir/b.txt)`, env)
	if !exists(dir+"/b.txt") || exists(dir+"/a.txt") {
		t.Error("rename didn't happen")
	}
}
