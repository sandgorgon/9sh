package eval

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/9p/server"

	"github.com/sandgorgon/9sh/ns"
)

// unlistableFS wraps a real directory but fails every read of its root
// directory, standing in for a bound remote that has become unreachable:
// the files are all still there, the namespace just can't see them.
type unlistableFS struct{ server.FileSystem }

func (u unlistableFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	root, err := u.FileSystem.Attach(ctx, uname, aname)
	if err != nil {
		return nil, err
	}
	return unlistableRoot{root}, nil
}

type unlistableRoot struct{ server.File }

func (unlistableRoot) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("connection reset")
}

// unreachableEnv binds a directory holding one file at /dead through
// unlistableFS, alongside globEnv's healthy /testdir.
func unreachableEnv(t *testing.T) (env *Env, deadDir, liveDir string) {
	t.Helper()
	env, liveDir = globEnv(t)
	deadDir = t.TempDir()
	if err := os.WriteFile(deadDir+"/precious.txt", []byte("keep me"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fs, err := dirfs.New(deadDir)
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	if err := env.Namespace().BindFS(unlistableFS{fs}, "", "/dead", ns.Replace); err != nil {
		t.Fatalf("bind /dead: %v", err)
	}
	return env, deadDir, liveDir
}

func assertPrecious(t *testing.T, deadDir string) {
	t.Helper()
	got, err := os.ReadFile(deadDir + "/precious.txt")
	if err != nil {
		t.Fatalf("precious.txt is gone: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("precious.txt = %q, want %q", got, "keep me")
	}
}

func TestLsOfUnreachableBindIsAnError(t *testing.T) {
	env, _, _ := unreachableEnv(t)
	err := runEnvErr(t, `ls("/dead/*")`, env)
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error %q should carry the layer's own failure", err)
	}
}

// mv of a directory copies it and then removes the source; if the source's
// listing silently came back empty, that would copy nothing and then remove
// the source. It must fail without touching the source.
func TestMvDirectoryFromUnreachableBindLeavesSourceIntact(t *testing.T) {
	env, deadDir, liveDir := unreachableEnv(t)
	v := runEnv(t, `mv(/dead, /testdir/moved)`, env)
	if v.Kind() != "error" {
		t.Fatalf("mv = %#v (%s), want an ErrorVal", v, v.Kind())
	}
	assertPrecious(t, deadDir)
	if _, err := os.Stat(liveDir + "/moved"); err == nil {
		t.Errorf("mv left a (necessarily incomplete) destination behind")
	}
}

func TestRmdirRecursiveOfUnreachableBindLeavesContentIntact(t *testing.T) {
	env, deadDir, _ := unreachableEnv(t)
	v := runEnv(t, `rmdir(/dead, true)`, env)
	if v.Kind() != "error" {
		t.Fatalf("rmdir = %#v (%s), want an ErrorVal", v, v.Kind())
	}
	assertPrecious(t, deadDir)
}
