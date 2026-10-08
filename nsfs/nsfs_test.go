package nsfs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/ns"
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

func TestBindsFSFiles(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFSSpec(&memFS{name: "a"}, "", "/work", ns.Replace, `dir("/x")`)
	if err := n.BindFS(NewBindsFS(n), "", "/ns", ns.Replace); err != nil {
		t.Fatal(err)
	}
	root, _ := n.Attach(ctx, "u", "")

	txt := mustWalk(t, ctx, root, "ns", "binds")
	if err := txt.Open(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := readAll(t, ctx, txt), "bind dir(\"/x\"), /work\n# bind <builtin>, /ns\n"; got != want {
		t.Fatalf("binds = %q, want %q", got, want)
	}

	js := mustWalk(t, ctx, root, "ns", "binds.json")
	if err := js.Open(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var got []ns.Bind
	if err := json.Unmarshal([]byte(readAll(t, ctx, js)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Dst != "/work" || got[1].Dst != "/ns" {
		t.Fatalf("binds.json = %#v", got)
	}
}

func TestBindsFSSnapshotAtOpenAndReadOnly(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(NewBindsFS(n), "", "/ns", ns.Replace)
	root, _ := n.Attach(ctx, "u", "")

	f := mustWalk(t, ctx, root, "ns", "binds")
	if err := f.Open(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// A bind after Open must not change what this reader sees.
	n.BindFSSpec(&memFS{name: "a"}, "", "/late", ns.Replace, `dir("/l")`)
	if got, want := readAll(t, ctx, f), "# bind <builtin>, /ns\n"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}

	w := mustWalk(t, ctx, root, "ns", "binds")
	if err := w.Open(ctx, 1); err == nil { // OWRITE
		t.Fatal("opening binds for write should fail")
	}
	if _, err := w.Write(ctx, 0, []byte("x")); err == nil {
		t.Fatal("writing binds should fail")
	}
	if _, err := mustWalk(t, ctx, root, "ns").Walk(ctx, "nope"); err == nil {
		t.Fatal("walking a missing name in /ns should fail")
	}
}

func TestBindsFSLogFiles(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(NewBindsFS(n), "", "/ns", ns.Replace)
	n.BindFSSpec(&memFS{name: "a"}, "", "/w", ns.Replace, `dir("/x")`)
	n.Unbind("/w")
	root, _ := n.Attach(ctx, "u", "")

	txt := mustWalk(t, ctx, root, "ns", "log")
	txt.Open(ctx, 0)
	lines := strings.Split(strings.TrimSpace(readAll(t, ctx, txt)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "# bind <builtin>, /ns") ||
		!strings.HasPrefix(lines[1], `bind dir("/x"), /w`) || !strings.HasPrefix(lines[2], "unbind /w") {
		t.Fatalf("log text = %q", lines)
	}

	js := mustWalk(t, ctx, root, "ns", "log.json")
	js.Open(ctx, 0)
	var doc struct {
		Dropped uint64        `json:"dropped"`
		Entries []ns.LogEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(readAll(t, ctx, js)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Dropped != 0 || len(doc.Entries) != 3 || doc.Entries[2].Op != "unbind" {
		t.Fatalf("log.json = %#v", doc)
	}
}

func TestFormatBindsIsReplayableKyu(t *testing.T) {
	binds := []ns.Bind{
		{Dst: "/boot", Src: "", Disp: "replace"},
		{Dst: "/work", Src: `dir("/x")`, Disp: "replace"},
		{Dst: "/alias", Src: "/work", Disp: "replace"},
		{Dst: "/u", Src: "/boot", Disp: "replace"},
		{Dst: "/u", Src: "/boot", Disp: "after"},
		{Dst: "/ro", Src: "/work", Disp: "replace", RO: true},
	}
	want := "# bind <builtin>, /boot\n" +
		"bind dir(\"/x\"), /work\n" +
		"bind /work, /alias\n" +
		"bind /boot, /u\n" +
		"bind /boot, /u, after\n" +
		"bind /work, /ro, ro\n"
	if got := FormatBinds(binds); got != want {
		t.Fatalf("FormatBinds =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatLogKeepsDispositionsTimesAndDrops(t *testing.T) {
	entries := []ns.LogEntry{
		{Seq: 1, Time: "2026-01-02T03:04:01Z", Op: "bind", Dst: "/boot", Disp: "replace"},
		{Seq: 2, Time: "2026-01-02T03:04:02Z", Op: "bind", Dst: "/work", Src: `dir("/x")`, Disp: "replace"},
		{Seq: 3, Time: "2026-01-02T03:04:03Z", Op: "bind", Dst: "/u", Src: "/work + /boot", Disp: "replace"},
		{Seq: 4, Time: "2026-01-02T03:04:04Z", Op: "bind", Dst: "/u", Src: "/boot", Disp: "before"},
		{Seq: 5, Time: "2026-01-02T03:04:05Z", Op: "bind", Dst: "/ro2", Src: `dir("/x")`, Disp: "after", RO: true},
		{Seq: 6, Time: "2026-01-02T03:04:06Z", Op: "unbind", Dst: "/work"},
	}
	want := "# bind <builtin>, /boot  # 2026-01-02T03:04:01Z\n" +
		"bind dir(\"/x\"), /work  # 2026-01-02T03:04:02Z\n" +
		"bind /work + /boot, /u  # 2026-01-02T03:04:03Z\n" +
		"bind /boot, /u, before  # 2026-01-02T03:04:04Z\n" +
		"bind dir(\"/x\"), /ro2, after, ro  # 2026-01-02T03:04:05Z\n" +
		"unbind /work  # 2026-01-02T03:04:06Z\n"
	if got := FormatLog(entries, 0); got != want {
		t.Fatalf("FormatLog =\n%s\nwant\n%s", got, want)
	}
	if got := FormatLog(entries[:1], 5); !strings.HasPrefix(got, "# 5 earlier entries dropped\n") {
		t.Fatalf("FormatLog should say what was dropped, got %q", got)
	}
}

// A cloned namespace's /ns must describe the clone, not the original.
func TestCloneRebindsBindsFS(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(NewBindsFS(n), "", "/ns", ns.Replace)
	n.BindFS(&memFS{name: "a"}, "", "/x", ns.Replace)
	// Touch it first, so the original layer has already resolved its root.
	root, _ := n.Attach(ctx, "u", "")
	mustWalk(t, ctx, root, "ns", "binds")

	c := n.Clone()
	c.BindFS(&memFS{name: "b"}, "", "/clone-only", ns.Replace)

	read := func(x *ns.Namespace) string {
		root, _ := x.Attach(ctx, "u", "")
		f := mustWalk(t, ctx, root, "ns", "binds")
		f.Open(ctx, p9.OREAD)
		return readAll(t, ctx, f)
	}
	if got := read(c); !strings.Contains(got, "/clone-only") {
		t.Errorf("clone's /ns/binds should describe the clone:\n%s", got)
	}
	if got := read(n); strings.Contains(got, "/clone-only") {
		t.Errorf("original's /ns/binds leaked the clone's bind:\n%s", got)
	}
}

func TestNewUsesTheShellIdentity(t *testing.T) {
	ctx := context.Background()
	root, _ := New().Attach(ctx, "u", "")
	st, err := root.Stat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Uid != User {
		t.Errorf("root Uid = %q, want %q", st.Uid, User)
	}
}
