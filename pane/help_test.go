package pane

import (
	"testing"

	"github.com/sandgorgon/tui/input"
)

// TestHelpWidgetScrollsTowardsBottomOnPgDown guards the bug found
// writing TestHelpButtonShowsHelpContentOnScreen in model_test.go
// (2026-08-30): a first draft copied kyuReplWidget's bottom-anchored
// scrollOffset convention (right for a live transcript that should
// default to the latest output), which meant this static reference
// document opened already scrolled to its own last page instead of the
// top. scrollOffset here is a plain top-anchored offset (0 = start of
// the document, matching a pager like less/man), clamped to
// [0, len(helpText)-lastHeight].
func TestHelpWidgetScrollsTowardsBottomOnPgDown(t *testing.T) {
	w := &helpWidget{lastHeight: 5} // Paint sets this normally; fake it for a pure HandleEvent test
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset should start at 0 (top of the document), got %d", w.scrollOffset)
	}

	w.HandleEvent(input.KeyEvent{Key: input.KeyPgDown})
	if w.scrollOffset != 4 { // lastHeight - 1
		t.Fatalf("scrollOffset after one PgDown = %d, want 4", w.scrollOffset)
	}

	w.HandleEvent(input.KeyEvent{Key: input.KeyPgUp})
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after PgDown then PgUp = %d, want back to 0", w.scrollOffset)
	}

	// PgUp past 0 clamps rather than going negative.
	w.HandleEvent(input.KeyEvent{Key: input.KeyPgUp})
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after PgUp at 0 = %d, want clamped to 0", w.scrollOffset)
	}
}

func TestHelpWidgetEscQAndQuestionMarkClose(t *testing.T) {
	for _, ev := range []input.KeyEvent{
		{Key: input.KeyEsc},
		{Rune: '?'},
		{Rune: 'q'},
	} {
		w := &helpWidget{}
		cmd := w.HandleEvent(ev)
		if cmd == nil {
			t.Fatalf("expected a Cmd for %v", ev)
		}
		if _, ok := cmd().(closeHelpMsg); !ok {
			t.Fatalf("Cmd for %v produced %T, want closeHelpMsg", ev, cmd())
		}
	}
}

// TestHelpWidgetPaintClampsScrollAtBottom checks the pure clamp math
// Paint's own start computation relies on — Paint itself needs a real
// *cell.Painter, exercised via TestHelpButtonShowsHelpContentOnScreen's
// tui.App integration test in model_test.go instead (matching this
// package's established split between unit-level field checks and
// real-render integration checks — see e.g.
// TestKyuReplPgUpPgDownAdjustScrollOffset vs.
// TestKyuReplPgUpScrollsTranscript for the same pattern).
func TestHelpWidgetPaintClampsScrollAtBottom(t *testing.T) {
	const height = 5
	maxStart := max0(len(helpText) - height)
	if got := clampInt(len(helpText)*10, 0, maxStart); got != maxStart {
		t.Fatalf("clampInt(wildly-past-the-end, 0, %d) = %d, want %d", maxStart, got, maxStart)
	}
	if got := clampInt(-5, 0, maxStart); got != 0 {
		t.Fatalf("clampInt(negative, 0, %d) = %d, want 0", maxStart, got)
	}
}
