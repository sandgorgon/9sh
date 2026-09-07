package pane

import (
	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/style"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"
)

type browserMoveMsg struct {
	id    int
	delta int
}
type browserEnterMsg struct{ id int }
type browserClickMsg struct {
	id    int
	index int
}
type browserUpMsg struct{ id int }
type browserPreviewCloseMsg struct{ id int }

// browserNode renders a namespace-browser pane's content: while
// p.browserPreviewPath is set, the selected file's content (see
// browserPreviewNode below); otherwise a widget.List of the current
// directory's entries (".." prepended unless already at "/"), or the
// last listing error as a single row. List owns no navigation state
// itself — cursor and the listing are this package's own business
// state (paneState.browser*), threaded through Model.Update, per
// List's own "caller-owned cursor" contract.
func browserNode(p *paneState) tui.Node {
	id := p.id
	if p.browserPreviewPath != "" {
		lines := p.browserPreviewLines
		if p.browserPreviewErr != "" {
			lines = []string{"error: " + p.browserPreviewErr}
		}
		return browserPreviewNode(id, p.browserPreviewPath, lines)
	}

	items := p.browserEntries
	if p.browserErr != "" {
		items = []string{"error: " + p.browserErr}
	}
	if len(items) == 0 {
		items = []string{"(empty)"}
	}

	return widget.List(items, p.browserCursor, widget.ListOptions{Theme: style.DefaultDark()},
		func(e input.Event) tui.Msg {
			switch ev := e.(type) {
			case input.KeyEvent:
				switch ev.Key {
				case input.KeyUp:
					return browserMoveMsg{id: id, delta: -1}
				case input.KeyDown:
					return browserMoveMsg{id: id, delta: 1}
				case input.KeyEnter:
					return browserEnterMsg{id: id}
				case input.KeyBackspace:
					return browserUpMsg{id: id}
				}
			case input.MouseEvent:
				if ev.Button == input.MouseLeft && !ev.Drag {
					return browserClickMsg{id: id, index: ev.Y}
				}
			}
			return nil
		}).Key(paneKey(id, "browser"))
}

// browserPreviewProps is browserPreviewWidget's Reconcile input — id
// (for the Msg its own key handling returns) plus the content to
// render; lines is recomputed by browserNode every frame from
// paneState, not retained here.
type browserPreviewProps struct {
	id    int
	path  string
	lines []string
}

// browserPreviewNode is a plain, read-only scrollable text viewer for
// one namespace file's content — the browse pane's answer to kyu's
// cat(path), reached by Enter/click on a file entry instead of a
// directory one. Deliberately keyed apart from "browser" (the List
// above): swapping between listing and preview is a real mode change,
// not incremental state within one widget, so tui disposes/recreates
// rather than reconciling in place — matching how a plain widget.List
// vs. this Component were never going to share retained state anyway.
//
// Modeled directly on help.go's helpWidget (same top-anchored
// scrollOffset, same PgUp/PgDown/wheel handling) rather than sharing
// code with it: one browse pane can have its own preview open
// independently of any other, so this needs to be a per-pane Component
// keyed by id, where helpWidget is a single app-wide instance behind
// one widget.Modal — different enough in shape that factoring out the
// handful of scroll-math lines they'd share isn't worth it.
func browserPreviewNode(id int, path string, lines []string) tui.Node {
	if len(lines) == 0 {
		lines = []string{"(empty)"}
	}
	return tui.Component(paneKey(id, "browserpreview"), browserPreviewProps{id: id, path: path, lines: lines}, func() tui.Widget {
		return &browserPreviewWidget{}
	}).Key(paneKey(id, "browserpreview"))
}

type browserPreviewWidget struct {
	props        browserPreviewProps
	scrollOffset int
	lastHeight   int
}

func (w *browserPreviewWidget) Reconcile(props any) bool {
	w.props = props.(browserPreviewProps)
	return true
}

func (w *browserPreviewWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	if width <= 0 || height <= 0 {
		return
	}
	w.lastHeight = height
	lines := w.props.lines
	maxStart := max0(len(lines) - height)
	start := clampInt(w.scrollOffset, 0, maxStart)
	end := min(start+height, len(lines))
	for y, line := range lines[start:end] {
		p.Text(0, y, line, cell.Style{})
	}
}

func (w *browserPreviewWidget) HandleEvent(e input.Event) tui.Cmd {
	id := w.props.id
	switch ev := e.(type) {
	case input.MouseEvent:
		switch ev.Button {
		case input.MouseWheelUp:
			w.scrollOffset = max0(w.scrollOffset - scrollStep)
		case input.MouseWheelDown:
			w.scrollOffset += scrollStep
		}
	case input.KeyEvent:
		switch ev.Key {
		case input.KeyEsc, input.KeyBackspace:
			// Mirrors browserUpMsg's own Backspace-in-listing meaning
			// ("go up a level") — a preview is one level below the
			// listing it was opened from, so this returns to that
			// listing rather than climbing to the parent directory.
			return func() tui.Msg { return browserPreviewCloseMsg{id: id} }
		case input.KeyPgUp:
			w.scrollOffset = max0(w.scrollOffset - max0(w.lastHeight-1))
		case input.KeyPgDown:
			w.scrollOffset += max0(w.lastHeight - 1)
		}
	}
	return nil
}

func (w *browserPreviewWidget) Focusable() bool { return true }
func (w *browserPreviewWidget) SetFocused(bool) {}
