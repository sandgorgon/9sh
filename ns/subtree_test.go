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

// subtreeNS binds a real directory tree at /work and a "secret" one
// beside it, so a served /work has something outside it to leak.
func subtreeNS(t *testing.T) *Namespace {
	t.Helper()
	work, secret := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(work, "sub", "deep"), 0755)
	os.WriteFile(filepath.Join(work, "top.txt"), []byte("top"), 0644)
	os.WriteFile(filepath.Join(work, "sub", "s.txt"), []byte("sub"), 0644)
	os.WriteFile(filepath.Join(secret, "key.txt"), []byte("hunter2"), 0644)
	n := New()
	for path, dir := range map[string]string{"/work": work, "/secret": secret} {
		fs, err := dirfs.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := n.BindFS(fs, "", path, Replace); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func attach(t *testing.T, fs server.FileSystem) server.File {
	t.Helper()
	root, err := fs.Attach(context.Background(), "peer", "")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return root
}

func TestSubtreeServesOnlyThePath(t *testing.T) {
	ctx := context.Background()
	root := attach(t, subtreeNS(t).Subtree("/work", false))

	f := mustWalk(t, ctx, root, "top.txt")
	f.Open(ctx, p9.OREAD)
	if got := readAll(t, ctx, f); got != "top" {
		t.Fatalf("top.txt = %q", got)
	}
	// The served root is its own parent, and the sibling bind is unreachable.
	up := mustWalk(t, ctx, root, "..")
	if _, err := up.Walk(ctx, "secret"); err == nil {
		t.Fatal("'..' from the served root reached a sibling bind")
	}
	if _, err := root.Walk(ctx, "secret"); err == nil {
		t.Fatal("the served root exposes /secret")
	}
	names := map[string]bool{}
	ents, err := ReadDirEntries(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		names[e.Name] = true
	}
	if !names["top.txt"] || !names["sub"] || names["secret"] || names["work"] {
		t.Fatalf("root listing = %v", names)
	}
}

func TestSubtreeDotDotCannotClimbPastTheRoot(t *testing.T) {
	ctx := context.Background()
	// Served root is /work/sub, whose real parent (/work) holds top.txt.
	root := attach(t, subtreeNS(t).Subtree("/work/sub", false))

	if _, err := root.Walk(ctx, "top.txt"); err == nil {
		t.Fatal("served root /work/sub can see its parent's top.txt")
	}
	// Down two levels, then up more times than we went down.
	f := mustWalk(t, ctx, root, "deep")
	for i := 0; i < 5; i++ {
		var err error
		if f, err = f.Walk(ctx, ".."); err != nil {
			t.Fatalf("'..' #%d: %v", i, err)
		}
	}
	if _, err := f.Walk(ctx, "top.txt"); err == nil {
		t.Fatal("climbed above the served root via repeated '..'")
	}
	if g, err := f.Walk(ctx, "s.txt"); err != nil {
		t.Fatalf("after climbing back to the root, its own file should resolve: %v", err)
	} else {
		g.Open(ctx, p9.OREAD)
		if got := readAll(t, ctx, g); got != "sub" {
			t.Fatalf("s.txt = %q", got)
		}
	}
}

func TestSubtreeReadOnlyRefusesWrites(t *testing.T) {
	ctx := context.Background()
	root := attach(t, subtreeNS(t).Subtree("/work", true))

	f := mustWalk(t, ctx, root, "top.txt")
	if err := f.Open(ctx, p9.OREAD); err != nil {
		t.Fatal(err)
	}
	if err := mustWalk(t, ctx, root, "top.txt").Open(ctx, p9.OWRITE); err == nil {
		t.Error("open for write on a read-only subtree should fail")
	}
	if _, err := root.Create(ctx, "new.txt", 0644, p9.OWRITE); err == nil {
		t.Error("create on a read-only subtree should fail")
	}
	// ".." at the root must not hand back an unwrapped, writable root.
	up := mustWalk(t, ctx, root, "..")
	if _, err := up.Create(ctx, "new.txt", 0644, p9.OWRITE); err == nil {
		t.Error("'..' from a read-only root escaped the read-only wrapper")
	}
	// Nor may a file reached after descending and climbing back.
	down := mustWalk(t, ctx, root, "sub", "..", "top.txt")
	if err := down.Open(ctx, p9.OWRITE); err == nil {
		t.Error("write open via sub/../top.txt on a read-only subtree should fail")
	}
}

func TestSubtreeResolvesLazilyAndRejectsNonDirectories(t *testing.T) {
	ctx := context.Background()
	n := subtreeNS(t)
	fs := n.Subtree("/later", false)
	if _, err := fs.Attach(ctx, "peer", ""); err == nil {
		t.Fatal("Attach of an unbound path should fail")
	}
	// Bound after the Subtree was created — the startup-configs case.
	n.BindFS(&memFS{name: "f", content: "x"}, "", "/later", Replace)
	if _, err := fs.Attach(ctx, "peer", ""); err != nil {
		t.Fatalf("Attach after the path is bound: %v", err)
	}
	if _, err := n.Subtree("/work/top.txt", false).Attach(ctx, "peer", ""); err == nil {
		t.Fatal("serving a plain file should be refused")
	}
}
