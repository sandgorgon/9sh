package pane

import (
	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"
)

// flatFocusable is a single-line, border-free alternative to
// tui.Focusable. tui.Focusable always reserves a 1-cell border on every
// side around its child (a deliberate, documented v1 placeholder — see
// tui/focus.go), which needs at least 2 rows of height before the
// child has any room to paint at all, and 3 to show one line of text
// with the border still visible. 9sh's control-strip buttons and pane
// title bars are each only layout.Length(1) tall by design (a compact
// single-line toolbar/title, not a bordered button) — wrapping them in
// tui.Focusable was silently clipping their label to nothing. This was
// a real, invisible-in-headless-testing bug: it only surfaced by
// driving a real build under a pty and finding the control strip and
// every pane's title bar rendered as blank rows.
//
// This keeps Tab/Shift-Tab focusability and click/Enter activation,
// using a background-filled bar (see Model.barStyle) rather than a
// border to show focus — Paint fills the whole cell with style(), not
// just the label's own width, which is what makes it read as a
// continuous bar instead of colored text floating on the terminal's
// own background.
func flatFocusable(key any, label string, style func(focused bool) cell.Style, onEvent func(input.Event) tui.Msg) tui.Node {
	return tui.Component(key, flatFocusableProps{label: label, style: style, onEvent: onEvent}, func() tui.Widget {
		return &flatFocusableWidget{}
	})
}

type flatFocusableProps struct {
	label   string
	style   func(focused bool) cell.Style
	onEvent func(input.Event) tui.Msg
}

type flatFocusableWidget struct {
	label   string
	style   func(focused bool) cell.Style
	onEvent func(input.Event) tui.Msg
	focused bool
}

func (w *flatFocusableWidget) Reconcile(props any) bool {
	p := props.(flatFocusableProps)
	changed := w.label != p.label
	w.label, w.style, w.onEvent = p.label, p.style, p.onEvent
	return changed
}

func (w *flatFocusableWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	st := w.style(w.focused)
	// Fill the whole cell first, not just the label's own width — this
	// is what makes it read as a background-colored bar (see barStyle)
	// rather than colored text floating on the terminal's own
	// background, with no seam past the label.
	p.Fill(0, 0, width, height, ' ', st)
	p.Text(0, 0, w.label, st)
}

func (w *flatFocusableWidget) HandleEvent(e input.Event) tui.Cmd {
	if w.onEvent == nil {
		return nil
	}
	msg := w.onEvent(e)
	if msg == nil {
		return nil
	}
	return func() tui.Msg { return msg }
}

func (w *flatFocusableWidget) Focusable() bool         { return true }
func (w *flatFocusableWidget) SetFocused(focused bool) { w.focused = focused }

// barFill paints a plain background-filled Node with no label and no
// interaction — for the control strip's own trailing space, past its
// last button, so the bar's background spans the full pane width.
// tui.Text("", style) looks like it should do this but doesn't: Text's
// Paint only iterates the string's own runes, so an empty string
// touches zero cells, leaving that whole region at the terminal's
// plain background — a real, visible seam past "quit".
func barFill(style cell.Style) tui.Node {
	return tui.Component("bar-fill", style, func() tui.Widget {
		return &barFillWidget{}
	})
}

type barFillWidget struct{ style cell.Style }

func (w *barFillWidget) Reconcile(props any) bool {
	w.style = props.(cell.Style)
	return true
}

func (w *barFillWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	p.Fill(0, 0, width, height, ' ', w.style)
}

func (w *barFillWidget) HandleEvent(input.Event) tui.Cmd { return nil }
func (w *barFillWidget) Focusable() bool                 { return false }
func (w *barFillWidget) SetFocused(bool)                 {}
