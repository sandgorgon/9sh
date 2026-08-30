package pane

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/layout"
	"github.com/sandgorgon/tui/tui"
)

func skipUnlessOnPath(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

// ---- pure Model.Update/View logic — no tui.App needed ----

func TestNewSeedsPanes(t *testing.T) {
	m := New(nil, "", ShellSpec("a"), ShellSpec("b"))
	if len(m.panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(m.panes))
	}
	if m.panes[0].id == m.panes[1].id {
		t.Fatal("panes got the same id")
	}
}

func TestToggleMinimizeFlipsState(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	if m.panes[0].minimized {
		t.Fatal("new pane should start expanded")
	}
	next, _ := m.Update(toggleMinimizeMsg{id: id})
	m = next.(Model)
	if !m.panes[0].minimized {
		t.Fatal("first toggle should minimize")
	}
	next, _ = m.Update(toggleMinimizeMsg{id: id})
	m = next.(Model)
	if m.panes[0].minimized {
		t.Fatal("second toggle should restore")
	}
}

func TestToggleMinimizeUnknownIDIsNoOp(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, cmd := m.Update(toggleMinimizeMsg{id: 999})
	if cmd != nil {
		t.Fatal("unknown id should not produce a Cmd")
	}
	m2 := next.(Model)
	if m2.panes[0].minimized {
		t.Fatal("unknown id toggle should not affect any real pane")
	}
}

func TestAddPaneMsgAppendsPane(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, _ := m.Update(addPaneMsg{spec: ShellSpec("b")})
	m = next.(Model)
	if len(m.panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(m.panes))
	}
	if m.panes[1].title != "b" {
		t.Fatalf("new pane title = %q, want b", m.panes[1].title)
	}
}

func TestClosePaneRemovesFromPanesAndTree(t *testing.T) {
	m := New(nil, "", ShellSpec("a"), ShellSpec("b"))
	closeID := m.panes[0].id
	keepID := m.panes[1].id

	next, cmd := m.Update(closePaneMsg{id: closeID})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("closing one of two panes should not produce a Cmd (only the last one quits)")
	}
	if len(m.panes) != 1 {
		t.Fatalf("got %d panes, want 1", len(m.panes))
	}
	if m.panes[0].id != keepID {
		t.Fatalf("wrong pane remained: got id %d, want %d", m.panes[0].id, keepID)
	}
	if len(m.root.children) != 1 {
		t.Fatalf("root has %d children, want 1", len(m.root.children))
	}
	if m.root.children[0].node.paneID != keepID {
		t.Fatalf("remaining tree leaf paneID = %d, want %d", m.root.children[0].node.paneID, keepID)
	}
}

func TestCloseUnknownIDIsNoOp(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, cmd := m.Update(closePaneMsg{id: 999})
	if cmd != nil {
		t.Fatal("unknown id should not produce a Cmd")
	}
	m2 := next.(Model)
	if len(m2.panes) != 1 {
		t.Fatalf("got %d panes, want 1 (unchanged)", len(m2.panes))
	}
}

func TestClosingLastPaneQuits(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	next, cmd := m.Update(closePaneMsg{id: id})
	if cmd == nil {
		t.Fatal("expected a Cmd when closing the last remaining pane")
	}
	if _, ok := cmd().(tui.QuitMsg); !ok {
		t.Fatalf("Cmd produced %T, want tui.QuitMsg", cmd())
	}
	m2 := next.(Model)
	if len(m2.panes) != 0 {
		t.Fatalf("got %d panes, want 0", len(m2.panes))
	}
}

// TestClosingOnePaneKeepsSiblingAlive is TestAddingSecondPaneKeepsFirstPaneAlive's
// counterpart in the other direction: removing a leaf from root's
// children must not disturb its remaining sibling's retained state.
// See removeLeafFromTree's doc comment for exactly why a naive
// "collapse the parent" implementation would have broken this.
func TestClosingOnePaneKeepsSiblingAlive(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	survivor := exec.Command("sh", "-c", "echo SURVIVOR; read x; echo GOT:$x")
	doomed := exec.Command("sh", "-c", "echo DOOMED; read x; echo GOT:$x")
	m := New(nil, "", Spec{Title: "survivor", Command: survivor}, Spec{Title: "doomed", Command: doomed})
	doomedID := m.panes[1].id

	app := tui.NewApp(m, 40, 16)
	defer app.Close()

	waitForText(t, app, "SURVIVOR", 3*time.Second)
	waitForText(t, app, "DOOMED", 3*time.Second)

	app.Dispatch(closePaneMsg{id: doomedID})
	forceRenders(app, 3)

	buf := app.Buffer().String()
	if strings.Contains(buf, "DOOMED") {
		t.Fatal("closed pane's content is still on screen")
	}
	if strings.Contains(buf, "failed to start") {
		t.Fatal("closing one pane discarded its sibling's retained state — the sibling's running process was killed")
	}
	if !strings.Contains(buf, "SURVIVOR") {
		t.Fatalf("surviving pane's output vanished after closing its sibling:\n%s", buf)
	}
}

// TestTitleBarXKeyClosesPane drives the real input path (app.HandleInput,
// not a synthetic Update(closePaneMsg{...}) call) to confirm 'x' actually
// closes a pane only once focus has reached its title bar — the control
// strip's own buttons only react to clicked() (Enter/Space/click), so a
// bare 'x' there must be a no-op, exactly like any other pane content.
func TestTitleBarXKeyClosesPane(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	first := exec.Command("sh", "-c", "echo FIRSTPANE; read x")
	second := exec.Command("sh", "-c", "echo SECONDPANE; read x")
	m := New(nil, "", Spec{Title: "first", Command: first}, Spec{Title: "second", Command: second})

	app := tui.NewApp(m, 40, 16)
	defer app.Close()

	waitForText(t, app, "FIRSTPANE", 3*time.Second)
	waitForText(t, app, "SECONDPANE", 3*time.Second)

	// Advance focus from the control strip's first button to the first
	// pane's own title bar — controlStripFocusables Tabs lands exactly
	// one short of where pane.InitialFocusAdvances would land on
	// content instead (see its doc comment).
	for range controlStripFocusables {
		app.HandleInput(input.KeyEvent{Key: input.KeyTab})
	}
	// HandleInput's returned Cmds aren't auto-run — App.Run's own event
	// loop does that (runCmd); a direct HandleInput caller has to do
	// the same two-step (run the Cmd, Dispatch its Msg) itself.
	for _, cmd := range app.HandleInput(input.KeyEvent{Rune: 'x'}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	forceRenders(app, 2)

	buf := app.Buffer().String()
	if strings.Contains(buf, "FIRSTPANE") {
		t.Fatalf("first pane should have been closed by 'x' on its title bar:\n%s", buf)
	}
	if !strings.Contains(buf, "SECONDPANE") {
		t.Fatalf("second pane should still be visible:\n%s", buf)
	}
}

func TestSplitPaneAddsSiblingInTree(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id

	next, cmd := m.Update(splitPaneMsg{id: id, dir: layout.Horizontal})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("splitPaneMsg should not produce a Cmd")
	}
	if len(m.panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(m.panes))
	}
	if m.panes[1].kind != KindKyuRepl {
		t.Fatalf("new sibling kind = %v, want KindKyuRepl", m.panes[1].kind)
	}

	// root wraps [original top-level row] -> now that row's own leaf
	// should have become an interior Horizontal split of [original, new].
	if len(m.root.children) != 1 {
		t.Fatalf("root has %d children, want 1 (unchanged — split happens inside the row, not at root)", len(m.root.children))
	}
	row := m.root.children[0].node
	if row.paneID != 0 {
		t.Fatal("the split row should now be an interior node, not still a bare leaf")
	}
	if row.dir != layout.Horizontal {
		t.Fatalf("split direction = %v, want Horizontal", row.dir)
	}
	if len(row.children) != 2 {
		t.Fatalf("split row has %d children, want 2", len(row.children))
	}
	if row.children[0].node.paneID != id {
		t.Fatalf("first child paneID = %d, want original pane %d", row.children[0].node.paneID, id)
	}
	if row.children[1].node.paneID != m.panes[1].id {
		t.Fatalf("second child paneID = %d, want new pane %d", row.children[1].node.paneID, m.panes[1].id)
	}
}

func TestSplitUnknownIDIsNoOp(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, cmd := m.Update(splitPaneMsg{id: 999, dir: layout.Vertical})
	if cmd != nil {
		t.Fatal("unknown id should not produce a Cmd")
	}
	m2 := next.(Model)
	if len(m2.panes) != 1 {
		t.Fatalf("got %d panes, want 1 (unchanged)", len(m2.panes))
	}
}

func TestResizeAdjustsWeightAndClamps(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	next, _ := m.Update(splitPaneMsg{id: id, dir: layout.Horizontal})
	m = next.(Model)
	row := m.root.children[0].node

	next, _ = m.Update(resizePaneMsg{id: id, delta: 3})
	m = next.(Model)
	row = m.root.children[0].node
	if row.children[0].weight != 4 {
		t.Fatalf("weight after +3 = %d, want 4 (started at 1)", row.children[0].weight)
	}

	next, _ = m.Update(resizePaneMsg{id: id, delta: -10})
	m = next.(Model)
	row = m.root.children[0].node
	if row.children[0].weight != 1 {
		t.Fatalf("weight after a large decrease = %d, want clamped to 1", row.children[0].weight)
	}
}

// TestSplitKeysOnTitleBar drives the real input path, mirroring
// TestTitleBarXKeyClosesPane, to confirm 'r' on a title bar actually
// splits via app.HandleInput (not just a direct Update call) — the new
// sibling's own title bar (with the same close/split/resize hint text)
// should now be on screen too. 60 cols split into two 30-col halves is
// deliberately narrow enough to have once clipped the hint text
// mid-word before it was shortened — see paneNode's own comment on
// why the hint is "(x/d/r/+/-)" rather than the old spelled-out form.
func TestSplitKeysOnTitleBar(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))

	app := tui.NewApp(m, 60, 16)
	defer app.Close()

	for range controlStripFocusables {
		app.HandleInput(input.KeyEvent{Key: input.KeyTab})
	}
	for _, cmd := range app.HandleInput(input.KeyEvent{Rune: 'r'}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	forceRenders(app, 2)

	buf := app.Buffer().String()
	if n := strings.Count(buf, "x/d/r/+/-"); n != 2 {
		t.Fatalf("expected 2 title bars after splitting, found %d:\n%s", n, buf)
	}
}

// TestFKeyRequestsFocusAtComputedIndex is a direct Update-level test:
// with 3 panes, F2 should ask to focus the second pane's title bar —
// controlStripFocusables buttons, then 2 focusables (title, content)
// per pane ahead of it, matching paneOrder()'s document order and
// tui's own App.focusables ordering (see Update's input.KeyEvent case
// for the arithmetic this pins down).
func TestFKeyRequestsFocusAtComputedIndex(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil), KyuReplSpec("b", nil), KyuReplSpec("c", nil))

	_, cmd := m.Update(input.KeyEvent{Key: input.KeyF2})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd requesting a focus change")
	}
	fm, ok := cmd().(tui.FocusMsg)
	if !ok {
		t.Fatalf("expected tui.FocusMsg, got %T", cmd())
	}
	want := controlStripFocusables + 2 // pane b's title bar
	if fm.Index != want {
		t.Fatalf("got focus index %d, want %d", fm.Index, want)
	}
}

// TestFKeyPastPaneCountIsNoop confirms F9 with only one pane open
// doesn't return a Cmd at all — fKeyPaneNumber recognizes the key, but
// paneOrder() is too short, so there's nothing to jump to.
func TestFKeyPastPaneCountIsNoop(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil))
	_, cmd := m.Update(input.KeyEvent{Key: input.KeyF9})
	if cmd != nil {
		t.Fatalf("expected a nil Cmd, got one that produces %v", cmd())
	}
}

// TestFKeyJumpsFocusEndToEnd drives the real input path (mirroring
// TestSplitKeysOnTitleBar): confirms a real F2 KeyEvent traveling
// through App.HandleInput's actual event pipeline (not a direct
// Update call) still produces a Cmd yielding the right tui.FocusMsg.
//
// This does *not* prove tui actually moves live focus in response —
// FocusMsg's special-casing (like QuitMsg's and ClipboardMsg's) lives
// inside Run()'s own event loop, not in Dispatch, so App.FocusIndex()
// never changes here even after "delivering" the Cmd's Msg via
// Dispatch — confirmed by tracing tui/app.go directly: Run() reads
// this Msg off its own private msgCh and calls a.SetFocus itself
// before Dispatch ever sees it, and there's no headless harness for
// Run() (the tui#7 PR's own test plan notes the same limitation for
// QuitMsg/ClipboardMsg — this project's headless tests can't drive
// Run() at all, only Dispatch/HandleInput). Real end-to-end
// confirmation that F2 moves live focus is a tmux/real-terminal check,
// not something a Go test can assert.
func TestFKeyJumpsFocusEndToEnd(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil), KyuReplSpec("b", nil))
	app := tui.NewApp(m, 80, 16)
	defer app.Close()

	cmds := app.HandleInput(input.KeyEvent{Key: input.KeyF2})
	if len(cmds) != 1 || cmds[0] == nil {
		t.Fatalf("expected exactly one non-nil Cmd, got %v", cmds)
	}
	fm, ok := cmds[0]().(tui.FocusMsg)
	if !ok {
		t.Fatalf("expected tui.FocusMsg, got %T", cmds[0]())
	}
	want := controlStripFocusables + 2 // pane b's title bar
	if fm.Index != want {
		t.Fatalf("got focus index %d, want %d", fm.Index, want)
	}
}

// TestPaneTitleShowsFKeyLabel confirms the "[F#]" hint painted in
// paneNode actually reaches the screen for the first 9 panes.
func TestPaneTitleShowsFKeyLabel(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil), KyuReplSpec("b", nil))
	app := tui.NewApp(m, 80, 16)
	defer app.Close()

	buf := app.Buffer().String()
	if !strings.Contains(buf, "[F1]") || !strings.Contains(buf, "[F2]") {
		t.Fatalf("expected both [F1] and [F2] labels on screen:\n%s", buf)
	}
}

// TestSplittingLiveShellPanePreservesItsProcess pins down the fix for
// sandgorgon/tui#3 (picked up via the v0.1.10 bump): splitting a pane
// wraps it one level deeper in the tree, which used to look like a
// brand-new subtree to tui's reconciler and discard the pane's live
// pty; the reconciler's whole-tree key index now finds and reuses it
// instead. If this test starts failing again (the process gets
// discarded), that's a real regression, either in a future tui bump or
// in this file's own key handling — not something to just re-flip back
// to the old "loses its process" assertion.
func TestSplittingLiveShellPanePreservesItsProcess(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	cmd := exec.Command("sh", "-c", "echo READY; read x; echo GOT:$x")
	m := New(nil, "", Spec{Title: "test", Command: cmd})
	id := m.panes[0].id

	app := tui.NewApp(m, 40, 16)
	defer app.Close()

	waitForText(t, app, "READY", 3*time.Second)

	app.Dispatch(splitPaneMsg{id: id, dir: layout.Horizontal})
	forceRenders(app, 3)

	if strings.Contains(app.Buffer().String(), "failed to start") {
		t.Fatal("split-open pane's process was discarded and failed to restart from an already-consumed exec.Cmd — tui#3's fix should prevent this")
	}
	if !strings.Contains(app.Buffer().String(), "READY") {
		t.Fatalf("original pane's output vanished after splitting it:\n%s", app.Buffer().String())
	}
}

func TestPaneExitedMsgMarksExited(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	next, _ := m.Update(paneExitedMsg{id: id})
	m = next.(Model)
	if !m.panes[0].exited {
		t.Fatal("pane should be marked exited")
	}
}

func TestQuitRequestedProducesQuitCmd(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	_, cmd := m.Update(quitRequestedMsg{})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	if _, ok := cmd().(tui.QuitMsg); !ok {
		t.Fatalf("Cmd produced %T, want tui.QuitMsg", cmd())
	}
}

func TestClickedRecognizesEnterSpaceAndLeftClick(t *testing.T) {
	cases := []struct {
		name string
		e    input.Event
		want bool
	}{
		{"enter", input.KeyEvent{Key: input.KeyEnter}, true},
		{"space", input.KeyEvent{Rune: ' '}, true},
		{"other key", input.KeyEvent{Rune: 'x'}, false},
		{"left click", input.MouseEvent{Button: input.MouseLeft}, true},
		{"drag", input.MouseEvent{Button: input.MouseLeft, Drag: true}, false},
		{"release", input.MouseEvent{Button: input.MouseRelease}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clicked(c.e); got != c.want {
				t.Errorf("clicked(%v) = %v, want %v", c.e, got, c.want)
			}
		})
	}
}

// ---- integration: real tui.App + widget.Terminal, the part with
// actual keying-correctness risk (see model.go's package doc) ----

func TestMinimizeKeepsProcessAliveAndStatePreserved(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	cmd := exec.Command("sh", "-c", "echo READY; read x; echo GOT:$x")
	m := New(nil, "", Spec{Title: "test", Command: cmd})
	id := m.panes[0].id

	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	waitForText(t, app, "READY", 3*time.Second)

	app.Dispatch(toggleMinimizeMsg{id: id})
	forceRenders(app, 3)
	if strings.Contains(app.Buffer().String(), "READY") {
		t.Fatal("minimized pane should not occupy screen space")
	}

	app.Dispatch(toggleMinimizeMsg{id: id})
	waitForText(t, app, "READY", 3*time.Second)

	if strings.Contains(app.Buffer().String(), "failed to start") {
		t.Fatal("pane was disposed and recreated across minimize/restore — the running process was killed")
	}
}

// TestAddingSecondPaneKeepsFirstPaneAlive is the tree-restructuring
// counterpart to TestMinimizeKeepsProcessAliveAndStatePreserved:
// appending a new top-level row must not discard an existing pane's
// retained widget state (its live pty in particular). See
// appendTopLevelRow's doc comment for exactly why this could break —
// m.root has to keep its own identity stable across the append, or
// the first pane's parent would effectively change from reconcile's
// point of view even though the pane's own key doesn't.
func TestAddingSecondPaneKeepsFirstPaneAlive(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	cmd := exec.Command("sh", "-c", "echo READY; read x; echo GOT:$x")
	m := New(nil, "", Spec{Title: "first", Command: cmd})

	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	waitForText(t, app, "READY", 3*time.Second)

	app.Dispatch(addPaneMsg{spec: ShellSpec("second")})
	forceRenders(app, 3)

	if strings.Contains(app.Buffer().String(), "failed to start") {
		t.Fatal("adding a second pane discarded the first pane's retained state — its running process was killed")
	}
	if !strings.Contains(app.Buffer().String(), "READY") {
		t.Fatalf("first pane's output vanished after adding a second pane:\n%s", app.Buffer().String())
	}
}

func TestExitedPaneShowsIndicatorAfterEvent(t *testing.T) {
	skipUnlessOnPath(t, "true")
	cmd := exec.Command("true")
	m := New(nil, "", Spec{Title: "test", Command: cmd})

	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	// OnExit fires opportunistically from HandleEvent (see
	// widget.Terminal's doc comment) — repeated no-op renders are
	// enough to eventually observe the exited state once the process
	// has actually exited and a render happens to run after that.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		app.Dispatch(struct{}{})
		if strings.Contains(app.Buffer().String(), "exited") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("exited indicator never appeared:\n%s", app.Buffer().String())
}

func forceRenders(app *tui.App, n int) {
	for range n {
		app.Dispatch(struct{}{})
	}
}

func waitForText(t *testing.T, app *tui.App, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		app.Dispatch(struct{}{})
		if strings.Contains(app.Buffer().String(), substr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in buffer:\n%s", substr, app.Buffer().String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
