package ns

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/9p/server"
)

func devOf(t *testing.T, ctx context.Context, f server.File) uint32 {
	t.Helper()
	st, err := f.Stat(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return st.Dev
}

func devsByName(t *testing.T, ctx context.Context, dir server.File) map[string]uint32 {
	t.Helper()
	entries, err := ReadDirEntries(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDirEntries: %v", err)
	}
	out := map[string]uint32{}
	for _, e := range entries {
		out[e.Name] = e.Dev
	}
	return out
}

// Every layer stamps its own id, in a bind-point listing and in a Stat of a
// walked file alike; synthetic tree directories carry none.
func TestLayersStampTheirDev(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(&memFS{name: "a", content: "A"}, "", "/u", Replace)
	n.BindFS(&memFS{name: "b", content: "B"}, "", "/u", After)
	n.BindFS(&memFS{name: "x"}, "", "/n/h", Replace)
	binds := n.Binds()
	devA, devB, devH := binds[0].Dev, binds[1].Dev, binds[2].Dev
	if devA == 0 || devB == 0 || devA == devB || devH == devA || devH == devB {
		t.Fatalf("layer ids should be distinct and nonzero, got %d %d %d", devA, devB, devH)
	}

	root, _ := n.Attach(ctx, "u", "")
	got := devsByName(t, ctx, mustWalk(t, ctx, root, "u"))
	if got["a"] != devA || got["b"] != devB {
		t.Errorf("union listing devs = %v, want a=%d b=%d", got, devA, devB)
	}
	if d := devOf(t, ctx, mustWalk(t, ctx, root, "u", "b")); d != devB {
		t.Errorf("Stat(/u/b).Dev = %d, want %d", d, devB)
	}
	if got := devsByName(t, ctx, root); got["u"] != 0 || got["n"] != 0 {
		t.Errorf("synthetic tree directories should have dev 0, got %v", got)
	}
	if d := devOf(t, ctx, mustWalk(t, ctx, root, "n")); d != 0 {
		t.Errorf("Stat(/n).Dev = %d, want 0 (tree node)", d)
	}
}

// A directory below a bind point is served by one layer, and everything in
// it — read as a listing or stat'd — carries that layer's id, even though
// the listing is a raw pass-through of the backend's own directory read.
func TestNestedDirectoriesStayStamped(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "f.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("y"), 0644)
	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := New()
	ctx := context.Background()
	n.BindFS(fs, "", "/d", Replace)
	dev := n.Binds()[0].Dev

	root, _ := n.Attach(ctx, "u", "")
	for _, path := range [][]string{{"d"}, {"d", "sub"}} {
		for name, got := range devsByName(t, ctx, mustWalk(t, ctx, root, path...)) {
			if got != dev {
				t.Errorf("%v/%s: dev = %d, want %d", path, name, got, dev)
			}
		}
	}
	if d := devOf(t, ctx, mustWalk(t, ctx, root, "d", "sub", "f.txt")); d != dev {
		t.Errorf("Stat(/d/sub/f.txt).Dev = %d, want %d", d, dev)
	}
	// A file's content is not a directory read and must come through untouched.
	f := mustWalk(t, ctx, root, "d", "top.txt")
	if err := f.Open(ctx, p9.OREAD); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, ctx, f); got != "y" {
		t.Errorf("file content = %q, want %q", got, "y")
	}
}

// Plan 9's bind doesn't change which server a file lives on, so a layer
// made by binding an existing path adds no id of its own: its files keep
// the id of the layer that really serves them.
func TestPathBindKeepsSourceDev(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(&memFS{name: "a", content: "A"}, "", "/src", Replace)
	if err := n.BindPath(ctx, []string{"/src"}, "/alias", Replace); err != nil {
		t.Fatal(err)
	}
	dev := n.Binds()[0].Dev
	if got := n.Binds()[1].Dev; got != 0 {
		t.Errorf("Binds() reports dev %d for a path bind, want 0", got)
	}

	root, _ := n.Attach(ctx, "u", "")
	if got := devsByName(t, ctx, mustWalk(t, ctx, root, "alias")); got["a"] != dev {
		t.Errorf("alias listing dev = %v, want a=%d", got, dev)
	}
	if d := devOf(t, ctx, mustWalk(t, ctx, root, "alias", "a")); d != dev {
		t.Errorf("Stat(/alias/a).Dev = %d, want %d", d, dev)
	}
	res, err := n.Resolve(ctx, "/alias/a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dev != dev {
		t.Errorf("Resolve(/alias/a).Dev = %d, want the serving layer's %d", res.Dev, dev)
	}
}

// Ids are never reused: rebinding after an unbind must not hand a new layer
// a stale id that an old listing may still hold.
func TestDevIsNotReusedAfterUnbind(t *testing.T) {
	n := New()
	n.BindFS(&memFS{name: "a"}, "", "/x", Replace)
	old := n.Binds()[0].Dev
	if err := n.Unbind("/x"); err != nil {
		t.Fatal(err)
	}
	n.BindFS(&memFS{name: "a"}, "", "/x", Replace)
	if got := n.Binds()[0].Dev; got == old {
		t.Errorf("rebound layer reused dev %d", got)
	}
}

// Nothing leaves the namespace stamped: the ids are this process's own.
func TestUnstampedZeroesDevOnTheWayOut(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "f.txt"), []byte("x"), 0644)
	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := New()
	ctx := context.Background()
	n.BindFS(fs, "", "/d", Replace)

	// Sanity: the namespace itself is stamped.
	inner, _ := n.Attach(ctx, "u", "")
	if devOf(t, ctx, mustWalk(t, ctx, inner, "d", "sub")) == 0 {
		t.Fatal("precondition: namespace file should be stamped")
	}

	root, err := Unstamped(n.Subtree("/", false)).Attach(ctx, "u", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range [][]string{{"d"}, {"d", "sub"}} {
		for name, got := range devsByName(t, ctx, mustWalk(t, ctx, root, path...)) {
			if got != 0 {
				t.Errorf("%v/%s exported with dev %d, want 0", path, name, got)
			}
		}
	}
	if d := devOf(t, ctx, mustWalk(t, ctx, root, "d", "sub", "f.txt")); d != 0 {
		t.Errorf("exported Stat dev = %d, want 0", d)
	}
}

// setDirDev patches whole entries in place, in both wire flavors, and
// leaves a truncated trailing entry alone.
func TestSetDirDev(t *testing.T) {
	for _, unix := range []bool{false, true} {
		a := p9.Stat{Name: "a", Uid: "u", Gid: "g", Muid: "m", Extension: "ext"}
		b := p9.Stat{Name: "longer-name", Uid: "u", Gid: "g", Muid: "m", Dev: 99}
		blob := append(a.MarshalVersion(unix), b.MarshalVersion(unix)...)
		full := len(blob)
		blob = append(blob, b.MarshalVersion(unix)[:5]...) // partial third entry
		orig := append([]byte(nil), blob[full:]...)

		setDirDev(blob, 7)

		la := len(a.MarshalVersion(unix))
		for i, want := range []struct {
			off int
			end int
		}{{0, la}, {la, full}} {
			st, err := p9.UnmarshalStatVersion(blob[want.off:want.end], unix)
			if err != nil {
				t.Fatalf("unix=%v entry %d: %v", unix, i, err)
			}
			if st.Dev != 7 {
				t.Errorf("unix=%v entry %d: Dev = %d, want 7", unix, i, st.Dev)
			}
		}
		if string(blob[full:]) != string(orig) {
			t.Errorf("unix=%v: truncated trailing entry was modified", unix)
		}
	}
}
