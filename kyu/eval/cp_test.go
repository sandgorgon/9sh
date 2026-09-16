package eval

import (
	"os"
	"testing"
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

func TestCpDirectoryToExistingDestinationIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.Mkdir(dir+"/srcdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Mkdir(dir+"/dstdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	v := runEnv(t, `cp(/testdir/srcdir, /testdir/dstdir)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestCpDestIsDirectoryIsErrorVal(t *testing.T) {
	env, dir := globEnv(t)
	if err := os.WriteFile(dir+"/src.txt", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(dir+"/subdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	v := runEnv(t, `cp(/testdir/src.txt, /testdir/subdir)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestCpSourceNonexistentIsErrorVal(t *testing.T) {
	env, _ := globEnv(t)
	v := runEnv(t, `cp(/testdir/does-not-exist, /testdir/dst.txt)`, env)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}
