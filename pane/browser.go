package pane

import (
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

// browserNode renders a namespace-browser pane's content: a
// widget.List of the current directory's entries (".." prepended
// unless already at "/"), or the last listing error as a single row.
// List owns no navigation state itself — cursor and the listing are
// this package's own business state (paneState.browser*), threaded
// through Model.Update, per List's own "caller-owned cursor" contract.
func browserNode(p *paneState) tui.Node {
	id := p.id
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
