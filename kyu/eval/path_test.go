package eval

import (
	"context"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestPathConvertsAbsoluteString(t *testing.T) {
	v := run(t, `path("/local/some/dir")`)
	if v.(value.Path) != "/local/some/dir" {
		t.Errorf("got %v, want /local/some/dir", v)
	}
}

// TestPathUsableAsBindSource confirms path(...)'s result behaves
// exactly like a hand-typed Path literal as bind's SRC — walked
// through /jobs (already bound by jobsEnv) into a new location, then
// walked again to confirm the bind actually took.
func TestPathUsableAsBindSource(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `bind path("/jobs"), /x`, env)
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
	if _, err := f.Walk(ctx, "clone"); err != nil {
		t.Fatalf("walk x/clone (should be /jobs/clone via the path(...) bind): %v", err)
	}
}

func TestPathRejectsRelativeString(t *testing.T) {
	runErr(t, `path("relative/dir")`)
}

func TestPathRejectsNonStringArg(t *testing.T) {
	runErr(t, `path(5)`)
}

func TestPathRejectsWrongArgCount(t *testing.T) {
	runErr(t, `path()`)
	runErr(t, `path("/a", "/b")`)
}
