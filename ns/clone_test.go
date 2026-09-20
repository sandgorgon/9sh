package ns

import (
	"context"
	"reflect"
	"strings"
	"testing"

	p9 "github.com/sandgorgon/9p"
)

func dsts(n *Namespace) []string {
	var out []string
	for _, b := range n.Binds() {
		out = append(out, b.Dst)
	}
	return out
}

func TestCloneIsIndependentBothWays(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(&memFS{name: "a", content: "A"}, "", "/one", Replace)
	n.BindFS(&memFS{name: "b", content: "B"}, "", "/two", Replace)
	c := n.Clone()

	if got, want := dsts(c), []string{"/one", "/two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("clone binds = %v, want %v", got, want)
	}
	// Everything already bound is reachable in the clone.
	root, _ := c.Attach(ctx, "u", "")
	f := mustWalk(t, ctx, root, "one", "a")
	f.Open(ctx, p9.OREAD)
	if got := readAll(t, ctx, f); got != "A" {
		t.Fatalf("read through clone = %q", got)
	}

	// A bind and an unbind in the clone don't touch the original...
	c.BindFS(&memFS{name: "c"}, "", "/only-clone", Replace)
	if err := c.Unbind("/two"); err != nil {
		t.Fatal(err)
	}
	if got, want := dsts(n), []string{"/one", "/two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("original after clone changes = %v, want %v", got, want)
	}
	// ...and the original's later binds don't appear in the clone.
	n.BindFS(&memFS{name: "d"}, "", "/only-orig", Replace)
	if got, want := dsts(c), []string{"/one", "/only-clone"}; !reflect.DeepEqual(got, want) {
		t.Errorf("clone after original changes = %v, want %v", got, want)
	}
}

// A `bind /work, /alias` layer captured a live tree node; inside the
// clone it must follow the clone's /work, not the original's.
func TestCloneRemapsLayersThatReferenceTheTree(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(&memFS{name: "old", content: "1"}, "", "/work", Replace)
	if err := n.BindPath(ctx, []string{"/work"}, "/alias", Replace); err != nil {
		t.Fatal(err)
	}
	c := n.Clone()
	c.BindFS(&memFS{name: "new", content: "2"}, "", "/work", Replace)

	viaAlias := func(ns *Namespace, name string) bool {
		root, _ := ns.Attach(ctx, "u", "")
		f, err := walkAll(ctx, root, []string{"alias", name})
		return err == nil && f != nil
	}
	if !viaAlias(c, "new") || viaAlias(c, "old") {
		t.Error("clone's /alias should follow the clone's rebound /work")
	}
	if !viaAlias(n, "old") || viaAlias(n, "new") {
		t.Error("original's /alias must not see the clone's rebind of /work")
	}
}

func TestCloneRebindsNamespaceHoldingFilesystems(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(NewBindsFS(n), "", "/ns", Replace)
	n.BindFS(&memFS{name: "a"}, "", "/x", Replace)
	// Touch it first, so the original layer has already resolved its root.
	root, _ := n.Attach(ctx, "u", "")
	mustWalk(t, ctx, root, "ns", "binds")

	c := n.Clone()
	c.BindFS(&memFS{name: "b"}, "", "/clone-only", Replace)

	read := func(ns *Namespace) string {
		root, _ := ns.Attach(ctx, "u", "")
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

func TestCloneKeepsReadOnlyAndCarriesTheLog(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(&memFS{name: "f", content: "data"}, "", "/rw", Replace)
	n.BindPathOpts(ctx, []string{"/rw"}, "/ro", Replace, BindOpts{ReadOnly: true})
	c := n.Clone()

	root, _ := c.Attach(ctx, "u", "")
	if err := mustWalk(t, ctx, root, "ro", "f").Open(ctx, p9.OWRITE); err == nil {
		t.Error("a read-only bind must stay read-only in the clone")
	}
	if err := mustWalk(t, ctx, root, "rw", "f").Open(ctx, p9.OWRITE); err != nil {
		t.Errorf("the rw bind should stay writable in the clone: %v", err)
	}

	c.Unbind("/rw")
	entries, _ := c.Log()
	origEntries, _ := n.Log()
	if len(entries) != len(origEntries)+1 || entries[len(entries)-1].Op != "unbind" {
		t.Errorf("clone log = %d entries (orig %d); want orig's history plus its own unbind", len(entries), len(origEntries))
	}
	if len(origEntries) != 2 {
		t.Errorf("original's log changed: %d entries", len(origEntries))
	}
}
