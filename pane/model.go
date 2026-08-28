// Package pane is a minimizable multi-pane multiplexer built on tui's
// retained widget.Terminal and 9sh's own native widgets, stacked
// vertically, each with an always-visible title-bar row that toggles it
// between full size and collapsed-to-title-bar.
//
// Three pane kinds exist: KindShell hosts a real pty-attached process
// (a shell, or anything else exec'able) via widget.Terminal; KindKyuRepl
// hosts a native kyu REPL (kyurepl.go) evaluating against the same
// shared *eval.Env as every other entry point into 9sh (see cmd/9sh's
// bootstrap); KindNamespaceBrowser (browser.go) is a live, navigable
// view of whatever's bound in that Env's namespace — the first of the
// namespace-aware "differentiator" panes the project's design notes
// call for (a job viewer spanning /jobs + /n/<host>/jobs, and a 9vcs-
// backed session viewer, are follow-ups once their own prerequisite
// phases exist).
//
// Minimizing is a click/Enter action on a pane's title bar, not a
// global hotkey: tui.App delivers every key to both Model.Update and
// the focused widget at once, with no way to suppress the latter — a
// hotkey pressed while a Terminal pane is focused would be forwarded
// straight into the hosted shell right alongside whatever Update did
// with it. A pane's Node keeps an explicit, stable key (its pane ID)
// at every level of its subtree, from the pane-list Box's child down
// through its content widget: reconcile.go's key matching is scoped
// per-parent, so an unkeyed ancestor whose position shifts would
// discard and rebuild everything beneath it — including a live
// Terminal's pty — even though the leaf node itself carried a key.
package pane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/layout"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/ns"
)

type Kind int

const (
	KindShell Kind = iota
	KindKyuRepl
	KindNamespaceBrowser
)

// Spec describes a pane to create, at startup (New) or later (AddPane).
type Spec struct {
	Title   string
	Kind    Kind
	Command *exec.Cmd // KindShell only
	Env     *eval.Env // KindKyuRepl and KindNamespaceBrowser
}

// ShellSpec is a convenience Spec running the user's $SHELL (/bin/sh if
// unset).
func ShellSpec(title string) Spec {
	return Spec{Title: title, Kind: KindShell, Command: exec.Command(shellPath())}
}

// KyuReplSpec is a native kyu REPL pane evaluating against env.
func KyuReplSpec(title string, env *eval.Env) Spec {
	return Spec{Title: title, Kind: KindKyuRepl, Env: env}
}

// NamespaceBrowserSpec is a live namespace browser rooted at "/" in
// env's namespace.
func NamespaceBrowserSpec(title string, env *eval.Env) Spec {
	return Spec{Title: title, Kind: KindNamespaceBrowser, Env: env}
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
	kind      Kind
	minimized bool
	exited    bool
	exitErr   error

	command *exec.Cmd // KindShell
	env     *eval.Env // KindKyuRepl, KindNamespaceBrowser

	// KindNamespaceBrowser's own business state — List's cursor (and,
	// by the same convention here, the current path and listing) is
	// caller-owned, not retained inside the widget; see browser.go.
	browserPath    string
	browserEntries []string
	browserCursor  int
	browserErr     string
}

// Model is the pane multiplexer's tui.Model.
type Model struct {
	panes  []*paneState
	nextID int
	env    *eval.Env // shared with every pane's spec that needs one, for the "+" buttons
}

// New builds a Model seeded with the given panes. env is used to build
// new kyu-repl/namespace-browser panes from the control strip's "+"
// buttons — pass the same *eval.Env used elsewhere in the process (see
// cmd/9sh's bootstrap) so kyu state is shared, not duplicated per pane.
func New(env *eval.Env, specs ...Spec) Model {
	m := Model{env: env}
	for _, s := range specs {
		m = m.withNewPane(s)
	}
	return m
}

func (m Model) withNewPane(s Spec) Model {
	m.nextID++
	m.panes = append(m.panes, &paneState{
		id: m.nextID, title: s.Title, kind: s.Kind,
		command: s.Command, env: s.Env, browserPath: "/",
	})
	return m
}

// Init kicks off the initial namespace listing for any seed panes that
// are namespace browsers — listDirCmd does real I/O (a namespace Walk/
// Read), which belongs in a Cmd, not directly in Update/View.
func (m Model) Init() tui.Cmd {
	var cmds []tui.Cmd
	for _, p := range m.panes {
		if p.kind == KindNamespaceBrowser {
			cmds = append(cmds, listDirCmd(p.id, p.env.Namespace(), p.browserPath))
		}
	}
	return tui.Batch(cmds...)
}

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
		next := m.withNewPane(mm.spec)
		if mm.spec.Kind == KindNamespaceBrowser {
			newPane := next.panes[len(next.panes)-1]
			return next, listDirCmd(newPane.id, mm.spec.Env.Namespace(), newPane.browserPath)
		}
		return next, nil
	case quitRequestedMsg:
		return m, tui.Quit()
	case browserMoveMsg:
		if p := m.find(mm.id); p != nil {
			p.browserCursor = clamp(p.browserCursor+mm.delta, 0, max0(len(p.browserEntries)-1))
		}
	case browserEnterMsg:
		if p := m.find(mm.id); p != nil {
			return m, m.browserEnter(p, p.browserCursor)
		}
	case browserClickMsg:
		if p := m.find(mm.id); p != nil {
			p.browserCursor = clamp(mm.index, 0, max0(len(p.browserEntries)-1))
			return m, m.browserEnter(p, p.browserCursor)
		}
	case browserUpMsg:
		if p := m.find(mm.id); p != nil && p.browserPath != "/" {
			newPath := parentNSPath(p.browserPath)
			p.browserPath, p.browserCursor = newPath, 0
			return m, listDirCmd(mm.id, p.env.Namespace(), newPath)
		}
	case browserListedMsg:
		if p := m.find(mm.id); p != nil && p.browserPath == mm.path {
			p.browserErr = ""
			p.browserEntries = mm.names
			if mm.err != nil {
				p.browserErr = mm.err.Error()
				p.browserEntries = nil
			}
		}
	}
	return m, nil
}

// browserEnter treats entries[idx] as selected: ".." goes up, a
// directory (trailing "/") descends, a plain file is a no-op for v1
// (no file-content view yet).
func (m Model) browserEnter(p *paneState, idx int) tui.Cmd {
	if idx < 0 || idx >= len(p.browserEntries) {
		return nil
	}
	selected := p.browserEntries[idx]
	if selected == ".." {
		newPath := parentNSPath(p.browserPath)
		p.browserPath, p.browserCursor = newPath, 0
		return listDirCmd(p.id, p.env.Namespace(), newPath)
	}
	if !strings.HasSuffix(selected, "/") {
		return nil
	}
	newPath := joinNSPath(p.browserPath, strings.TrimSuffix(selected, "/"))
	p.browserPath, p.browserCursor = newPath, 0
	return listDirCmd(p.id, p.env.Namespace(), newPath)
}

func (m Model) find(id int) *paneState {
	for _, p := range m.panes {
		if p.id == id {
			return p
		}
	}
	return nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func joinNSPath(base, name string) string {
	if base == "/" {
		return "/" + name
	}
	return base + "/" + name
}

func parentNSPath(p string) string {
	trimmed := strings.TrimSuffix(p, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx <= 0 {
		return "/"
	}
	return trimmed[:idx]
}

// ---- namespace listing: real I/O, so it's a Cmd, not done in Update ----

type browserListedMsg struct {
	id    int
	path  string
	names []string
	err   error
}

func listDirCmd(id int, namespace *ns.Namespace, path string) tui.Cmd {
	return func() tui.Msg {
		ctx := context.Background()
		root, err := namespace.Attach(ctx, "9sh", "")
		if err != nil {
			return browserListedMsg{id: id, path: path, err: err}
		}
		f := root
		for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
			if part == "" {
				continue
			}
			f, err = f.Walk(ctx, part)
			if err != nil {
				return browserListedMsg{id: id, path: path, err: err}
			}
		}
		entries, err := ns.ReadDirEntries(ctx, f)
		if err != nil {
			return browserListedMsg{id: id, path: path, err: err}
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
			if e.Qid.IsDir() {
				names[i] += "/"
			}
		}
		sort.Strings(names)
		if path != "/" {
			names = append([]string{".."}, names...)
		}
		return browserListedMsg{id: id, path: path, names: names}
	}
}

// ---- view ----

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
		tui.Child(layout.Length(1), m.controlStrip()),
		tui.Child(layout.Fill(1), paneList),
	)
}

func (m Model) controlStrip() tui.Node {
	return tui.Box(layout.Horizontal,
		tui.Child(layout.Length(10), addPaneButton("+ shell", ShellSpec("shell"))),
		tui.Child(layout.Length(8), addPaneButton("+ kyu", KyuReplSpec("kyu", m.env))),
		tui.Child(layout.Length(11), addPaneButton("+ browse", NamespaceBrowserSpec("browse", m.env))),
		tui.Child(layout.Length(8), quitButton()),
		tui.Child(layout.Fill(1), tui.Text("", cell.Style{})),
	).Key("control-strip")
}

func addPaneButton(label string, spec Spec) tui.Node {
	return tui.Focusable("btn-"+label, tui.Text(" "+label+" ", cell.Style{Attr: cell.AttrBold}),
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return addPaneMsg{spec: spec}
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
	label := chevron + paneLabel(p)
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

	var content tui.Node
	switch p.kind {
	case KindKyuRepl:
		content = kyuReplNode(id, p.env)
	case KindNamespaceBrowser:
		content = browserNode(p)
	default: // KindShell
		content = widget.Terminal(widget.TerminalOptions{
			Command: p.command,
			OnExit:  func(err error) tui.Msg { return paneExitedMsg{id: id, err: err} },
			// A pane hosts a real shell — tab-completion has to work, so
			// Tab must reach it rather than being intercepted for focus
			// navigation. ReleaseKey is left at its default (Ctrl+\), the
			// way out to Tab-navigate title bars/buttons again.
			WantsRawTab: true,
		}).Key(paneKey(id, "term"))
	}

	return tui.Box(layout.Vertical,
		tui.Child(layout.Length(1), titleBar),
		tui.Child(layout.Fill(1), content),
	).Key(paneKey(id, "box"))
}

func paneLabel(p *paneState) string {
	if p.kind == KindNamespaceBrowser {
		return p.title + ": " + p.browserPath
	}
	return p.title
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
