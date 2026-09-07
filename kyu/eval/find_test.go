package eval

import (
	"os"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestFindMatchesRecursively(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/top.go", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(dir+"/sub", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(dir+"/sub/nested.go", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/sub/nested.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	v := runEnv(t, `find(/testdir, "*.go")`, env)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	var got []string
	for _, e := range lst.Elems {
		got = append(got, string(e.(value.Path)))
	}
	want := []string{"/testdir/sub/nested.go", "/testdir/top.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFindMatchesDirectoriesToo(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/target", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	v := runEnv(t, `find(/testdir, "target")`, env)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 1 || lst.Elems[0].(value.Path) != "/testdir/target" {
		t.Fatalf("got %#v, want [/testdir/target]", lst.Elems)
	}
}

func TestFindNonexistentDirIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `find(/testdir/does-not-exist, "*")`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
