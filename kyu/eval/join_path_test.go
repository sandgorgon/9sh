package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// Path + String appends a segment, sharing join_path's implementation, so
// `/n + name` builds a computed mount point without a String ever being
// implicitly coerced into a Path.
func TestPathPlusStringAppendsASegment(t *testing.T) {
	cases := []struct{ src, want string }{
		{`/n + "host"`, "/n/host"},
		{`/n + "host" + "jobs"`, "/n/host/jobs"},
		{`/local/project + "sub/deeper"`, "/local/project/sub/deeper"},
		{`/local/project + ".." + "other"`, "/local/other"},
		{`/n + ""`, "/n"},
	}
	for _, tc := range cases {
		v := run(t, tc.src)
		if p, ok := v.(value.Path); !ok || string(p) != tc.want {
			t.Errorf("%s = %#v, want path %s", tc.src, v, tc.want)
		}
	}
}

// TestPathPlusStringMatchesJoinPath: one implementation behind both, so
// they can't disagree.
func TestPathPlusStringMatchesJoinPath(t *testing.T) {
	for _, seg := range []string{"host", "a/b", "..", ".", "", "x/../y", "/abs"} {
		a := run(t, `join_path(/base/dir, "`+seg+`")`)
		b := run(t, `/base/dir + "`+seg+`"`)
		if a != b {
			t.Errorf("segment %q: join_path = %v, + = %v", seg, a, b)
		}
	}
}

// TestPathPlusPathIsStillNamespaceUnion: only a String on the right means
// "a name"; a Path on the right keeps its existing meaning.
func TestPathPlusPathIsStillNamespaceUnion(t *testing.T) {
	v := run(t, `/a + /b`)
	u, ok := v.(value.NSUnion)
	if !ok || len(u.Paths) != 2 || u.Paths[0] != "/a" || u.Paths[1] != "/b" {
		t.Errorf("/a + /b = %#v, want a two-path NSUnion", v)
	}
}

// TestStringPlusPathIsAnErrorWithAHint: the Path goes on the left; a
// String is never implicitly turned into a Path.
func TestStringPlusPathIsAnErrorWithAHint(t *testing.T) {
	err := runErr(t, `"host" + /n`)
	if !strings.Contains(err.Error(), "path on the left") {
		t.Errorf("error %q should say the path goes on the left", err)
	}
}

func TestStringPlusStringStillConcatenates(t *testing.T) {
	if v := run(t, `"a" + "b"`); v != value.String("ab") {
		t.Errorf(`"a" + "b" = %#v, want "ab"`, v)
	}
}
