package eval

import (
	"os"
	"regexp"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

var permStringRe = regexp.MustCompile(`^[r-][w-][x-][r-][w-][x-][r-][w-][x-]$`)

func TestStatReturnsFileMetadata(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/f.txt", []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `stat(/testdir/f.txt)`, env)
	rec, ok := v.(*value.Record)
	if !ok {
		t.Fatalf("want Record, got %#v", v)
	}
	if p, _ := rec.Get("path"); p.(value.Path) != "/testdir/f.txt" {
		t.Errorf("path = %v, want /testdir/f.txt", p)
	}
	if n, _ := rec.Get("name"); n.(value.String) != "f.txt" {
		t.Errorf("name = %v, want f.txt", n)
	}
	if sz, _ := rec.Get("size"); sz.(value.Int) != 5 {
		t.Errorf("size = %v, want 5", sz)
	}
	if isDir, _ := rec.Get("is_dir"); isDir.(value.Bool) != false {
		t.Errorf("is_dir = %v, want false", isDir)
	}
	mode, _ := rec.Get("mode")
	if !permStringRe.MatchString(string(mode.(value.String))) {
		t.Errorf("mode = %q, want a 9-char rwx string", mode)
	}
	if mt, _ := rec.Get("mtime"); int64(mt.(value.Int)) <= 0 {
		t.Errorf("mtime = %v, want > 0", mt)
	}
}

func TestStatOnDirectoryReportsIsDir(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `stat(/testdir)`, env)
	rec, ok := v.(*value.Record)
	if !ok {
		t.Fatalf("want Record, got %#v", v)
	}
	if isDir, _ := rec.Get("is_dir"); isDir.(value.Bool) != true {
		t.Errorf("is_dir = %v, want true", isDir)
	}
}

func TestStatNonexistentPathIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `stat(/testdir/does-not-exist)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestStatRejectsNonPathArg(t *testing.T) {
	env, _ := globEnv(t)
	runEnvErr(t, `stat("/testdir/f.txt")`, env)
}

func TestStatRejectsWrongArgCount(t *testing.T) {
	env, _ := globEnv(t)
	runEnvErr(t, `stat()`, env)
	runEnvErr(t, `stat(/a, /b)`, env)
}

func TestLsReturnsRecordsWithMetadata(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/a.go", []byte("aa"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/b.go", []byte("bbbbb"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(dir+"/c.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v := runEnv(t, `ls("/testdir/*.go")`, env)
	lst, ok := v.(*value.List)
	if !ok {
		t.Fatalf("want List, got %#v", v)
	}
	if len(lst.Elems) != 2 {
		t.Fatalf("got %d matches, want 2: %v", len(lst.Elems), v)
	}
	first, ok := lst.Elems[0].(*value.Record)
	if !ok {
		t.Fatalf("want Record elements, got %#v", lst.Elems[0])
	}
	if n, _ := first.Get("name"); n.(value.String) != "a.go" {
		t.Errorf("first entry name = %v, want a.go", n)
	}
	if sz, _ := first.Get("size"); sz.(value.Int) != 2 {
		t.Errorf("first entry size = %v, want 2", sz)
	}
	second := lst.Elems[1].(*value.Record)
	if n, _ := second.Get("name"); n.(value.String) != "b.go" {
		t.Errorf("second entry name = %v, want b.go", n)
	}
	if sz, _ := second.Get("size"); sz.(value.Int) != 5 {
		t.Errorf("second entry size = %v, want 5", sz)
	}
}

func TestLsNoMatchesReturnsEmptyList(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `ls("/testdir/*.nonexistent")`, env)
	lst, ok := v.(*value.List)
	if !ok || len(lst.Elems) != 0 {
		t.Fatalf("want empty List, got %#v", v)
	}
}

func TestLsRejectsNonStringArg(t *testing.T) {
	env, _ := globEnv(t)
	runEnvErr(t, `ls(5)`, env)
}

func TestLsRejectsWrongArgCount(t *testing.T) {
	env, _ := globEnv(t)
	runEnvErr(t, `ls()`, env)
}
