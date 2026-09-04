package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/examples/dirfs"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func TestJoinPathAppendsSegments(t *testing.T) {
	v := run(t, `join_path(/local/project, "sub", "deeper")`)
	if v.(value.Path) != "/local/project/sub/deeper" {
		t.Errorf("got %v, want /local/project/sub/deeper", v)
	}
}

func TestJoinPathNoSegmentsReturnsBaseUnchanged(t *testing.T) {
	v := run(t, `join_path(/local/project)`)
	if v.(value.Path) != "/local/project" {
		t.Errorf("got %v, want /local/project", v)
	}
}

func TestJoinPathCleansDotDot(t *testing.T) {
	v := run(t, `join_path(/local/project, "..", "other")`)
	if v.(value.Path) != "/local/other" {
		t.Errorf("got %v, want /local/other", v)
	}
}

func TestJoinPathSegmentMayContainSlashes(t *testing.T) {
	v := run(t, `join_path(/local/project, "sub/deeper")`)
	if v.(value.Path) != "/local/project/sub/deeper" {
		t.Errorf("got %v, want /local/project/sub/deeper", v)
	}
}

func TestJoinPathRejectsNonPathBase(t *testing.T) {
	runErr(t, `join_path("/local/project", "sub")`)
}

func TestJoinPathRejectsNonStringSegment(t *testing.T) {
	runErr(t, `join_path(/local/project, 5)`)
}

func TestJoinPathRejectsNoArguments(t *testing.T) {
	runErr(t, `join_path()`)
}

// TestJoinPathResultUsableAsBindSource confirms join_path's Path result
// works exactly like a hand-typed literal as bind's SRC — walked
// through a real bound directory (/testdir/sub), not just checked at
// the value level like the tests above.
func TestJoinPathResultUsableAsBindSource(t *testing.T) {
	env := jobsEnv(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "greeting"), []byte("hello from join_path\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	if err := env.Namespace().BindFS(fs, "", "/testdir", ns.Replace); err != nil {
		t.Fatalf("bind /testdir: %v", err)
	}

	src := `work := /testdir
bind join_path(work, "sub"), /x`
	v := runEnv(t, src, env)
	if v.Kind() == "error" {
		t.Fatalf("bind failed: %s", v.String())
	}

	ctx := context.Background()
	root, err := env.Namespace().Attach(ctx, "test", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := root.Walk(ctx, "x")
	if err != nil {
		t.Fatalf("walk x: %v", err)
	}
	f, err = f.Walk(ctx, "greeting")
	if err != nil {
		t.Fatalf("walk x/greeting: %v", err)
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	got, err := readAllFile(ctx, f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello from join_path\n" {
		t.Fatalf("got %q", got)
	}
}
