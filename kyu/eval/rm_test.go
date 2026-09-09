package eval

import (
	"os"
	"testing"
)

func TestRmRemovesFile(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/gone.txt", []byte("bye"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `rm(/testdir/gone.txt)`, env)
	if _, err := os.Stat(dir + "/gone.txt"); !os.IsNotExist(err) {
		t.Errorf("gone.txt still exists (or unexpected stat error): %v", err)
	}
}

func TestRmDirectoryIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `rm(/testdir)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestRmNonexistentIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `rm(/testdir/does-not-exist)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
