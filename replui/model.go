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
	"github.com/sandgorgon/tui/pty"
	"github.com/sandgorgon/tui/style"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"

	"github.com/sandgorgon/9sh/kyu/eval"
)

// Model is replui's tui.Model — see the package doc comment for why
// this exists instead of a bare kyuReplNode.
type Model struct {
	// replWidget is the one long-lived kyuReplWidget instance for this
	// session's lifetime, constructed once in New and handed to
	// kyuReplNode on every View() call — see kyuReplNode's own doc
	// comment for why this can't just be built lazily inside it: tui's
	// reconciler discards a Component's retained Widget the moment its
	// Node is absent from an entire frame, which happens for as long as
	// a fullscreen %cmd is attached (see fullscreen below), so anything
	// that must survive that has to be owned here, at the Model level,
	// not inside the widget the reconciler is free to throw away.
	replWidget *kyuReplWidget

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
	return Model{
		replWidget: &kyuReplWidget{env: env},
		theme:      style.Default(style.DetectAppearance(os.Getenv)),
	}
}

func (m Model) Init() tui.Cmd { return nil }

// fullscreenAttach is what kyuReplWidget hands Model when something
// needs the real screen: either a fullscreen %cmd (see kyu/eval's
// runExternalFullscreen — cmd is set, unstarted; View's widget.Terminal
// construction starts it, attached to a real local pty) or an attach()
// job (see kyu/eval's biAttach/AttachHandlerFunc — stream is set
// instead, already live; View's widget.Terminal drives it directly, no
// local pty involved at all). Exactly one of cmd/stream is set. onDone
// is eval's own callback — for cmd, already closing over whatever
// namespace paths were checked out for this invocation and writing them
// back; for stream, a no-op today (attach() has nothing to write back,
// detaching never touches the job itself) — either way it must be
// called exactly once, when the child exits or the stream ends — see
// fullscreenExitedMsg's handling in Update.
type fullscreenAttach struct {
	cmd    *exec.Cmd
	stream pty.Stream
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

// evalStartedMsg is submit()'s tui.Cmd result -- not the evaluation
// itself (a real trap: *tui.App resolves a Cmd returned from the
// focused widget's HandleEvent synchronously, on its own event-loop
// goroutine, via resolveWidgetCmd, exactly because every built-in
// widget's own callback-style Cmd only ever repackages a Msg it
// already computed, never blocking work -- see resolveWidgetCmd's own
// doc comment in package tui. Putting the actual eval.Eval call inside
// this Cmd's closure, as an earlier version of this fix did, silently
// reintroduced the very freeze it was meant to fix: it just moved the
// blocking call from Model.Update into resolveWidgetCmd instead, still
// on App.Run's one event-loop goroutine). Handled in Update purely to
// start the redraw tick (see redrawTickMsg) so w.busy's eventual
// resolution -- and Ctrl+C's actual effect on the running command --
// become visible without needing an unrelated keypress first; the
// evaluate() call it names runs on a genuine goroutine submit() spawns
// itself, reporting back via TakePendingMsg/evalDoneMsg instead (see
// both their own doc comments).
type evalStartedMsg struct{}

// evalDoneMsg is what kyuReplWidget.TakePendingMsg reports once
// submit()'s background evaluate() call (see its own doc comment for
// why it has to run this way, not as a Cmd) finishes: its evalResult is
// ready to be applied back to the one live *kyuReplWidget -- via
// applyEvalResult, on this, the UI goroutine, same as everything else
// Update touches.
type evalDoneMsg struct{ result evalResult }

type redrawTickMsg struct{}

// redrawInterval balances "fullscreen output shows up promptly" against
// redraw overhead — see redrawTickCmd's doc comment.
const redrawInterval = 50 * time.Millisecond

// redrawTickCmd self-reschedules (see redrawTickMsg's handling in
// Update) for as long as a fullscreen attachment is live, or a
// background evaluate() call is (m.replWidget.busy). widget.Terminal's
// own doc comment explains why this is needed for the fullscreen case:
// a hosted pty's output updates the widget's internal vt.Screen state
// continuously in a background goroutine, but that only becomes
// visible the next time the App happens to render a frame for any
// other reason. The busy case is the same gap for
// kyuReplWidget.TakePendingMsg: it's only ever drained by a Dispatch
// call, so without this tick, a foreground %cmd's completion (or an
// interrupted one, post-Ctrl+C) would only show up whenever the user
// next happened to press some unrelated key.
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
		if m.fullscreen != nil || m.replWidget.busy {
			return m, redrawTickCmd()
		}
		m.tickRunning = false
	case toggleHelpMsg:
		m.helpOpen = !m.helpOpen
	case closeHelpMsg:
		m.helpOpen = false
	case evalStartedMsg:
		if m.tickRunning {
			return m, nil
		}
		m.tickRunning = true
		return m, redrawTickCmd()
	case evalDoneMsg:
		return m, m.replWidget.applyEvalResult(mm.result)
	}
	return m, nil
}

func (m Model) View() tui.Node {
	if m.fullscreen != nil {
		// A fullscreen %cmd (vim, top, ssh, ...) or an attach() job has
		// temporarily taken over the whole screen — the same
		// widget.Terminal construction 9mux's own Terminal pane kind
		// uses, just hosted directly here instead of inside a pane tree,
		// and (for attach()) driven from a live pty.Stream instead of a
		// locally-spawned Command. See fullscreenAttach's doc comment and
		// fullscreenExitedMsg's handling in Update for how control comes
		// back. No help overlay while this is up: there's nothing of this
		// screen's own left to show it over, and the hosted program/job
		// owns the keyboard entirely.
		return widget.Terminal(widget.TerminalOptions{
			Command:     m.fullscreen.cmd,
			Stream:      m.fullscreen.stream,
			OnExit:      func(err error) tui.Msg { return fullscreenExitedMsg{err: err} },
			WantsRawTab: true,
			Theme:       m.theme,
		}).Key("fullscreen-term")
	}
	return tui.Box(layout.Vertical,
		tui.Child(layout.Fill(1), kyuReplNode(m.replWidget)),
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
