// Package pane is a minimizable multi-pane terminal multiplexer built
// on tui's retained widget.Terminal: each pane hosts a real pty-attached
// process, stacked vertically, with its own always-visible title-bar
// row that toggles it between full size and collapsed-to-title-bar.
//
// Minimizing is a click/Enter action on that title bar, not a global
// hotkey: tui.App delivers every key to both Model.Update and the
// focused widget at once, with no way to suppress the latter — a
// hotkey pressed while a Terminal pane is focused would be forwarded
// straight into the hosted shell right alongside whatever Update did
// with it. A pane's Node keeps an explicit, stable key (its pane ID)
// at every level of its subtree, from the pane-list Box's child down
// through Terminal itself: reconcile.go's key matching is scoped
// per-parent, so an unkeyed ancestor whose position shifts would
// discard and rebuild everything beneath it — including a live
// Terminal's pty — even though the Terminal node itself carried a key.
package pane

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/layout"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"
)

// Spec describes a pane to create, at startup (New) or later (AddPane).
type Spec struct {
	Title   string
	Command *exec.Cmd
}

// ShellSpec is a convenience Spec running the user's $SHELL (/bin/sh if
// unset).
func ShellSpec(title string) Spec {
	return Spec{Title: title, Command: exec.Command(shellPath())}
}

func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

type paneState struct {
	id        int
	title     string
	command   *exec.Cmd
	minimized bool
	exited    bool
	exitErr   error
}

// Model is the pane multiplexer's tui.Model.
type Model struct {
	panes  []*paneState
	nextID int
}

// New builds a Model seeded with the given panes.
func New(specs ...Spec) Model {
	var m Model
	for _, s := range specs {
		m = m.withNewPane(s)
	}
	return m
}

func (m Model) withNewPane(s Spec) Model {
	m.nextID++
	m.panes = append(m.panes, &paneState{id: m.nextID, title: s.Title, command: s.Command})
	return m
}

func (m Model) Init() tui.Cmd { return nil }

type toggleMinimizeMsg struct{ id int }
type paneExitedMsg struct {
	id  int
	err error
}
type addPaneMsg struct{ spec Spec }
type quitRequestedMsg struct{}

// AddPane returns a Msg that adds a new pane — usable from outside this
// package (e.g. a future kyu-level "open a pane" integration) via
// whatever Cmd/message-injection path the host App exposes.
func AddPane(s Spec) tui.Msg { return addPaneMsg{spec: s} }

func (m Model) Update(msg tui.Msg) (tui.Model, tui.Cmd) {
	switch mm := msg.(type) {
	case toggleMinimizeMsg:
		if p := m.find(mm.id); p != nil {
			p.minimized = !p.minimized
		}
	case paneExitedMsg:
		if p := m.find(mm.id); p != nil {
			p.exited = true
			p.exitErr = mm.err
		}
	case addPaneMsg:
		return m.withNewPane(mm.spec), nil
	case quitRequestedMsg:
		return m, tui.Quit()
	}
	return m, nil
}

func (m Model) find(id int) *paneState {
	for _, p := range m.panes {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (m Model) View() tui.Node {
	var paneChildren []tui.BoxChild
	for _, p := range m.panes {
		constraint := layout.Fill(1)
		if p.minimized {
			constraint = layout.Length(1)
		}
		paneChildren = append(paneChildren, tui.Child(constraint, m.paneNode(p)))
	}
	paneList := tui.Box(layout.Vertical, paneChildren...).Key("pane-list")

	return tui.Box(layout.Vertical,
		tui.Child(layout.Length(1), controlStrip()),
		tui.Child(layout.Fill(1), paneList),
	)
}

func controlStrip() tui.Node {
	return tui.Box(layout.Horizontal,
		tui.Child(layout.Length(14), newPaneButton()),
		tui.Child(layout.Length(8), quitButton()),
		tui.Child(layout.Fill(1), tui.Text("", cell.Style{})),
	).Key("control-strip")
}

func newPaneButton() tui.Node {
	return tui.Focusable("new-pane-btn", tui.Text(" + new pane ", cell.Style{Attr: cell.AttrBold}),
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return addPaneMsg{spec: ShellSpec("shell")}
		})
}

func quitButton() tui.Node {
	return tui.Focusable("quit-btn", tui.Text(" quit ", cell.Style{Attr: cell.AttrBold}),
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return quitRequestedMsg{}
		})
}

// clicked reports whether e is the "activate" gesture for a button-like
// control: Enter/Space from the keyboard, or a plain (non-drag) left
// click — the same me.Button == input.MouseLeft && !me.Drag idiom
// widget.TextInput/TextArea/Table use for their own click handling.
func clicked(e input.Event) bool {
	switch ev := e.(type) {
	case input.KeyEvent:
		return ev.Key == input.KeyEnter || ev.Rune == ' '
	case input.MouseEvent:
		return ev.Button == input.MouseLeft && !ev.Drag
	}
	return false
}

func (m Model) paneNode(p *paneState) tui.Node {
	id := p.id

	chevron := "▾ "
	if p.minimized {
		chevron = "▸ "
	}
	label := chevron + p.title
	if p.exited {
		label += " (exited)"
	}
	titleBar := tui.Focusable(paneKey(id, "title"), tui.Text(label, titleStyle(p)),
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return toggleMinimizeMsg{id: id}
		})

	term := widget.Terminal(widget.TerminalOptions{
		Command: p.command,
		OnExit:  func(err error) tui.Msg { return paneExitedMsg{id: id, err: err} },
		// A pane hosts a real shell — tab-completion has to work, so
		// Tab must reach it rather than being intercepted for focus
		// navigation. ReleaseKey is left at its default (Ctrl+\), the
		// way out to Tab-navigate title bars/buttons again.
		WantsRawTab: true,
	}).Key(paneKey(id, "term"))

	return tui.Box(layout.Vertical,
		tui.Child(layout.Length(1), titleBar),
		tui.Child(layout.Fill(1), term),
	).Key(paneKey(id, "box"))
}

func paneKey(id int, part string) string {
	return fmt.Sprintf("pane-%d-%s", id, part)
}

func titleStyle(p *paneState) cell.Style {
	if p.exited {
		return cell.Style{Fg: cell.ANSIColor(1), Attr: cell.AttrBold}
	}
	return cell.Style{Attr: cell.AttrBold, Underline: cell.UnderlineSingle}
}
