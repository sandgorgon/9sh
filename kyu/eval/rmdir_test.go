package eval

import (
	"os"
	"testing"
)

func TestRmdirRemovesEmptyDirectory(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/empty", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	runEnv(t, `rmdir(/testdir/empty)`, env)
	if _, err := os.Stat(dir + "/empty"); !os.IsNotExist(err) {
		t.Errorf("empty still exists (or unexpected stat error): %v", err)
	}
}

func TestRmdirNonEmptyIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/full", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(dir+"/full/inside.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `rmdir(/testdir/full)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestRmdirRecursiveRemovesNonEmptyDirectory(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.MkdirAll(dir+"/tree/nested", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(dir+"/tree/top.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/tree/nested/deep.txt", []byte("y"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runEnv(t, `rmdir(/testdir/tree, true)`, env)
	if _, err := os.Stat(dir + "/tree"); !os.IsNotExist(err) {
		t.Errorf("tree still exists (or unexpected stat error): %v", err)
	}
}

func TestRmdirOnFileIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/afile.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `rmdir(/testdir/afile.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestRmdirNonexistentIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `rmdir(/testdir/does-not-exist)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
