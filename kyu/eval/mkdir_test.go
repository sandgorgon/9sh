package eval

import (
	"os"
	"testing"
)

func TestMkdirCreatesDirectory(t *testing.T) {
	env, dir := globEnv(t)
	runEnv(t, `mkdir(/testdir/newdir)`, env)
	st, err := os.Stat(dir + "/newdir")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("newdir exists but isn't a directory")
	}
}

func TestMkdirCreatesMissingIntermediateDirectories(t *testing.T) {
	env, dir := globEnv(t)
	runEnv(t, `mkdir(/testdir/a/b/c)`, env)
	st, err := os.Stat(dir + "/a/b/c")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("a/b/c exists but isn't a directory")
	}
}

func TestMkdirAlreadyExistingDirectoryIsNoOp(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/already", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	v := runEnv(t, `mkdir(/testdir/already)`, env)
	if v.Kind() == "error" {
		t.Fatalf("got an ErrorVal for an already-existing directory: %#v", v)
	}
	st, err := os.Stat(dir + "/already")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("already exists but isn't a directory")
	}
}

func TestMkdirExistingFileIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/afile.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `mkdir(/testdir/afile.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestMkdirIntermediateIsFileIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/afile.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `mkdir(/testdir/afile.txt/newdir)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
