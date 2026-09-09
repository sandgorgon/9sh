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

func TestMvSourceIsDirectoryIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/subdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	v := runEnv(t, `mv(/testdir/subdir, /testdir/dst.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestMvSourceNonexistentIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `mv(/testdir/does-not-exist, /testdir/dst.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
