package replui

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
)

func skipUnlessOnPath(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

func forceRenders(app *tui.App, n int) {
	for range n {
		app.Dispatch(struct{}{})
	}
}

// ---- pure Model.Update logic — no tui.App needed ----

func TestToggleHelpMsgOpensAndCloses(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	if m.helpOpen {
		t.Fatal("help should start closed")
	}

	next, _ := m.Update(toggleHelpMsg{})
	m = next.(Model)
	if !m.helpOpen {
		t.Fatal("expected help open after one toggleHelpMsg")
	}

	next, _ = m.Update(toggleHelpMsg{})
	m = next.(Model)
	if m.helpOpen {
		t.Fatal("expected help closed after a second toggleHelpMsg")
	}
}

func TestCloseHelpMsgClosesRegardlessOfState(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	next, _ := m.Update(toggleHelpMsg{})
	m = next.(Model)
	if !m.helpOpen {
		t.Fatal("setup: expected help open")
	}
	next, _ = m.Update(closeHelpMsg{})
	m = next.(Model)
	if m.helpOpen {
		t.Fatal("expected help closed after closeHelpMsg")
	}
}

// TestStartFullscreenMsgSetsFullscreenAndStartsTick confirms Update's
// pane-multiplexer-free version of the original startFullscreenMsg
// handling: no id, no Model.find — the attachment just becomes the
// Model's one fullscreen field directly.
func TestStartFullscreenMsgSetsFullscreenAndStartsTick(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	attach := &fullscreenAttach{cmd: exec.Command("true")}
	next, cmd := m.Update(startFullscreenMsg{attach: attach})
	m = next.(Model)
	if m.fullscreen != attach {
		t.Fatal("expected Model.fullscreen to be set to the attached value")
	}
	if !m.tickRunning || cmd == nil {
		t.Fatal("expected the redraw tick to start alongside the attachment")
	}
}

// TestFullscreenExitedMsgCallsOnDoneAndClearsFullscreen confirms the
// write-back/cleanup callback always runs exactly once, and the Model
// reverts to its normal (non-fullscreen) state.
func TestFullscreenExitedMsgCallsOnDoneAndClearsFullscreen(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	var onDoneCalled bool
	attach := &fullscreenAttach{cmd: exec.Command("true"), onDone: func(error) { onDoneCalled = true }}
	next, _ := m.Update(startFullscreenMsg{attach: attach})
	m = next.(Model)

	next, _ = m.Update(fullscreenExitedMsg{})
	m = next.(Model)
	if !onDoneCalled {
		t.Fatal("expected onDone to be called")
	}
	if m.fullscreen != nil {
		t.Fatal("expected Model.fullscreen cleared after exit")
	}
}

// ---- integration: real tui.App, the part with actual keying-
// correctness risk (see this package's own doc comment) ----

// TestHelpShowsContentOnScreenViaF1 drives the real input path (F1,
// this package's help toggle now that there's no control-strip button
// — see kyurepl.go's handleKey) rather than just Update, confirming
// the modal's content genuinely reaches the screen.
func TestHelpShowsContentOnScreenViaF1(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 80, 24)
	defer app.Close()

	for _, cmd := range app.HandleInput(input.KeyEvent{Key: input.KeyF1}) {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "9sh — help") {
		t.Fatalf("expected help content on screen after F1:\n%s", buf)
	}
}

// TestFullscreenAttachRendersTerminalAndRestoresOnExit exercises the
// full round trip: a fullscreen attachment takes over the screen (a
// widget.Terminal, same as 9mux's own Terminal pane kind), and once
// the child exits, control comes back to the normal kyu-repl prompt.
//
// widget.Terminal implements tui.PendingMsgSource as of tui v0.6.1:
// App.Dispatch drains OnExit's Msg on its own, from whatever Dispatch
// call happens to run next, rather than needing a real keystroke to
// notice it (the previous behavior, which swallowed that keystroke —
// filed upstream as sandgorgon/tui#33, fixed in v0.6.1). For a fast-
// exiting child like "true", the whole attach-exit-restore cycle can
// complete within the single Dispatch(startFullscreenMsg{...}) call
// below — Dispatch recursively redispatches a widget's pending Msg
// before returning (see tui's own Dispatch doc comment) — so this test
// doesn't wait for a transient "[exited]" frame the way it used to;
// onDoneCalled and the prompt's return are the only durable signals
// once the cycle settles, however many Dispatch calls it took.
// dispatchAll runs the tui.Cmds HandleInput returns synchronously,
// mirroring the loop cmd/9sh's real run loop uses (see
// TestHelpShowsContentOnScreenViaF1).
func dispatchAll(app *tui.App, cmds []tui.Cmd) {
	for _, cmd := range cmds {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
}

// typeLineAndSubmit drives the real HandleInput path (not
// kyuReplWidget.HandleEvent directly) to submit src as a top-level kyu
// expression — used below to get something into the transcript/history
// before exercising the fullscreen round trip.
func typeLineAndSubmit(app *tui.App, src string) {
	for _, r := range src {
		dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: r}))
	}
	dispatchAll(app, app.HandleInput(input.KeyEvent{Key: input.KeyEnter}))
}

// TestFullscreenRoundTripPreservesTranscriptAndHistory is the
// regression test for a real, previously-shipped bug: Model.View used
// to build kyuReplNode from a fresh &kyuReplWidget{} every call, so
// once a fullscreen attachment (kyuReplNode's tree slot going missing
// for the whole time vim/9ed/... owns the screen) got disposed by tui's
// reconciler (see kyuReplNode's own doc comment), control coming back
// minted a brand-new widget with an empty transcript and empty history
// — as if 9sh had just started, even though the session hadn't
// restarted at all. Model now owns the one long-lived kyuReplWidget
// instance itself (Model.replWidget), so this must survive the round
// trip.
func TestFullscreenRoundTripPreservesTranscriptAndHistory(t *testing.T) {
	skipUnlessOnPath(t, "true")
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	typeLineAndSubmit(app, "40 + 2")
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "42") {
		t.Fatalf("setup: expected 42 in the transcript before fullscreen:\n%s", buf)
	}

	var onDoneCalled bool
	app.Dispatch(startFullscreenMsg{attach: &fullscreenAttach{
		cmd:    exec.Command("true"),
		onDone: func(error) { onDoneCalled = true },
	}})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !onDoneCalled {
		app.Dispatch(struct{}{})
		time.Sleep(20 * time.Millisecond)
	}
	if !onDoneCalled {
		t.Fatalf("onDone was never called after the fullscreen program exited:\n%s", app.Buffer().String())
	}

	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "42") {
		t.Fatalf("transcript lost after returning from a fullscreen program:\n%s", buf)
	}

	dispatchAll(app, app.HandleInput(input.KeyEvent{Key: input.KeyUp}))
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "40 + 2") {
		t.Fatalf("input history lost after returning from a fullscreen program (Up didn't recall it):\n%s", buf)
	}
}

func TestFullscreenAttachRendersTerminalAndRestoresOnExit(t *testing.T) {
	skipUnlessOnPath(t, "true")
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	var onDoneCalled bool
	app.Dispatch(startFullscreenMsg{attach: &fullscreenAttach{
		cmd:    exec.Command("true"),
		onDone: func(error) { onDoneCalled = true },
	}})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !onDoneCalled {
		app.Dispatch(struct{}{})
		time.Sleep(20 * time.Millisecond)
	}
	if !onDoneCalled {
		t.Fatalf("onDone was never called after the fullscreen program exited:\n%s", app.Buffer().String())
	}
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "9sh>") {
		t.Fatalf("expected the kyu-repl prompt back on screen after the fullscreen program exited:\n%s", buf)
	}
}
