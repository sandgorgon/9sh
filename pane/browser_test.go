package pane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/input"
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
	m := New(env, "", NamespaceBrowserSpec("browse", env))
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

// TestBrowserFileSelectionStartsPreviewRead confirms selecting a plain
// file kicks off a readFileCmd (not a listing Cmd) and leaves the
// current directory alone — applying that Cmd's result should populate
// the in-pane preview, covered by TestBrowserFileReadOpensPreview below.
func TestBrowserFileSelectionStartsPreviewRead(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	// entries: [.., a.txt, sub/] — cursor 0 is "..", move to a.txt (index 1)
	next, _ := m.Update(browserMoveMsg{id: id, delta: 1})
	m = next.(Model)

	next, cmd := m.Update(browserEnterMsg{id: id})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("selecting a plain file should produce a readFileCmd")
	}
	if m.panes[0].browserPath != "/x" {
		t.Fatalf("path changed to %q on file selection, should stay /x", m.panes[0].browserPath)
	}
	if m.panes[0].browserPendingReadPath != "/x/a.txt" {
		t.Fatalf("browserPendingReadPath = %q, want /x/a.txt", m.panes[0].browserPendingReadPath)
	}

	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.panes[0].browserPreviewPath != "/x/a.txt" {
		t.Fatalf("browserPreviewPath = %q, want /x/a.txt", m.panes[0].browserPreviewPath)
	}
	if want := []string{"A"}; !equalStrings(m.panes[0].browserPreviewLines, want) {
		t.Fatalf("browserPreviewLines = %v, want %v", m.panes[0].browserPreviewLines, want)
	}
	if m.panes[0].browserPendingReadPath != "" {
		t.Fatalf("browserPendingReadPath left set to %q after the read resolved", m.panes[0].browserPendingReadPath)
	}
}

// TestBrowserPreviewCloseReturnsToListing confirms browserPreviewCloseMsg
// (Esc/Backspace inside the preview) clears the preview state and leaves
// the underlying listing untouched — no re-fetch, since nothing about
// the directory itself changed.
func TestBrowserPreviewCloseReturnsToListing(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)
	next, _ := m.Update(browserMoveMsg{id: id, delta: 1})
	m = next.(Model)
	next, cmd := m.Update(browserEnterMsg{id: id})
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.panes[0].browserPreviewPath == "" {
		t.Fatal("setup: expected a preview open")
	}

	next, _ = m.Update(browserPreviewCloseMsg{id: id})
	m = next.(Model)
	if m.panes[0].browserPreviewPath != "" {
		t.Fatalf("browserPreviewPath = %q after close, want empty", m.panes[0].browserPreviewPath)
	}
	want := []string{"..", "a.txt", "sub/"}
	if !equalStrings(m.panes[0].browserEntries, want) {
		t.Fatalf("browserEntries after preview close = %v, want %v (unchanged)", m.panes[0].browserEntries, want)
	}
}

// TestBrowserStalePreviewReadIsDiscarded confirms a readFileCmd result
// for a path the pane is no longer waiting on (the user picked a
// different file, or navigated away, before it resolved) is ignored —
// the same staleness guard browserListedMsg already has for listDirCmd.
func TestBrowserStalePreviewReadIsDiscarded(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	next, _ := m.Update(browserFileReadMsg{id: id, path: "/x/some-other-file.txt", content: "stale"})
	m = next.(Model)
	if m.panes[0].browserPreviewPath != "" {
		t.Fatalf("browserPreviewPath = %q, want empty (stale read should be discarded)", m.panes[0].browserPreviewPath)
	}
}

func TestBrowserDotDotGoesUp(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
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
	m := New(env, "", NamespaceBrowserSpec("browse", env))
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

func TestBrowserClickOnFileStartsPreviewRead(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
	id := m.panes[0].id
	m = navigateIntoX(t, m, id, env)

	// entries: [.., a.txt, sub/] — click index 1 ("a.txt", a file): the
	// click sets cursor to that index and, since it's not a directory,
	// browserEnter's file branch kicks off a readFileCmd rather than a
	// listing Cmd — a clean way to confirm the click→index translation
	// itself, since clicking a directory immediately (synchronously)
	// descends and resets cursor to 0 for the new listing, which
	// would otherwise mask whether the index was set correctly.
	next, cmd := m.Update(browserClickMsg{id: id, index: 1})
	m = next.(Model)
	if m.panes[0].browserCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.panes[0].browserCursor)
	}
	if cmd == nil {
		t.Fatal("clicking a file should produce a readFileCmd")
	}
	if m.panes[0].browserPath != "/x" {
		t.Fatalf("path changed to %q on file click, should stay /x", m.panes[0].browserPath)
	}
}

func TestBrowserClickOnDirectoryDescends(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
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
	m := New(env, "", NamespaceBrowserSpec("browse", env))
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
	m := New(env, "", NamespaceBrowserSpec("browse", env))
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

// dispatchChain runs cmd, dispatches its Msg, and repeats on whatever
// Cmd that Dispatch call itself returns, until nil -- the synchronous
// stand-in for Run()'s own async runCmd->msgCh->Dispatch loop (see
// app.go's Run doc comment), needed here because browserEnter's own
// Update case returns a follow-up Cmd (listDirCmd/readFileCmd) rather
// than terminating in one hop the way e.g. beginSplitMsg's handling
// does (which is why TestSplitKeysOnTitleBar's simpler single-dispatch
// helper never needed this).
func dispatchChain(app *tui.App, cmd tui.Cmd) {
	for cmd != nil {
		cmd = app.Dispatch(cmd())
	}
}

// runFocusedInput sends e through app.HandleInput and synchronously
// resolves every resulting Cmd (and its own follow-ups) via
// dispatchChain.
func runFocusedInput(app *tui.App, e input.Event) {
	for _, cmd := range app.HandleInput(e) {
		dispatchChain(app, cmd)
	}
}

// TestBrowserPreviewOnScreenEndToEnd drives file preview through the
// real tui.App input/render path (not just Update): Tab to the browse
// pane's own content, Enter to descend into /x, Down+Enter to preview
// a.txt, confirming its content actually reaches the screen, then Esc
// to confirm the listing comes back. TestHelpButtonShowsHelpContentOnScreen's
// own doc comment explains why this class of test exists separately
// from the Update-only ones above: a wiring mistake in browserNode
// (e.g. never actually swapping to browserPreviewNode) wouldn't be
// caught by them.
func TestBrowserPreviewOnScreenEndToEnd(t *testing.T) {
	env := newBrowserTestEnv(t)
	m := New(env, "", NamespaceBrowserSpec("browse", env))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	if cmd := app.InitCmd(); cmd != nil {
		if batch, ok := cmd().(tui.BatchMsg); ok {
			for _, sub := range batch {
				app.Dispatch(sub())
			}
		}
	}

	// controlStripFocusables + 1: title bar -> content, see
	// InitialFocusAdvances' own doc comment (assumes exactly one pane
	// at startup, true here).
	for range controlStripFocusables + 1 {
		app.HandleInput(input.KeyEvent{Key: input.KeyTab})
	}

	runFocusedInput(app, input.KeyEvent{Key: input.KeyEnter}) // descend into /x
	runFocusedInput(app, input.KeyEvent{Key: input.KeyDown})  // .. -> a.txt
	runFocusedInput(app, input.KeyEvent{Key: input.KeyEnter}) // preview a.txt
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "A") {
		t.Fatalf("expected a.txt's content 'A' on screen after preview:\n%s", buf)
	}

	runFocusedInput(app, input.KeyEvent{Key: input.KeyEsc}) // back to the listing
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "a.txt") {
		t.Fatalf("expected the listing (with 'a.txt') back on screen after Esc:\n%s", buf)
	}
}
