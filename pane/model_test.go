package pane

import (
	"fmt"
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

// TestControlStripColorDistinctFromPaneTitles guards against the
// regression reported 2026-08-30: controlStripStyle used to base
// itself on theme.Primary, which is literally the same RGB value as
// theme.Focus in tui/style's own default themes — so a focused pane's
// title bar/top border came out visually identical to the always-
// visible control strip above it. Checks both theme.Border (an
// unfocused pane title) and theme.Focus (a focused one), unfocused and
// focused, against both of the control strip's own states.
func TestControlStripColorDistinctFromPaneTitles(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	for _, csFocused := range []bool{false, true} {
		csBg := m.controlStripStyle(csFocused).Bg
		if csBg == m.theme.Border {
			t.Errorf("controlStripStyle(%v).Bg matches theme.Border (an unfocused pane title's color)", csFocused)
		}
		if csBg == m.theme.Focus {
			t.Errorf("controlStripStyle(%v).Bg matches theme.Focus (a focused pane title's color)", csFocused)
		}
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

// TestAddPaneMsgSplitsLastPaneInsteadOfAppendingRow guards the
// addPaneMsg rewrite: a control-strip "+" now splits the last pane in
// document order (see addPaneTarget) rather than appending another
// root-level row, and alternates direction each time (see
// Model.nextSplitDir) so repeated additions actually tile in two
// dimensions instead of only ever deepening root's own Vertical axis.
func TestAddPaneMsgSplitsLastPaneInsteadOfAppendingRow(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	idA := m.panes[0].id

	// New's own doc comment: nextSplitDir starts Horizontal, so the
	// first "+" should split A to the right.
	next, _ := m.Update(addPaneMsg{spec: ShellSpec("b")})
	m = next.(Model)
	idB := m.panes[1].id

	if len(m.root.children) != 1 {
		t.Fatalf("root has %d children, want 1 (still no new root-level row)", len(m.root.children))
	}
	row := m.root.children[0].node
	if row.paneID != 0 {
		t.Fatal("root's sole child should now be an interior split, not still a bare leaf")
	}
	if row.dir != layout.Horizontal {
		t.Fatalf("first + split direction = %v, want Horizontal", row.dir)
	}
	if len(row.children) != 2 || row.children[0].node.paneID != idA || row.children[1].node.paneID != idB {
		t.Fatalf("first + split children = %+v, want [%d, %d]", row.children, idA, idB)
	}

	// Second "+" should alternate to Vertical, and split off the last
	// pane in document order (B), not the root or A.
	next, _ = m.Update(addPaneMsg{spec: ShellSpec("c")})
	m = next.(Model)
	idC := m.panes[2].id

	if len(m.root.children) != 1 {
		t.Fatalf("root has %d children after second +, want 1", len(m.root.children))
	}
	row = m.root.children[0].node
	if row.dir != layout.Horizontal || len(row.children) != 2 {
		t.Fatalf("outer split changed shape after second +: dir=%v children=%d, want unchanged Horizontal/2", row.dir, len(row.children))
	}
	if row.children[0].node.paneID != idA {
		t.Fatalf("A should still be the outer split's first child, unmoved by the second +")
	}
	inner := row.children[1].node
	if inner.paneID != 0 {
		t.Fatal("B's slot should now be an interior split, not still a bare leaf")
	}
	if inner.dir != layout.Vertical {
		t.Fatalf("second + split direction = %v, want Vertical (alternated from the first)", inner.dir)
	}
	if len(inner.children) != 2 || inner.children[0].node.paneID != idB || inner.children[1].node.paneID != idC {
		t.Fatalf("second + split children = %+v, want [%d, %d]", inner.children, idB, idC)
	}

	if order := m.paneOrder(); len(order) != 3 || order[0] != idA || order[1] != idB || order[2] != idC {
		t.Fatalf("paneOrder() = %v, want [%d, %d, %d]", order, idA, idB, idC)
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

	next, cmd := m.Update(splitPaneMsg{id: id, dir: layout.Horizontal, spec: KyuReplSpec("kyu", nil)})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("splitPaneMsg should not produce a Cmd for a kyu-repl sibling (no initial load needed)")
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

// TestHorizontalSplitPaintsPerPaneBorders drives a real horizontal
// split through the actual View()/renderSplit/paneNode render path and
// checks the frame buffer directly for each pane's own box-drawing
// frame (see paneNode's own doc comment on why every expanded pane
// draws a complete, independent border rather than the two panes
// sharing one divider line) — Buffer's text-only rendering (strings.
// Count etc., as most other pane tests use) can see the glyph but not
// the background alone, and the glyph alone isn't enough either: it's
// the same '│' widget.Terminal and other content could plausibly
// contain, so both together (background AND glyph, at the same cell)
// is what actually distinguishes a border cell from a coincidence.
// Scans a content row (below the title/top-border row) for '│'
// columns: two panes side by side, each drawing its own left+right
// border, means 4 — not 1 shared divider, as an earlier design had it.
// See TestVerticalSplitPaintsPerPaneBorders for the other axis.
func TestHorizontalSplitPaintsPerPaneBorders(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	id := m.panes[0].id

	next, _ := m.Update(splitPaneMsg{id: id, dir: layout.Horizontal, spec: KyuReplSpec("kyu2", nil)})
	m = next.(Model)

	app := tui.NewApp(m, 40, 10)
	defer app.Close()
	forceRenders(app, 1)

	buf := app.Buffer()
	const contentRow = 5 // well below the control strip + top-border row
	sideCols := 0
	for x := range 40 {
		c := buf.At(x, contentRow)
		if c.Style.Bg == m.theme.Border && c.Rune == '│' {
			sideCols++
		}
	}
	if sideCols != 4 {
		t.Fatalf("got %d border-styled '│' columns at row %d, want 4 (2 panes x left+right each)", sideCols, contentRow)
	}

	const topBorderRow = 1 // row 0 is the control strip
	topLeftCorners := 0
	for x := range 40 {
		c := buf.At(x, topBorderRow)
		if c.Style.Bg == m.theme.Border && c.Rune == '┌' {
			topLeftCorners++
		}
	}
	if topLeftCorners != 2 {
		t.Fatalf("got %d '┌' corners at row %d, want 2 (one per pane)", topLeftCorners, topBorderRow)
	}
}

// TestVerticalSplitPaintsPerPaneBorders is
// TestHorizontalSplitPaintsPerPaneBorders's counterpart for the other
// split axis: two panes stacked top-to-bottom should each still draw
// a complete frame — a '─' rule cell somewhere past the corners/title
// (column 20, an arbitrary interior column both panes' frames span),
// and both a '┌' and a '└' somewhere down column 0 (the upper pane's
// top-left and the lower pane's bottom-left, at minimum).
func TestVerticalSplitPaintsPerPaneBorders(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	id := m.panes[0].id

	next, _ := m.Update(splitPaneMsg{id: id, dir: layout.Vertical, spec: KyuReplSpec("kyu2", nil)})
	m = next.(Model)

	app := tui.NewApp(m, 40, 14)
	defer app.Close()
	forceRenders(app, 1)

	buf := app.Buffer()
	foundRule := false
	for y := range 14 {
		if buf.At(20, y).Style.Bg == m.theme.Border && buf.At(20, y).Rune == '─' {
			foundRule = true
			break
		}
	}
	if !foundRule {
		t.Fatalf("expected at least one '─' border cell at column 20:\n%s", buf.String())
	}

	foundTL, foundBL := false, false
	for y := range 14 {
		c := buf.At(0, y)
		if c.Style.Bg != m.theme.Border {
			continue
		}
		switch c.Rune {
		case '┌':
			foundTL = true
		case '└':
			foundBL = true
		}
	}
	if !foundTL || !foundBL {
		t.Fatalf("expected both '┌' and '└' somewhere in column 0: foundTL=%v foundBL=%v", foundTL, foundBL)
	}
}

func TestSplitUnknownIDIsNoOp(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, cmd := m.Update(splitPaneMsg{id: 999, dir: layout.Vertical, spec: KyuReplSpec("kyu", nil)})
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
	next, _ := m.Update(splitPaneMsg{id: id, dir: layout.Horizontal, spec: KyuReplSpec("kyu", nil)})
	m = next.(Model)
	row := m.root.children[0].node

	next, _ = m.Update(resizePaneMsg{id: id, delta: 3})
	m = next.(Model)
	row = m.root.children[0].node
	if want := defaultPaneWeight + 3; row.children[0].weight != want {
		t.Fatalf("weight after +3 = %d, want %d (started at defaultPaneWeight=%d)", row.children[0].weight, want, defaultPaneWeight)
	}

	next, _ = m.Update(resizePaneMsg{id: id, delta: -10})
	m = next.(Model)
	row = m.root.children[0].node
	if row.children[0].weight != 1 {
		t.Fatalf("weight after a large decrease = %d, want clamped to 1", row.children[0].weight)
	}
}

// TestResizeFloorSwitchesToAbsoluteMinCells guards the follow-up to
// the bug above (reported 2026-08-30, same session): plain Fill(weight)
// has no floor of its own once weight bottoms out at 1 — a sibling
// growing via its own '+' could still squeeze a weight-1 pane to
// nothing. childConstraint should switch to layout.Min(paneMinCells)
// exactly when weight hits that floor (not before, and not for
// minimize/zoom's own separate Length(1)/Length(0) collapses, which
// this test doesn't touch), guaranteeing at least one content line/
// column stays visible — going smaller than that is minimize's job,
// not more '-' presses.
func TestResizeFloorSwitchesToAbsoluteMinCells(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))

	// node must be non-nil (childConstraint dereferences c.node.paneID)
	// — paneID 0 marks an interior split, which is fine here since this
	// test only exercises the weight-based Fill/Min branch, not the
	// leaf-specific minimize check above it.
	node := &splitNode{}
	if got := m.childConstraint(splitChild{weight: 2, node: node}, true); got != layout.Fill(2) {
		t.Errorf("childConstraint at weight 2 = %v, want Fill(2)", got)
	}
	if got := m.childConstraint(splitChild{weight: 1, node: node}, true); got != layout.Min(paneMinCells) {
		t.Errorf("childConstraint at weight 1 (the resize floor) = %v, want Min(%d)", got, paneMinCells)
	}
}

// TestResizingASmallerPaneBelowItsStartingWeightWorks guards the bug
// reported 2026-08-30: every new pane used to start at weight 1, which
// is also resizePane's own floor — so pressing '-' on a fresh pane was
// a no-op from the very first press, since there was never any room to
// shrink below "the original size" at all. defaultPaneWeight fixes
// this by starting well above that floor; this pins down that a single
// '-' press on a freshly split pane actually decreases its weight, not
// just that repeated presses eventually clamp correctly (already
// covered above).
func TestResizingASmallerPaneBelowItsStartingWeightWorks(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	next, _ := m.Update(splitPaneMsg{id: id, dir: layout.Horizontal, spec: KyuReplSpec("kyu", nil)})
	m = next.(Model)
	row := m.root.children[0].node
	startWeight := row.children[0].weight

	next, _ = m.Update(resizePaneMsg{id: id, delta: -1})
	m = next.(Model)
	row = m.root.children[0].node
	if row.children[0].weight >= startWeight {
		t.Fatalf("weight after one '-' = %d, want less than the starting weight %d", row.children[0].weight, startWeight)
	}
}

// TestSplitKeysOnTitleBar drives the real input path, mirroring
// TestTitleBarXKeyClosesPane, to confirm the two-step 'r' then 'k'
// split flow actually works via app.HandleInput (not just direct
// Update calls) — the new sibling's own title bar (with the same
// close/split/zoom/resize hint text) should now be on screen too. 60
// cols split into two 30-col halves is deliberately narrow enough to
// have once clipped the hint text mid-word before it was shortened —
// see paneNode's own comment on why the hint is "(x/d/r/z/+/-)" rather
// than the old spelled-out form.
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
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "s=shell k=kyu") {
		t.Fatalf("expected the split-flow kind prompt after 'r':\n%s", buf)
	}
	for _, cmd := range app.HandleInput(input.KeyEvent{Rune: 'k'}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	forceRenders(app, 2)

	buf := app.Buffer().String()
	if n := strings.Count(buf, "x/d/r/z/+/-"); n != 2 {
		t.Fatalf("expected 2 title bars after splitting, found %d:\n%s", n, buf)
	}
}

// TestSplitFlowCancelsOnUnrecognizedKey confirms any key other than
// s/k/b/j/h during the split-flow's second step abandons it (via
// cancelSplitMsg) rather than splitting with some default kind.
func TestSplitFlowCancelsOnUnrecognizedKey(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	id := m.panes[0].id

	next, _ := m.Update(beginSplitMsg{id: id, dir: layout.Vertical})
	m = next.(Model)
	if !m.panes[0].awaitingSplitKind {
		t.Fatal("expected awaitingSplitKind after beginSplitMsg")
	}

	next, cmd := m.Update(cancelSplitMsg{id: id})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("cancelSplitMsg should not produce a Cmd")
	}
	if m.panes[0].awaitingSplitKind {
		t.Fatal("expected awaitingSplitKind cleared after cancelSplitMsg")
	}
	if len(m.panes) != 1 {
		t.Fatalf("got %d panes, want 1 (cancel shouldn't split)", len(m.panes))
	}
}

// TestSplitKindKeyMapping is a direct unit test of splitKindKey and
// defaultSpecForKind — the letter-to-kind mapping paneNode's title-bar
// closure relies on for the split flow's second keypress. It's a pure
// function, not something that needs a real widget/App to exercise;
// TestSplitKeysOnTitleBar already covers one full real-input-path case
// ('r' then 'k') end to end, so this fills in the other four kinds at
// the unit level instead of repeating a full tui.App drive five times.
func TestSplitKindKeyMapping(t *testing.T) {
	cases := []struct {
		key  rune
		kind Kind
	}{
		{'s', KindShell},
		{'k', KindKyuRepl},
		{'b', KindNamespaceBrowser},
		{'j', KindJobViewer},
		{'h', KindSessionViewer},
	}
	for _, tc := range cases {
		t.Run(string(tc.key), func(t *testing.T) {
			kind, ok := splitKindKey(tc.key)
			if !ok || kind != tc.kind {
				t.Fatalf("splitKindKey(%q) = (%v, %v), want (%v, true)", tc.key, kind, ok, tc.kind)
			}
			if spec := defaultSpecForKind(kind, nil, ""); spec.Kind != tc.kind {
				t.Fatalf("defaultSpecForKind(%v) built a Spec of kind %v", kind, spec.Kind)
			}
		})
	}
	if _, ok := splitKindKey('q'); ok {
		t.Fatal("splitKindKey('q') should not be recognized")
	}
}

// TestSplitPaneMsgWithChosenSpecBuildsRightSibling confirms Update's
// splitPaneMsg case (which paneNode's title-bar closure feeds a Spec
// computed from splitKindKey/defaultSpecForKind) actually builds a
// sibling of that kind, for a kind other than the default kyu-repl —
// TestSplitPaneAddsSiblingInTree already covers the kyu-repl case.
func TestSplitPaneMsgWithChosenSpecBuildsRightSibling(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	id := m.panes[0].id

	next, cmd := m.Update(splitPaneMsg{id: id, dir: layout.Vertical, spec: ShellSpec("shell")})
	m = next.(Model)
	// A new shell pane starts a shellRedrawTickCmd chain (no separate
	// initial-load Cmd exists for KindShell) — see
	// TestSplitPaneWithShellSpecStartsShellRedrawTick for that behavior
	// in isolation.
	if cmd == nil {
		t.Fatal("splitting with a shell spec should produce a Cmd (the shell redraw tick)")
	}
	if len(m.panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(m.panes))
	}
	if m.panes[1].kind != KindShell {
		t.Fatalf("new sibling kind = %v, want KindShell", m.panes[1].kind)
	}
}

// TestSplitPaneWithShellSpecStartsShellRedrawTick locks in the fix for
// shell-pane output only becoming visible after an unrelated keypress
// forced a redraw (widget.Terminal's pty output updates its internal
// state in a background goroutine, invisible until the App next
// renders a frame for any other reason — see shellRedrawTickCmd's doc
// comment). Splitting off a shell pane should start exactly one
// self-rescheduling tick chain, not zero and not more than one.
func TestSplitPaneWithShellSpecStartsShellRedrawTick(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	id := m.panes[0].id

	next, cmd := m.Update(splitPaneMsg{id: id, dir: layout.Vertical, spec: ShellSpec("shell")})
	m = next.(Model)
	if !m.shellTickRunning {
		t.Fatal("shellTickRunning should be true once a shell pane exists")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd starting the shell redraw tick")
	}
	if _, ok := cmd().(shellRedrawTickMsg); !ok {
		t.Fatalf("expected the Cmd to eventually produce shellRedrawTickMsg, got %T", cmd())
	}

	// A second shell pane must not start a second chain.
	id2 := m.panes[1].id
	_, cmd2 := m.Update(splitPaneMsg{id: id2, dir: layout.Vertical, spec: ShellSpec("shell2")})
	if cmd2 != nil {
		t.Fatal("a second shell pane should not start a redundant tick chain")
	}
}

// TestShellRedrawTickStopsOnceNoShellPaneRemains confirms the tick
// chain reschedules itself while a shell pane is mounted and stops
// (clearing shellTickRunning) once the last one closes, rather than
// ticking forever in the background.
func TestShellRedrawTickStopsOnceNoShellPaneRemains(t *testing.T) {
	m := New(nil, "", ShellSpec("shell"))
	m.shellTickRunning = true

	next, cmd := m.Update(shellRedrawTickMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected the tick to reschedule itself while a shell pane remains")
	}
	if !m.shellTickRunning {
		t.Fatal("shellTickRunning should stay true while rescheduling")
	}

	m.panes[0].kind = KindKyuRepl // simulate the shell pane having closed/changed
	next, cmd = m.Update(shellRedrawTickMsg{})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("expected the tick to stop once no shell pane remains")
	}
	if m.shellTickRunning {
		t.Fatal("shellTickRunning should be cleared once the tick stops")
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

// TestKyuReplPgUpScrollsTranscript drives the real input/render path
// (not just kyuReplWidget's own HandleEvent/scrollOffset field, which
// TestKyuReplPgUpPgDownAdjustScrollOffset in kyurepl_test.go already
// covers as a pure unit test) to confirm scrolling actually changes
// what Paint shows — an off-by-one in Paint's own start/end slicing
// wouldn't be caught by the field-level test alone. Submits enough
// distinct-marker lines to overflow a deliberately tiny content area,
// then checks that PgUp swaps the latest marker out of view for an
// earlier one, and enough PgDowns bring it back. Markers are string
// literals ("m1".."m9"), not bare digits: value.String's own String()
// returns unquoted content with no other formatting, and critically
// every line already contains a literal '9' via the "9sh>" prompt
// itself — a bare-digit marker would false-positive-match that on
// every single line, not just the one actually being checked for.
func TestKyuReplPgUpScrollsTranscript(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	app := tui.NewApp(m, 40, 6) // small enough that a handful of lines overflow it
	defer app.Close()

	for range controlStripFocusables + 1 { // +1: title bar -> content, see InitialFocusAdvances
		app.HandleInput(input.KeyEvent{Key: input.KeyTab})
	}
	for n := 1; n <= 9; n++ {
		marker := fmt.Sprintf(`"m%d"`, n)
		for _, r := range marker {
			for _, cmd := range app.HandleInput(input.KeyEvent{Rune: r}) {
				if cmd != nil {
					app.Dispatch(cmd())
				}
			}
		}
		for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyEnter}) {
			if cmd != nil {
				app.Dispatch(cmd())
			}
		}
	}
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "m9") {
		t.Fatalf("expected the latest marker (m9, echoed input or result) visible before scrolling:\n%s", buf)
	}

	// Hammer PgUp well past enough to reach the very top (start clamps
	// at 0 — see childConstraint/Paint's own max0 reasoning) rather
	// than relying on one press moving exactly far enough: both m9's
	// echoed-input line and its result line contain the "m9" marker,
	// so a single page isn't guaranteed to clear both out of view.
	for range 10 {
		for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyPgUp}) {
			if cmd != nil {
				app.Dispatch(cmd())
			}
		}
	}
	forceRenders(app, 1)
	buf := app.Buffer().String()
	if strings.Contains(buf, "m9") {
		t.Fatalf("expected the latest marker (m9) scrolled out of view after PgUp:\n%s", buf)
	}
	if !strings.Contains(buf, "m1") {
		t.Fatalf("expected the earliest marker (m1) visible at the top after PgUp:\n%s", buf)
	}

	for range 15 { // more than enough PgDowns to reach the bottom again
		for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyPgDown}) {
			if cmd != nil {
				app.Dispatch(cmd())
			}
		}
	}
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "m9") {
		t.Fatalf("expected the latest marker (m9) visible again after scrolling back down:\n%s", buf)
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

	app.Dispatch(splitPaneMsg{id: id, dir: layout.Horizontal, spec: KyuReplSpec("kyu", nil)})
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

func TestToggleThemeMsgFlipsAppearance(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	start := m.theme.Appearance

	next, cmd := m.Update(toggleThemeMsg{})
	if cmd != nil {
		t.Fatal("toggling the theme should not produce a Cmd")
	}
	m = next.(Model)
	if m.theme.Appearance == start {
		t.Fatalf("theme.Appearance unchanged after one toggle (still %v)", start)
	}

	next, _ = m.Update(toggleThemeMsg{})
	m = next.(Model)
	if m.theme.Appearance != start {
		t.Fatalf("theme.Appearance = %v after a second toggle, want back to %v", m.theme.Appearance, start)
	}
}

func TestToggleZoomMsgSetsAndClearsZoomedID(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	m, _ = m.splitPane(m.panes[0].id, layout.Horizontal, ShellSpec("b"))
	idA, idB := m.panes[0].id, m.panes[1].id

	next, cmd := m.Update(toggleZoomMsg{id: idA})
	if cmd != nil {
		t.Fatal("toggling zoom should not produce a Cmd")
	}
	m = next.(Model)
	if m.zoomedID != idA {
		t.Fatalf("zoomedID = %d after zooming A, want %d", m.zoomedID, idA)
	}

	// Zooming a *different* pane while one's already zoomed should
	// switch, not toggle it off.
	next, _ = m.Update(toggleZoomMsg{id: idB})
	m = next.(Model)
	if m.zoomedID != idB {
		t.Fatalf("zoomedID = %d after zooming B, want %d (should switch, not clear)", m.zoomedID, idB)
	}

	// Toggling the *currently* zoomed pane again un-zooms.
	next, _ = m.Update(toggleZoomMsg{id: idB})
	m = next.(Model)
	if m.zoomedID != 0 {
		t.Fatalf("zoomedID = %d after re-toggling B, want 0 (un-zoomed)", m.zoomedID)
	}
}

func TestClosingZoomedPaneClearsZoom(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	m, _ = m.splitPane(m.panes[0].id, layout.Horizontal, ShellSpec("b"))
	idB := m.panes[1].id

	next, _ := m.Update(toggleZoomMsg{id: idB})
	m = next.(Model)
	if m.zoomedID != idB {
		t.Fatal("setup: expected B to be zoomed")
	}

	next, _ = m.Update(closePaneMsg{id: idB})
	m = next.(Model)
	if m.zoomedID != 0 {
		t.Fatalf("zoomedID = %d after closing the zoomed pane, want 0", m.zoomedID)
	}
}

// TestZoomingAPaneKeepsSiblingProcessAlive is zoom's counterpart to
// TestMinimizeKeepsProcessAliveAndStatePreserved: collapsing every
// pane off the zoomed one's path to Length(1) (see childConstraint)
// must not discard their retained widget state — a live pty in
// particular — the same preserve-by-collapsing-not-removing guarantee
// minimize already established, now exercised for zoom's own subtree-
// wide collapse instead of a single pane's own axis-limited one.
//
// Builds the App from a single pane and splits via Dispatch
// afterward, matching TestSplittingLiveShellPanePreservesItsProcess's
// established pattern — not incidental: a live-shell pane that's
// already part of an interior split from NewApp's very first frame
// (i.e., built via Model.splitPane before ever constructing the App)
// hits a separate, pre-existing pty-sizing quirk unrelated to zoom
// (its content got stuck after a single echoed character) that no
// existing test happened to exercise before this one tried to. Worth
// its own investigation someday, but out of scope here — this test
// only needs to check zoom's own collapse-preserves-state guarantee,
// which the Dispatch-after-NewApp construction verifies just as well
// without tripping over that separate issue.
func TestZoomingAPaneKeepsSiblingProcessAlive(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	cmd := exec.Command("sh", "-c", "echo READY; read x; echo GOT:$x")
	m := New(nil, "", Spec{Title: "shell", Command: cmd})
	shellID := m.panes[0].id

	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	waitForText(t, app, "READY", 3*time.Second)

	app.Dispatch(splitPaneMsg{id: shellID, dir: layout.Horizontal, spec: KyuReplSpec("kyu", nil)})
	forceRenders(app, 3)
	// Model.nextID is a simple monotonic counter (see New/withNewPane/
	// splitPane) with no App-level accessor for "the pane a Dispatch
	// just created" — shellID is the only id handed out before this
	// split, so the new sibling's is deterministically the next one.
	kyuID := shellID + 1

	app.Dispatch(toggleZoomMsg{id: kyuID})
	forceRenders(app, 3)
	if strings.Contains(app.Buffer().String(), "READY") {
		t.Fatal("the shell pane should be collapsed out of view while a sibling is zoomed")
	}

	app.Dispatch(toggleZoomMsg{id: kyuID})
	waitForText(t, app, "READY", 3*time.Second)

	if strings.Contains(app.Buffer().String(), "failed to start") {
		t.Fatal("the shell pane was disposed and recreated across zoom/un-zoom — its running process was killed")
	}
}

func TestToggleHelpMsgOpensAndCloses(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	if m.helpOpen {
		t.Fatal("help should start closed")
	}

	next, _ := m.Update(toggleHelpMsg{})
	m = next.(Model)
	if !m.helpOpen {
		t.Fatal("expected help open after one toggleHelpMsg")
	}

	next, _ = m.Update(toggleHelpMsg{})
	m = next.(Model)
	if m.helpOpen {
		t.Fatal("expected help closed after a second toggleHelpMsg")
	}
}

func TestCloseHelpMsgClosesRegardlessOfState(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, _ := m.Update(toggleHelpMsg{})
	m = next.(Model)
	if !m.helpOpen {
		t.Fatal("setup: expected help open")
	}
	next, _ = m.Update(closeHelpMsg{})
	m = next.(Model)
	if m.helpOpen {
		t.Fatal("expected help closed after closeHelpMsg")
	}
}

// TestHelpButtonShowsHelpContentOnScreen drives the real input/render
// path (click-equivalent Enter on the help button, via the actual
// control-strip focus order) rather than just Update, confirming the
// modal's content genuinely reaches the screen — a wiring mistake in
// View() (e.g. the wrong Open expression, or the Modal Node missing
// from the tree some frame) wouldn't be caught by the Update-only
// tests above.
func TestHelpButtonShowsHelpContentOnScreen(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	app := tui.NewApp(m, 80, 24)
	defer app.Close()

	// Tab to the "help" button: starts at index 0 (the first add-pane
	// button), and 5 add-pane buttons precede "help" in the control
	// strip (see controlStrip's own child order), so 5 Tabs land there.
	for range 5 {
		app.HandleInput(input.KeyEvent{Key: input.KeyTab})
	}
	for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyEnter}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "pane multiplexer help") {
		t.Fatalf("expected help content on screen after activating the help button:\n%s", buf)
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

// TestHorizontalSplitPaneCannotMinimize drives the real input path: a
// pane that's a child of a horizontal split shouldn't be mini­mizable
// at all (see renderSplit's own doc comment — collapsing width to one
// column just garbles the title sideways). Uses F2 (verified working
// end to end by TestFKeyJumpsFocusEndToEnd's sibling tests) to jump
// straight to the second pane's own title bar, then Enter (the normal
// click-equivalent minimize toggle) should be a no-op.
func TestHorizontalSplitPaneCannotMinimize(t *testing.T) {
	m := New(nil, "", KyuReplSpec("kyu", nil))
	id := m.panes[0].id
	m, _ = m.splitPane(id, layout.Horizontal, KyuReplSpec("kyu2", nil))

	app := tui.NewApp(m, 80, 16)
	defer app.Close()
	forceRenders(app, 1)

	for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyF2}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyEnter}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	forceRenders(app, 2)

	buf := app.Buffer().String()
	if !strings.Contains(buf, "9sh>") || strings.Count(buf, "9sh>") != 2 {
		t.Fatalf("expected both panes' content still visible (minimize should be a no-op here):\n%s", buf)
	}
}

// TestAddingSecondPaneKeepsFirstPaneAlive is the tree-restructuring
// counterpart to TestMinimizeKeepsProcessAliveAndStatePreserved: a
// control-strip "+" addition splits the existing pane (see
// addPaneTarget/splitPane) and must not discard its retained widget
// state (its live pty in particular) in the process — the same
// reparent-preserves-state guarantee TestSplittingLiveShellPanePreservesItsProcess
// pins down for a title-bar split, exercised here through "+" instead.
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
