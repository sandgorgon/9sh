package eval

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/examples/memfs"
	"github.com/sandgorgon/9p/server"
)

// TestDialUnixBindsIntoNamespace exercises the kyu-visible half of 9sh#2:
// dial("/path/to.sock") — no TLS, no 9auth identity, exactly the trust
// tier a locally-run 9P server like 9ed sits in — bound at /n/local9p and
// then walked/read through the namespace exactly like any other bind.
// remote_test.go's TestDialUnixEndToEnd already covers remote.Dial's own
// dispatch logic directly; this confirms biDial and evalBindStmt carry it
// through unchanged, the same way TestAtHostEndToEnd does for the TCP
// path.
func TestDialUnixBindsIntoNamespace(t *testing.T) {
	fs := memfs.New()
	if err := writeMemFile(fs, "greeting", []byte("hello from 9ed\n")); err != nil {
		t.Fatalf("seeding memfs: %v", err)
	}

	sockPath := filepath.Join(t.TempDir(), "9ed.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	srv := &server.Server{FS: fs}
	go srv.Serve(l)

	env, _ := jobsEnvWithManager(t)

	src := `h := dial("` + sockPath + `")
bind h, /n/local9p`
	v := runEnv(t, src, env)
	if v.Kind() == "error" {
		t.Fatalf("bind failed: %s", v.String())
	}

	ctx := context.Background()
	root, err := env.Namespace().Attach(ctx, "test", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := root.Walk(ctx, "n")
	if err != nil {
		t.Fatalf("walk n: %v", err)
	}
	f, err = f.Walk(ctx, "local9p")
	if err != nil {
		t.Fatalf("walk n/local9p: %v", err)
	}
	f, err = f.Walk(ctx, "greeting")
	if err != nil {
		t.Fatalf("walk local9p/greeting: %v", err)
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	got, err := readAllFile(ctx, f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello from 9ed\n" {
		t.Fatalf("got %q", got)
	}
}

// writeMemFile mirrors remote_test.go's helper of the same name; kyu/eval
// doesn't otherwise import memfs, so it isn't worth sharing across
// packages for one helper.
func writeMemFile(fs server.FileSystem, name string, content []byte) error {
	ctx := context.Background()
	root, err := fs.Attach(ctx, "test", "")
	if err != nil {
		return err
	}
	f, err := root.Create(ctx, name, 0644, p9.OWRITE)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(ctx, 0, content)
	return err
}
