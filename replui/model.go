// Package replui is 9sh's single-screen interactive TUI: a native kyu
// REPL (kyuReplWidget, in kyurepl.go) with live syntax highlighting,
// multi-line continuation, Ctrl-R history search, and Tab completion,
// evaluating against the same shared *eval.Env every other 9sh entry
// point uses (see cmd/9sh's bootstrap). One screen, one widget — there
// is no multi-pane split tree here; that's github.com/sandgorgon/9mux's
// job now (a separate project — see its own README for the "any
// command hosts a pane, 9sh included" design and the 9P-browsing pane
// that replaces this package's former namespace-browser/job-viewer/
// session-viewer panes).
//
// Model exists at all — rather than kyuReplNode being cmd/9sh's own
// tui.App root directly — for exactly two pieces of state a lone widget
// can't hold itself: the built-in help overlay (a widget.Modal
// composited *over* the REPL, which only something above both nodes in
// the tree can arrange) and the fullscreen-%cmd handoff (swapping the
// *entire* screen's root node from the REPL to a widget.Terminal and
// back — see fullscreenAttach's doc comment).
package replui

import (
	"os"
	"os/exec"
	"time"

	"github.com/sandgorgon/tui/layout"
	"github.com/sandgorgon/tui/style"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"

	"github.com/sandgorgon/9sh/kyu/eval"
)

// Model is replui's tui.Model — see the package doc comment for why
// this exists instead of a bare kyuReplNode.
type Model struct {
	env *eval.Env

	// theme is only for the help overlay's own border/text contrast
	// (widget.Modal's ModalOptions.Theme) — kyuReplWidget's own colors
	// are fixed ANSI values (see kyurepl.go's promptStyle/errorStyle/...),
	// not theme-driven, since this screen has no per-pane chrome (border,
	// title bar) left to theme at all now that the split-tree multiplexer
	// is gone. Detected once at startup from $COLORFGBG, same heuristic
	// (and same "no live toggle" limitation once autodetection is wrong)
	// the multiplexer this replaced used for its whole title-bar/control-
	// strip chrome — that had a runtime toggle button; this doesn't,
	// since there's no control strip left to host one. Revisit with a
	// keybinding if a wrong autodetection turns out to matter in
	// practice for a help-overlay-only theme.
	theme style.Theme

	// fullscreen is non-nil while a fullscreen %cmd (vim, top, ssh, ...
	// — see kyu/eval's runExternalFullscreen) has temporarily taken over
	// the whole screen — see fullscreenAttach's doc comment. Unlike the
	// pane-multiplexer this package replaced (github.com/sandgorgon/9sh
	// at commit 4b488a7, before this split — see 9mux's own README for
	// the full history), there's only ever one screen to hand over and
	// take back, so this needs no pane id or Model.find: View swaps its
	// single root node directly on this field.
	fullscreen *fullscreenAttach

	// tickRunning tracks whether a redrawTickCmd chain is already in
	// flight — see redrawTickCmd's doc comment. Unlike the multiplexer
	// this replaced (any of several KindShell panes could need the
	// tick), the only thing in this single-screen model that ever hosts
	// a live pty is a fullscreen attachment, so this is really just
	// "is fullscreen != nil, tracked across the async tick chain."
	tickRunning bool

	helpOpen bool
}

// New builds a Model for env — the same shared *eval.Env every other
// 9sh entry point uses (see cmd/9sh's bootstrap), so kyu state (vars,
// namespace, job history) is identical whether reached through here,
// -repl, or a script.
func New(env *eval.Env) Model {
	return Model{env: env, theme: style.Default(style.DetectAppearance(os.Getenv))}
}

func (m Model) Init() tui.Cmd { return nil }

// fullscreenAttach is what kyuReplWidget hands Model when a fullscreen
// %cmd (see kyu/eval's runExternalFullscreen) needs the real screen —
// built by kyuReplWidget.attachFullscreen. cmd is unstarted (View's
// widget.Terminal construction starts it, attached to a real pty);
// onDone is eval's own callback (already closing over whatever
// namespace paths were checked out for this invocation) and must be
// called exactly once, when the child exits, so eval can write them
// back — see fullscreenExitedMsg's handling in Update.
type fullscreenAttach struct {
	cmd    *exec.Cmd
	onDone func(err error)
}

// startFullscreenMsg attaches a fullscreen program to the whole screen,
// returned by kyuReplWidget.consumeFullscreenCmd right after the Enter
// that triggered it. Handled in Update by setting Model.fullscreen,
// which View then renders as a widget.Terminal instead of the normal
// kyu-repl node — see fullscreenAttach's doc comment.
type startFullscreenMsg struct{ attach *fullscreenAttach }

// fullscreenExitedMsg is widget.Terminal's OnExit once a fullscreen
// attachment's child exits. Handled in Update by calling the
// attachment's onDone (eval's own write-back/cleanup) and clearing
// Model.fullscreen, so the next Paint reverts to the normal kyu-repl
// node.
type fullscreenExitedMsg struct{ err error }

type toggleHelpMsg struct{}
type closeHelpMsg struct{}

type redrawTickMsg struct{}

// redrawInterval balances "fullscreen output shows up promptly" against
// redraw overhead — see redrawTickCmd's doc comment.
const redrawInterval = 50 * time.Millisecond

// redrawTickCmd self-reschedules (see redrawTickMsg's handling in
// Update) for as long as a fullscreen attachment is live.
// widget.Terminal's own doc comment explains why this is needed: a
// hosted pty's output updates the widget's internal vt.Screen state
// continuously in a background goroutine, but that only becomes
// visible the next time the App happens to render a frame for any
// other reason.
func redrawTickCmd() tui.Cmd {
	return func() tui.Msg {
		time.Sleep(redrawInterval)
		return redrawTickMsg{}
	}
}

func (m Model) Update(msg tui.Msg) (tui.Model, tui.Cmd) {
	switch mm := msg.(type) {
	case startFullscreenMsg:
		m.fullscreen = mm.attach
		if m.tickRunning {
			return m, nil
		}
		m.tickRunning = true
		return m, redrawTickCmd()
	case fullscreenExitedMsg:
		if m.fullscreen != nil && m.fullscreen.onDone != nil {
			m.fullscreen.onDone(mm.err)
		}
		m.fullscreen = nil
	case redrawTickMsg:
		if m.fullscreen != nil {
			return m, redrawTickCmd()
		}
		m.tickRunning = false
	case toggleHelpMsg:
		m.helpOpen = !m.helpOpen
	case closeHelpMsg:
		m.helpOpen = false
	}
	return m, nil
}

func (m Model) View() tui.Node {
	if m.fullscreen != nil {
		// A fullscreen %cmd (vim, top, ssh, ...) has temporarily taken
		// over the whole screen — the same widget.Terminal construction
		// 9mux's own Terminal pane kind uses, just hosted directly here
		// instead of inside a pane tree. See fullscreenAttach's doc
		// comment and fullscreenExitedMsg's handling in Update for how
		// control comes back. No help overlay while this is up: there's
		// nothing of this screen's own left to show it over, and the
		// hosted program owns the keyboard entirely.
		return widget.Terminal(widget.TerminalOptions{
			Command:     m.fullscreen.cmd,
			OnExit:      func(err error) tui.Msg { return fullscreenExitedMsg{err: err} },
			WantsRawTab: true,
			Theme:       m.theme,
		}).Key("fullscreen-term")
	}
	return tui.Box(layout.Vertical,
		tui.Child(layout.Fill(1), kyuReplNode(m.env)),
		// Length(0): a widget.Modal's own assigned Rect is never used
		// (real drawing happens via PaintOverlay, a separate full-buffer
		// pass — see Modal's own doc comment), so this deliberately
		// takes no space in the normal Box flow; its Node just needs to
		// exist somewhere in the tree every frame for App to find it.
		tui.Child(layout.Length(0), widget.Modal(helpNode(), widget.ModalOptions{
			Theme:          m.theme,
			Title:          "Help",
			Open:           m.helpOpen,
			Width:          78,
			Height:         24,
			OnOutsideClick: func() tui.Msg { return closeHelpMsg{} },
		})),
	)
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
