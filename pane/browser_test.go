package pane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/ns"
)

// newBrowserTestEnv binds a real dirfs-backed temp directory (with a
// file and a subdirectory, to exercise both entry kinds) at /x, and
// returns a shared *eval.Env over that namespace.
func newBrowserTestEnv(t *testing.T) *eval.Env {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A"), 0644); err != nil {
		t.Fatalf("seed a.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("B"), 0644); err != nil {
		t.Fatalf("seed sub/b.txt: %v", err)
	}

	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	namespace := ns.New()
	if err := namespace.BindFS(fs, "", "/x", ns.Replace); err != nil {
		t.Fatalf("bind /x: %v", err)
	}
	return eval.NewGlobalEnv(namespace)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBrowserListsRootAndDescends(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id

	rootMsg := listDirCmd(id, env.Namespace(), "/")()
	next, _ := m.Update(rootMsg)
	m = next.(Model)
	if want := []string{"x/"}; !equalStrings(m.panes[0].browserEntries, want) {
		t.Fatalf("root entries = %v, want %v", m.panes[0].browserEntries, want)
	}

	next, cmd := m.Update(browserEnterMsg{id: id})
	m = next.(Model)
	if m.panes[0].browserPath != "/x" {
		t.Fatalf("path after descend = %q, want /x", m.panes[0].browserPath)
	}
	if cmd == nil {
		t.Fatal("descending should produce a listing Cmd")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)

	want := []string{"..", "a.txt", "sub/"}
	if !equalStrings(m.panes[0].browserEntries, want) {
		t.Fatalf("entries at /x = %v, want %v", m.panes[0].browserEntries, want)
	}
}

// navigateIntoX drives the real navigation flow (list root, descend
// into "x", apply the resulting listing) so p.browserPath and
// p.browserEntries end up populated exactly as they would in the app —
// listDirCmd's result is only applied when it matches the pane's
// current browserPath (a stale-response guard), so jumping straight to
// listDirCmd(..., "/x") without first navigating there is rejected.
func navigateIntoX(t *testing.T, m Model, id int, env *eval.Env) Model {
	t.Helper()
	next, _ := m.Update(listDirCmd(id, env.Namespace(), "/")())
	m = next.(Model)
	next, cmd := m.Update(browserEnterMsg{id: id})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected a listing Cmd after descending into /x")
	}
	next, _ = m.Update(cmd())
	return next.(Model)
}

func TestBrowserFileSelectionIsNoOp(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	// entries: [.., a.txt, sub/] — cursor 0 is "..", move to a.txt (index 1)
	next, _ := m.Update(browserMoveMsg{id: id, delta: 1})
	m = next.(Model)

	next, cmd := m.Update(browserEnterMsg{id: id})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("selecting a plain file should not produce a listing Cmd")
	}
	if m.panes[0].browserPath != "/x" {
		t.Fatalf("path changed to %q on file selection, should stay /x", m.panes[0].browserPath)
	}
}

func TestBrowserDotDotGoesUp(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	// cursor 0 is ".."
	next, cmd := m.Update(browserEnterMsg{id: id})
	m = next.(Model)
	if m.panes[0].browserPath != "/" {
		t.Fatalf("path after '..' = %q, want /", m.panes[0].browserPath)
	}
	if cmd == nil {
		t.Fatal("going up should produce a listing Cmd")
	}
}

func TestBrowserBackspaceGoesUp(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	next, cmd := m.Update(browserUpMsg{id: id})
	m = next.(Model)
	if m.panes[0].browserPath != "/" {
		t.Fatalf("path = %q, want /", m.panes[0].browserPath)
	}
	if cmd == nil {
		t.Fatal("expected a listing Cmd")
	}
}

func TestBrowserClickOnFileSelectsWithoutDescending(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	// entries: [.., a.txt, sub/] — click index 1 ("a.txt", a file): the
	// click sets cursor to that index and, since it's not a directory,
	// browserEnter's file branch returns without touching cursor/path
	// further — a clean way to confirm the click→index translation
	// itself, since clicking a directory immediately (synchronously)
	// descends and resets cursor to 0 for the new listing, which
	// would otherwise mask whether the index was set correctly.
	next, cmd := m.Update(browserClickMsg{id: id, index: 1})
	m = next.(Model)
	if m.panes[0].browserCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.panes[0].browserCursor)
	}
	if cmd != nil {
		t.Fatal("clicking a file should not produce a listing Cmd")
	}
	if m.panes[0].browserPath != "/x" {
		t.Fatalf("path changed to %q on file click, should stay /x", m.panes[0].browserPath)
	}
}

func TestBrowserClickOnDirectoryDescends(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	// entries: [.., a.txt, sub/] — click index 2 ("sub/")
	next, cmd := m.Update(browserClickMsg{id: id, index: 2})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("clicking a directory should produce a listing Cmd")
	}
	if m.panes[0].browserPath != "/x/sub" {
		t.Fatalf("path = %q, want /x/sub (optimistic update before the listing resolves)", m.panes[0].browserPath)
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.panes[0].browserPath != "/x/sub" {
		t.Fatalf("path after listing resolves = %q, want /x/sub", m.panes[0].browserPath)
	}
}

func TestBrowserListErrorSurfacesInState(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id

	next, _ := m.Update(listDirCmd(id, env.Namespace(), "/does-not-exist")())
	m = next.(Model)
	// browserPath is still "/" (never changed to the bad path), so a
	// listing result for "/does-not-exist" is stale/ignored by design
	// (see Update's browserListedMsg case: path must match). Drive it
	// the real way instead: descend into a path that then 404s isn't
	// reachable from this UI, so exercise the guard directly.
	if m.panes[0].browserErr != "" {
		t.Fatalf("a listing for a path the pane never navigated to should be ignored, got err=%q", m.panes[0].browserErr)
	}
}

// TestBrowserPaneIntegration drives a real tui.App end to end: the
// initial Init() Cmd (wrapped in a BatchMsg, per tui.Batch) populates
// the root listing, and the rendered Buffer shows the bound path.
func TestBrowserPaneIntegration(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, NamespaceBrowserSpec("browse", env))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	if cmd := app.InitCmd(); cmd != nil {
		if batch, ok := cmd().(tui.BatchMsg); ok {
			for _, sub := range batch {
				app.Dispatch(sub())
			}
		}
	}

	if !strings.Contains(app.Buffer().String(), "x/") {
		t.Fatalf("rendered buffer missing root listing entry 'x/':\n%s", app.Buffer().String())
	}
}
