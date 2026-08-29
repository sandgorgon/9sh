package pane

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/style"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"

	"github.com/sandgorgon/9sh/session"
)

// sessionViewerLimit caps how many history records a viewer pane pulls
// per refresh — a glance-able recent-activity list, not a full-history
// browser/diff view (the design doc's "9vcs log/diff viewer" framing);
// paging or a real 9vcs-log-shaped view is future work if this turns
// out not to be enough.
const sessionViewerLimit = 200

// sessionViewerNode renders a session-history pane: the most recent
// completed jobs (see session.ReadRecent), newest first. Refreshed on
// demand (Enter or 'r'), the same discipline jobViewerNode uses and for
// the same reason — real I/O in a Cmd, no polling loop.
func sessionViewerNode(p *paneState) tui.Node {
	id := p.id
	items := p.sessionRows
	if p.sessionErr != "" {
		items = []string{"error: " + p.sessionErr}
	}
	if len(items) == 0 {
		items = []string{"(no session history — press enter or r to refresh)"}
	}
	return widget.List(items, p.sessionCursor, widget.ListOptions{Theme: style.DefaultDark()},
		func(e input.Event) tui.Msg {
			switch ev := e.(type) {
			case input.KeyEvent:
				switch {
				case ev.Key == input.KeyUp:
					return sessionViewerMoveMsg{id: id, delta: -1}
				case ev.Key == input.KeyDown:
					return sessionViewerMoveMsg{id: id, delta: 1}
				case ev.Key == input.KeyEnter, ev.Rune == 'r':
					return sessionViewerRefreshRequestedMsg{id: id}
				}
			}
			return nil
		}).Key(paneKey(id, "sessionviewer"))
}

type sessionViewerMoveMsg struct {
	id    int
	delta int
}
type sessionViewerRefreshRequestedMsg struct{ id int }
type sessionListedMsg struct {
	id   int
	rows []string
	err  error
}

// listSessionCmd is the session viewer's real I/O (disk reads under
// dir) — a Cmd, never directly in Update. dir is p.sessionDir, which
// is empty when session history wasn't available at startup at all
// (see cmd/9sh's bootstrapSession) — that's reported as this pane's
// error, not a panic or a silent empty list indistinguishable from
// "no history yet".
func listSessionCmd(id int, dir string) tui.Cmd {
	return func() tui.Msg {
		if dir == "" {
			return sessionListedMsg{id: id, err: errors.New("session history isn't available this run (no 9vcs on PATH, or no home directory, at startup)")}
		}
		recs, err := session.ReadRecent(dir, sessionViewerLimit)
		if err != nil {
			return sessionListedMsg{id: id, err: err}
		}
		rows := make([]string, len(recs))
		for i, r := range recs {
			rows[i] = formatSessionRow(r)
		}
		return sessionListedMsg{id: id, rows: rows}
	}
}

func formatSessionRow(rec session.Record) string {
	ts := rec.TSEnd
	if ts.IsZero() {
		ts = rec.TSStart
	}
	when := "?"
	if !ts.IsZero() {
		when = ts.Local().Format("2006-01-02 15:04:05")
	}
	status := "?"
	switch {
	case rec.Signal != "":
		status = "sig:" + rec.Signal
	case rec.Exit != nil:
		status = fmt.Sprintf("exit:%d", *rec.Exit)
	}
	argv := strings.Join(rec.Argv, " ")
	if argv == "" {
		argv = "(" + rec.Kind + ")"
	}
	return fmt.Sprintf("%s  %-10s %-10s %s", when, rec.Host, status, argv)
}
