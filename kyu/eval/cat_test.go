package eval

import (
	"os"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestCatReadsFileContent(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/f.txt", []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `cat(/testdir/f.txt)`, env)
	s, ok := v.(value.String)
	if !ok {
		t.Fatalf("want String, got %#v", v)
	}
	if string(s) != "hello world" {
		t.Errorf("cat = %q, want %q", s, "hello world")
	}
}

func TestCatIsPipeableIntoStringOps(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/f.txt", []byte("  padded  "), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `cat(/testdir/f.txt) | trim`, env)
	if v.(value.String) != "padded" {
		t.Errorf("got %q, want %q", v, "padded")
	}
}

func TestCatOnDirectoryIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `cat(/testdir)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestCatOnNonexistentPathIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `cat(/testdir/does-not-exist)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
