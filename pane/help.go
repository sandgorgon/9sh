package pane

import (
	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"
)

// helpText is the built-in help screen's content — kept as plain data
// (not generated from the hotkey-handling code it documents) so it can
// be scanned and edited on its own; keep it in sync by hand whenever a
// binding changes elsewhere in this package (controlStrip, paneNode's
// title-bar switch, kyuReplWidget.handleKey).
var helpText = []string{
	"9sh — pane multiplexer help",
	"",
	"Control strip (always visible, top row):",
	"  + shell / + kyu / + browse / + jobs / + history   add a pane of that kind",
	"  theme                                              toggle light/dark",
	"  quit                                               quit 9sh",
	"",
	"Every pane's title bar:",
	"  x              close this pane",
	"  d              split down (then pick a kind: s/k/b/j/h, or anything",
	"                 else to cancel)",
	"  r              split right (same kind-picker as d)",
	"  z              zoom/un-zoom this pane to fill the whole screen",
	"  + / -          resize this pane along its split axis, down to one",
	"                 visible content line — smaller than that, minimize",
	"                 instead",
	"  click/Enter    minimize/restore (only along a vertical split axis)",
	"  F1-F9          jump keyboard focus straight to pane N",
	"",
	"Inside a kyu REPL pane, once its content (not just its title bar)",
	"has focus:",
	"  Enter                  submit, or continue if brackets are still open",
	"  Left/Right              move the cursor; Ctrl+Left/Right by word",
	"  Home/End (Ctrl+A/E)     jump to the start/end of the current line",
	"  Backspace/Delete        delete before/after the cursor",
	"  Ctrl+W                  delete the word before the cursor",
	"  Ctrl+U / Ctrl+K          delete to line start / delete to line end",
	"  Up/Down                  recall previous/next submitted input",
	"                           (only when not mid multi-line input)",
	"  PgUp/PgDown, mouse wheel  scroll the transcript",
	"  Ctrl+C                   copy the whole transcript",
	"  Alt+C                    copy only what's currently visible",
	"  paste                    inserts at the cursor",
	"",
	"Inside a shell pane, once its content has focus: Tab reaches the",
	"hosted shell directly (real completion); Ctrl+\\ releases focus back",
	"to pane navigation.",
	"",
	"This help: PgUp/PgDown or the wheel to scroll; Esc, '?', or a click",
	"outside this box to close.",
}

// helpNode is the built-in help screen's body — see toggleHelpMsg/
// closeHelpMsg in model.go for how it opens/closes (a widget.Modal
// wrapping this, in Model.View), and helpText above for its content.
func helpNode() tui.Node {
	return tui.Component("help-content", struct{}{}, func() tui.Widget {
		return &helpWidget{}
	})
}

// helpWidget is a plain, read-only scrollable text viewer — no editing,
// unlike kyuReplWidget, which this deliberately doesn't share code
// with: the scroll math is a handful of lines, and sharing it would
// mean generalizing kyuReplWidget for one caller that needs a third of
// what it does. Also deliberately not the same *direction* of scroll
// math: kyuReplWidget's scrollOffset counts back from the bottom
// (right, for a live transcript that should default to showing the
// latest output); this is a static reference document, which should
// default to showing the *top* — scrollOffset here is a plain
// top-anchored offset instead (0 = start of the document), same
// convention a pager like less or man uses.
type helpWidget struct {
	scrollOffset int
	lastHeight   int
}

func (w *helpWidget) Reconcile(props any) bool { return true }

func (w *helpWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	if width <= 0 || height <= 0 {
		return
	}
	w.lastHeight = height
	maxStart := max0(len(helpText) - height)
	start := clampInt(w.scrollOffset, 0, maxStart)
	end := min(start+height, len(helpText))
	for y, line := range helpText[start:end] {
		p.Text(0, y, line, cell.Style{})
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (w *helpWidget) HandleEvent(e input.Event) tui.Cmd {
	switch ev := e.(type) {
	case input.MouseEvent:
		switch ev.Button {
		case input.MouseWheelUp:
			w.scrollOffset = max0(w.scrollOffset - scrollStep)
		case input.MouseWheelDown:
			w.scrollOffset += scrollStep
		}
	case input.KeyEvent:
		switch {
		case ev.Key == input.KeyEsc, ev.Rune == '?', ev.Rune == 'q':
			// Safe to claim these here (unlike a global hotkey elsewhere
			// in this package — see controlStrip's own doc comment on
			// why '?' isn't bound globally to *open* help): widget.Modal
			// claims focus exclusively for its body while open, so
			// nothing else could receive this key instead.
			return func() tui.Msg { return closeHelpMsg{} }
		case ev.Key == input.KeyPgUp:
			w.scrollOffset = max0(w.scrollOffset - max0(w.lastHeight-1))
		case ev.Key == input.KeyPgDown:
			w.scrollOffset += max0(w.lastHeight - 1)
		}
	}
	return nil
}

func (w *helpWidget) Focusable() bool { return true }
func (w *helpWidget) SetFocused(bool) {}
