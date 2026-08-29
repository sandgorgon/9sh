// Package pane is a minimizable multi-pane multiplexer built on tui's
// retained widget.Terminal and 9sh's own native widgets, stacked
// vertically, each with an always-visible title-bar row that toggles it
// between full size and collapsed-to-title-bar.
//
// Five pane kinds exist: KindShell hosts a real pty-attached process
// (a shell, or anything else exec'able) via widget.Terminal; KindKyuRepl
// hosts a native kyu REPL (kyurepl.go) evaluating against the same
// shared *eval.Env as every other entry point into 9sh (see cmd/9sh's
// bootstrap); KindNamespaceBrowser (browser.go) is a live, navigable
// view of whatever's bound in that Env's namespace; KindJobViewer
// (jobviewer.go) spans /jobs plus every /n/<host>/jobs currently
// bound; KindSessionViewer (sessionviewer.go) reads back 9vcs-backed
// session history from disk. The latter three are the namespace-aware
// "differentiator" panes the project's design notes call for.
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
	"github.com/sandgorgon/tui/style"
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
	KindJobViewer
	KindSessionViewer
)

// Spec describes a pane to create, at startup (New) or later (AddPane).
type Spec struct {
	Title      string
	Kind       Kind
	Command    *exec.Cmd // KindShell only
	Env        *eval.Env // KindKyuRepl, KindNamespaceBrowser, KindJobViewer
	SessionDir string    // KindSessionViewer only — see SessionViewerSpec
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

// JobViewerSpec is a live job viewer spanning /jobs and every /n/<host>
// currently bound in env's namespace — see jobviewer.go.
func JobViewerSpec(title string, env *eval.Env) Spec {
	return Spec{Title: title, Kind: KindJobViewer, Env: env}
}

// SessionViewerSpec is a session-history viewer reading dir (typically
// ~/.config/9/session, the same directory cmd/9sh's bootstrapSession
// passes to session.New) — see sessionviewer.go. dir may be empty (no
// session history was available at startup at all); the pane reports
// that as its own error rather than a silent empty list.
func SessionViewerSpec(title, dir string) Spec {
	return Spec{Title: title, Kind: KindSessionViewer, SessionDir: dir}
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
	env     *eval.Env // KindKyuRepl, KindNamespaceBrowser, KindJobViewer

	// KindNamespaceBrowser's own business state — List's cursor (and,
	// by the same convention here, the current path and listing) is
	// caller-owned, not retained inside the widget; see browser.go.
	browserPath    string
	browserEntries []string
	browserCursor  int
	browserErr     string

	// KindJobViewer's own business state — see jobviewer.go.
	jobRows   []string
	jobCursor int
	jobErr    string

	// KindSessionViewer's own business state — see sessionviewer.go.
	// sessionDir is copied from Spec.SessionDir at construction (see
	// withNewPane); it never changes for this pane's lifetime.
	sessionDir    string
	sessionRows   []string
	sessionCursor int
	sessionErr    string
}

// Model is the pane multiplexer's tui.Model.
type Model struct {
	panes      []*paneState
	nextID     int
	env        *eval.Env   // shared with every pane's spec that needs one, for the "+" buttons
	theme      style.Theme // for the control strip / title bar background bars — see barStyle
	sessionDir string      // for the "+ history" button's SessionViewerSpec — see New
}

// New builds a Model seeded with the given panes. env is used to build
// new kyu-repl/namespace-browser/job-viewer panes from the control
// strip's "+" buttons — pass the same *eval.Env used elsewhere in the
// process (see cmd/9sh's bootstrap) so kyu state is shared, not
// duplicated per pane. sessionDir is likewise for the "+ history"
// button (see SessionViewerSpec); pass "" if session history isn't
// available this run (cmd/9sh's bootstrapSession already knows) — the
// pane reports that itself rather than cmd/9sh needing to skip adding
// the button at all.
//
// The theme is picked once, at startup, from $COLORFGBG (style.
// DetectAppearance's own doc comment explains why that heuristic and
// not a real query — there's no escape sequence every terminal answers
// for "what's your background color"), defaulting to Dark on anything
// unparseable. 9sh doesn't re-detect this mid-session; a user whose
// terminal theme changes while 9sh is running would need to restart it
// to pick up a new one — a real, accepted limitation, not something
// worth polling for.
func New(env *eval.Env, sessionDir string, specs ...Spec) Model {
	m := Model{env: env, sessionDir: sessionDir, theme: style.Default(style.DetectAppearance(os.Getenv))}
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
		sessionDir: s.SessionDir,
	})
	return m
}

// Init kicks off the initial namespace/disk listing for any seed panes
// that need it (namespace browser, job viewer, session viewer) — each
// does real I/O, which belongs in a Cmd, not directly in Update/View.
func (m Model) Init() tui.Cmd {
	var cmds []tui.Cmd
	for _, p := range m.panes {
		switch p.kind {
		case KindNamespaceBrowser:
			cmds = append(cmds, listDirCmd(p.id, p.env.Namespace(), p.browserPath))
		case KindJobViewer:
			cmds = append(cmds, listJobsCmd(p.id, p.env.Namespace()))
		case KindSessionViewer:
			cmds = append(cmds, listSessionCmd(p.id, p.sessionDir))
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
		newPane := next.panes[len(next.panes)-1]
		switch mm.spec.Kind {
		case KindNamespaceBrowser:
			return next, listDirCmd(newPane.id, mm.spec.Env.Namespace(), newPane.browserPath)
		case KindJobViewer:
			return next, listJobsCmd(newPane.id, mm.spec.Env.Namespace())
		case KindSessionViewer:
			return next, listSessionCmd(newPane.id, mm.spec.SessionDir)
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
	case jobViewerMoveMsg:
		if p := m.find(mm.id); p != nil {
			p.jobCursor = clamp(p.jobCursor+mm.delta, 0, max0(len(p.jobRows)-1))
		}
	case jobViewerRefreshRequestedMsg:
		if p := m.find(mm.id); p != nil {
			return m, listJobsCmd(p.id, p.env.Namespace())
		}
	case jobsListedMsg:
		if p := m.find(mm.id); p != nil {
			p.jobErr = ""
			p.jobRows = mm.rows
			if mm.err != nil {
				p.jobErr = mm.err.Error()
				p.jobRows = nil
			}
			p.jobCursor = clamp(p.jobCursor, 0, max0(len(p.jobRows)-1))
		}
	case sessionViewerMoveMsg:
		if p := m.find(mm.id); p != nil {
			p.sessionCursor = clamp(p.sessionCursor+mm.delta, 0, max0(len(p.sessionRows)-1))
		}
	case sessionViewerRefreshRequestedMsg:
		if p := m.find(mm.id); p != nil {
			return m, listSessionCmd(p.id, p.sessionDir)
		}
	case sessionListedMsg:
		if p := m.find(mm.id); p != nil {
			p.sessionErr = ""
			p.sessionRows = mm.rows
			if mm.err != nil {
				p.sessionErr = mm.err.Error()
				p.sessionRows = nil
			}
			p.sessionCursor = clamp(p.sessionCursor, 0, max0(len(p.sessionRows)-1))
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

// controlStripFocusables is how many Tab-focusable widgets
// controlStrip contributes, ahead of any pane, in Tab order — kept
// beside controlStrip's own children by hand and used by
// InitialFocusAdvances, since tui ties Tab order to document/paint
// order with no independent override (see InitialFocusAdvances' doc
// comment for why that matters here). Update this if a button is
// added or removed above.
const controlStripFocusables = 6 // + shell, + kyu, + browse, + jobs, + history, quit

func (m Model) controlStrip() tui.Node {
	return tui.Box(layout.Horizontal,
		tui.Child(layout.Length(10), m.addPaneButton("+ shell", ShellSpec("shell"))),
		tui.Child(layout.Length(8), m.addPaneButton("+ kyu", KyuReplSpec("kyu", m.env))),
		tui.Child(layout.Length(11), m.addPaneButton("+ browse", NamespaceBrowserSpec("browse", m.env))),
		tui.Child(layout.Length(9), m.addPaneButton("+ jobs", JobViewerSpec("jobs", m.env))),
		tui.Child(layout.Length(12), m.addPaneButton("+ history", SessionViewerSpec("history", m.sessionDir))),
		tui.Child(layout.Length(8), m.quitButton()),
		// barFill (not tui.Text("", ...), which paints nothing at all
		// for an empty string — Text's Paint only iterates the string's
		// own runes) carries the same background past the last button,
		// so the strip reads as one continuous bar across the pane's
		// full width, not just up to "quit". Never itself focusable, so
		// it only ever needs the unfocused variant.
		tui.Child(layout.Fill(1), barFill(m.controlStripStyle(false))),
	).Key("control-strip")
}

// barStyle is the background-filled style for a pane's own title bar:
// theme.Border (a muted, structural color) unfocused, theme.Focus
// when focused — see flatFocusableWidget.Paint, which fills its whole
// cell with this before drawing text on top, that's what actually
// makes it read as a bar instead of colored text floating on the
// terminal's own background. Foreground is left at the terminal's own
// default (cell.Color's zero value) rather than an explicit theme
// color, so text stays legible regardless of exactly which accent the
// terminal renders theme.Border/Focus as.
func (m Model) barStyle(focused bool) cell.Style {
	bg := m.theme.Border
	if focused {
		bg = m.theme.Focus
	}
	return cell.Style{Bg: bg, Attr: cell.AttrBold}
}

// controlStripStyle is the control strip's own bar color — deliberately
// distinct from barStyle/theme.Border, so the always-visible top-level
// toolbar reads as a different, more prominent layer of chrome than a
// per-pane title bar. Uses theme.Primary rather than theme.Focus for
// the base: in tui/style's own default themes Primary and Focus are
// literally the same RGB value (both are "the accent color"), so
// swapping to Focus on focus would be invisible; a reverse-video
// attribute flip is a real, visible change regardless of that.
func (m Model) controlStripStyle(focused bool) cell.Style {
	st := cell.Style{Bg: m.theme.Primary, Attr: cell.AttrBold}
	if focused {
		st.Attr |= cell.AttrReverse
	}
	return st
}

// InitialFocusAdvances is how many synthetic Tab presses cmd/9sh's
// runTUI should feed a freshly constructed tui.App (via HandleInput,
// before Run ever reads real input) so the shell starts with keyboard
// focus on the first pane's actual content instead of, as tui.App's
// zero-value focusIdx would otherwise leave it, the control strip's
// first button — a real usability bug found by driving a real build
// under a pty: a few keystrokes typed immediately after launch went
// nowhere, since nothing was listening for them yet.
//
// tui ties Tab order strictly to document/paint order (see
// reconcile.go's collectFocusables) with no separate "start focus
// here" hook, and moving the control strip or a pane's title bar
// later in the tree to fix this would move it later on screen too —
// so this instead replays N real Tab keypresses through the same
// public path a user's keyboard would use. Assumes exactly one pane
// at startup (true of every current cmd/9sh call site: one title bar
// beyond the control strip's own buttons) — not meant as a general
// "N panes" formula.
func InitialFocusAdvances() int {
	return controlStripFocusables + 1 // + the first pane's own title bar
}

func (m Model) addPaneButton(label string, spec Spec) tui.Node {
	return flatFocusable("btn-"+label, " "+label+" ", m.controlStripStyle,
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return addPaneMsg{spec: spec}
		})
}

func (m Model) quitButton() tui.Node {
	return flatFocusable("quit-btn", " quit ", m.controlStripStyle,
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
	titleBar := flatFocusable(paneKey(id, "title"), label,
		func(focused bool) cell.Style { return m.titleStyle(p, focused) },
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
	case KindJobViewer:
		content = jobViewerNode(p)
	case KindSessionViewer:
		content = sessionViewerNode(p)
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

func (m Model) titleStyle(p *paneState, focused bool) cell.Style {
	if p.exited {
		return cell.Style{Bg: m.theme.Error, Attr: cell.AttrBold}
	}
	return m.barStyle(focused)
}
