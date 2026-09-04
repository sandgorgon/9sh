package pane

import (
	"testing"

	"github.com/sandgorgon/tui/tui"
)

// TestTitleBarStaysHighlightedWhenContentHasFocus exercises the real
// tui.FocusAware integration (tui v0.5.0+, sandgorgon/tui#24/#25): a
// pane's title bar should stay highlighted while focus is on any of
// that pane's own widgets, content included, not only while the title
// bar is itself the literal tab-focused widget.
//
// Drives the real synchronous App.SetFocus path, which calls
// App.render(), which in turn calls Model.SetFocusedKey right before
// View() — unlike F-key-style SetFocusCmd/FocusMsg, which (per
// TestFKeyJumpsFocusEndToEnd's doc comment) can't be driven headlessly
// at all, since that indirection is resolved inside Run()'s own private
// loop, not Dispatch/HandleInput.
func TestTitleBarStaysHighlightedWhenContentHasFocus(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil), KyuReplSpec("b", nil))
	app := tui.NewApp(m, 80, 16)
	defer app.Close()

	// Same index arithmetic TestFKeyJumpsFocusEndToEnd already relies
	// on: controlStripFocusables, then pane a's title (+0), then its
	// content (+1).
	contentIdx := controlStripFocusables + 1
	if ok := app.SetFocus(contentIdx); !ok {
		t.Fatalf("SetFocus(%d) (pane a's content) failed", contentIdx)
	}

	buf := app.Buffer()
	row1Bg := buf.At(5, 1).Style.Bg // row 1: pane a's title bar (row 0 is the control strip)
	if row1Bg != m.theme.Focus {
		t.Errorf("pane a's title bar Bg = %v while its content has focus, want theme.Focus %v", row1Bg, m.theme.Focus)
	}
}

// TestTitleBarUnfocusedWhenNoPaneHasFocus is the control case: with
// focus on the control strip (index 0, never any pane), no pane's
// title bar should show theme.Focus.
func TestTitleBarUnfocusedWhenNoPaneHasFocus(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil))
	app := tui.NewApp(m, 80, 16)
	defer app.Close()

	if ok := app.SetFocus(0); !ok { // first control-strip button
		t.Fatal("SetFocus(0) failed")
	}

	buf := app.Buffer()
	got := buf.At(5, 1).Style.Bg
	if got != m.theme.Border {
		t.Errorf("pane a's title bar Bg = %v with focus on the control strip, want theme.Border %v", got, m.theme.Border)
	}
}

// TestTitleBarTracksWhichPaneNotJustAnyFocus confirms paneHasFocus
// actually distinguishes between panes (a prefix-matching bug — e.g.
// matching on any non-empty focusedKey instead of this pane's own
// "pane-<id>-" prefix — would make every pane's title bar light up
// together): with focus on pane b's content, pane a's title bar (a
// fixed screen position, row 1, regardless of where pane b's own title
// bar lands) must stay theme.Border, not theme.Focus.
func TestTitleBarTracksWhichPaneNotJustAnyFocus(t *testing.T) {
	m := New(nil, "", KyuReplSpec("a", nil), KyuReplSpec("b", nil))
	app := tui.NewApp(m, 80, 16)
	defer app.Close()

	// Pane b is the 2nd pane: title at controlStripFocusables+2, content
	// at +3 — same "+2 per pane, title then content" arithmetic
	// TestFKeyJumpsFocusEndToEnd and TestFKeyRequestsFocusAtComputedIndex
	// already rely on.
	paneBContentIdx := controlStripFocusables + 3
	if ok := app.SetFocus(paneBContentIdx); !ok {
		t.Fatalf("SetFocus(%d) (pane b's content) failed", paneBContentIdx)
	}

	got := app.Buffer().At(5, 1).Style.Bg // row 1: pane a's title bar
	if got != m.theme.Border {
		t.Errorf("pane a's title bar Bg = %v while pane b's content has focus, want theme.Border %v (pane a should be unaffected)", got, m.theme.Border)
	}
}
