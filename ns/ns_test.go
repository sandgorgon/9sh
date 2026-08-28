package ns

import (
	"context"
	"errors"
	"io"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

// memFS is a tiny single-directory, single-file server.FileSystem used
// only to exercise bind/union without pulling in a real backend.
type memFS struct {
	name    string // the one file this fs exports at its root
	content string
}

func (m *memFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	return &memRoot{m: m}, nil
}

type memRoot struct{ m *memFS }

func (r *memRoot) Qid() p9.Qid { return p9.Qid{Type: p9.QTDIR, Path: 1} }
func (r *memRoot) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: r.Qid(), Mode: p9.DMDIR | 0755, Name: "/"}, nil
}
func (r *memRoot) WStat(ctx context.Context, st p9.Stat) error { return errors.New("unsupported") }
func (r *memRoot) Walk(ctx context.Context, name string) (server.File, error) {
	if name != r.m.name {
		return nil, errors.New("no such file")
	}
	return &memLeaf{m: r.m}, nil
}
func (r *memRoot) Open(ctx context.Context, mode p9.Mode) error { return nil }
func (r *memRoot) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, errors.New("unsupported")
}
func (r *memRoot) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	entries := []p9.Stat{{Qid: p9.Qid{Type: p9.QTFILE, Path: 2}, Mode: 0644, Name: r.m.name}}
	return server.MarshalDir(entries, offset, p)
}
func (r *memRoot) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("unsupported")
}
func (r *memRoot) Remove(ctx context.Context) error { return errors.New("unsupported") }
func (r *memRoot) Close() error                     { return nil }

type memLeaf struct{ m *memFS }

func (l *memLeaf) Qid() p9.Qid { return p9.Qid{Type: p9.QTFILE, Path: 2} }
func (l *memLeaf) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: l.Qid(), Mode: 0644, Name: l.m.name, Length: uint64(len(l.m.content))}, nil
}
func (l *memLeaf) WStat(ctx context.Context, st p9.Stat) error { return errors.New("unsupported") }
func (l *memLeaf) Walk(ctx context.Context, name string) (server.File, error) {
	return nil, errors.New("not a directory")
}
func (l *memLeaf) Open(ctx context.Context, mode p9.Mode) error { return nil }
func (l *memLeaf) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, errors.New("not a directory")
}
func (l *memLeaf) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	if offset >= int64(len(l.m.content)) {
		return 0, io.EOF
	}
	return copy(p, l.m.content[offset:]), nil
}
func (l *memLeaf) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("unsupported")
}
func (l *memLeaf) Remove(ctx context.Context) error { return errors.New("unsupported") }
func (l *memLeaf) Close() error                     { return nil }

func readAll(t *testing.T, ctx context.Context, f server.File) string {
	t.Helper()
	var out []byte
	buf := make([]byte, 4096)
	var off int64
	for {
		n, err := f.Read(ctx, off, buf)
		out = append(out, buf[:n]...)
		off += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if n == 0 {
			break
		}
	}
	return string(out)
}

func mustWalk(t *testing.T, ctx context.Context, f server.File, parts ...string) server.File {
	t.Helper()
	for _, p := range parts {
		var err error
		f, err = f.Walk(ctx, p)
		if err != nil {
			t.Fatalf("walk %q: %v", p, err)
		}
	}
	return f
}

func TestBindFSAndRead(t *testing.T) {
	ns := New()
	if err := ns.BindFS(&memFS{name: "greeting", content: "hi"}, "", "/msgs", Replace); err != nil {
		t.Fatalf("BindFS: %v", err)
	}
	ctx := context.Background()
	root, err := ns.Attach(ctx, "u", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	f := mustWalk(t, ctx, root, "msgs", "greeting")
	if got := readAll(t, ctx, f); got != "hi" {
		t.Fatalf("content = %q, want hi", got)
	}
}

func TestBindPathReshapesExistingBind(t *testing.T) {
	ns := New()
	ns.BindFS(&memFS{name: "a", content: "AAA"}, "", "/src", Replace)
	ctx := context.Background()

	if err := ns.BindPath(ctx, []string{"/src"}, "/dst", Replace); err != nil {
		t.Fatalf("BindPath: %v", err)
	}
	root, _ := ns.Attach(ctx, "u", "")
	f := mustWalk(t, ctx, root, "dst", "a")
	if got := readAll(t, ctx, f); got != "AAA" {
		t.Fatalf("content via bound dst = %q, want AAA", got)
	}
}

func TestUnionDispositionOrder(t *testing.T) {
	ns := New()
	ctx := context.Background()
	ns.BindFS(&memFS{name: "only-in-first", content: "1"}, "", "/u", Replace)
	ns.BindFS(&memFS{name: "only-in-second", content: "2"}, "", "/u", After)

	root, _ := ns.Attach(ctx, "u", "")
	f1 := mustWalk(t, ctx, root, "u", "only-in-first")
	if got := readAll(t, ctx, f1); got != "1" {
		t.Fatalf("only-in-first = %q, want 1", got)
	}
	f2 := mustWalk(t, ctx, root, "u", "only-in-second")
	if got := readAll(t, ctx, f2); got != "2" {
		t.Fatalf("only-in-second = %q, want 2", got)
	}
}

func TestBeforeWinsOverExistingOnNameCollision(t *testing.T) {
	ns := New()
	ctx := context.Background()
	ns.BindFS(&memFS{name: "x", content: "old"}, "", "/u", Replace)
	ns.BindFS(&memFS{name: "x", content: "new"}, "", "/u", Before)

	root, _ := ns.Attach(ctx, "u", "")
	f := mustWalk(t, ctx, root, "u", "x")
	if got := readAll(t, ctx, f); got != "new" {
		t.Fatalf("Before-bound layer should win on name collision, got %q, want new", got)
	}
}

func TestReplaceDropsPriorLayers(t *testing.T) {
	ns := New()
	ctx := context.Background()
	ns.BindFS(&memFS{name: "gone", content: "x"}, "", "/u", Replace)
	ns.BindFS(&memFS{name: "here", content: "y"}, "", "/u", Replace)

	root, _ := ns.Attach(ctx, "u", "")
	uDir, err := root.Walk(ctx, "u")
	if err != nil {
		t.Fatalf("walk to /u: %v", err)
	}
	if _, err := uDir.Walk(ctx, "gone"); err == nil {
		t.Fatal("Replace should have dropped the earlier layer entirely")
	}
	f, err := uDir.Walk(ctx, "here")
	if err != nil {
		t.Fatalf("walk to here: %v", err)
	}
	if got := readAll(t, ctx, f); got != "y" {
		t.Fatalf("content = %q, want y", got)
	}
}

func TestDirectoryListingMergesTreeAndLayers(t *testing.T) {
	ns := New()
	ctx := context.Background()
	ns.BindFS(&memFS{name: "leaf1", content: "a"}, "", "/mix", Replace)
	// an explicit tree child at the same level, via a nested bind
	ns.BindFS(&memFS{name: "inner", content: "b"}, "", "/mix/sub", Replace)

	root, _ := ns.Attach(ctx, "u", "")
	mix := mustWalk(t, ctx, root, "mix")
	entries, err := readDirEntries(ctx, mix)
	if err != nil {
		t.Fatalf("readDirEntries: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["leaf1"] {
		t.Errorf("listing %v missing layer entry leaf1", names)
	}
	if !names["sub"] {
		t.Errorf("listing %v missing explicit tree child sub", names)
	}
}

func TestUnboundPathErrors(t *testing.T) {
	ns := New()
	ctx := context.Background()
	root, _ := ns.Attach(ctx, "u", "")
	if _, err := root.Walk(ctx, "nothing-here"); err == nil {
		t.Fatal("walking an unbound path should error, not silently succeed")
	}
}

func TestBindPathErrorsOnUnresolvableSource(t *testing.T) {
	ns := New()
	ctx := context.Background()
	if err := ns.BindPath(ctx, []string{"/does/not/exist"}, "/dst", Replace); err == nil {
		t.Fatal("BindPath from an unresolvable source should error")
	}
}
