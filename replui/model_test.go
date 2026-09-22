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
// TestHelpOpensAndClosesWithQuestionMark).
func dispatchAll(app *tui.App, cmds []tui.Cmd) {
	for _, cmd := range cmds {
		if cmd != nil {
			app.Dispatch(cmd())
		}
	}
}

// typeLineAndSubmit drives the real HandleInput path (not
// kyuReplWidget.HandleEvent directly) to submit src as a top-level kyu
// expression, and waits for it to actually finish, before returning --
// used below to get something into the transcript/history before
// exercising the fullscreen round trip. Safe to run every Cmd inline
// via dispatchAll, even for a foreground %cmd: submit() spawns its own
// background goroutine for the actual evaluate() call and hands back
// only a trivial, instantly-resolvable Cmd (evalStartedMsg -- see its
// own doc comment for why that split matters), so that goroutine is
// still genuinely in flight when this call returns to dispatchAll --
// the wait below (polling w.busy, resolved by repeatedly Dispatching a
// no-op the same way TestFullscreenAttachRendersTerminalAndRestoresOnExit
// already does for widget.Terminal's own PendingMsgSource, since that's
// what drains kyuReplWidget.TakePendingMsg -- see its own doc comment)
// is what makes the transcript actually contain src's result by the
// time this returns, matching what every caller here expects.
func typeLineAndSubmit(t *testing.T, app *tui.App, w *kyuReplWidget, src string) {
	t.Helper()
	for _, r := range src {
		dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: r}))
	}
	dispatchAll(app, app.HandleInput(input.KeyEvent{Key: input.KeyEnter}))
	deadline := time.Now().Add(5 * time.Second)
	for w.busy && time.Now().Before(deadline) {
		app.Dispatch(struct{}{})
		time.Sleep(time.Millisecond)
	}
	if w.busy {
		t.Fatalf("typeLineAndSubmit(%q): still busy 5s after submitting", src)
	}
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

	typeLineAndSubmit(t, app, m.replWidget, "40 + 2")
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

// TestCtrlCInterruptsRunningForegroundCommand is the end-to-end
// regression test for real gap #1 (the TUI's Ctrl+C freeze): submitting
// a long-running foreground %cmd must not block the event loop from
// handling further input, and Ctrl+C must reach the running process's
// real interrupt handler and end it well before it would finish on its
// own -- see kyurepl.go's busy/submit/evaluate/TakePendingMsg doc
// comments for the mechanism (Env.InterruptHandler, already wired by
// runExternalDirect/runExternalViaJob for every foreground %cmd; the
// only thing that was ever missing was the TUI actually calling it).
// `sleep 20` is run with a nil namespace (Namespace() == nil), so this
// exercises runExternalDirect's interrupt handler specifically -- the
// same one `9sh -repl`'s own real SIGINT already used.
func TestCtrlCInterruptsRunningForegroundCommand(t *testing.T) {
	skipUnlessOnPath(t, "sleep")
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	for _, r := range `%sleep 20` {
		dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: r}))
	}
	// submit()'s background goroutine (see its own doc comment) is
	// spawned here, inline, before HandleInput even returns -- so busy
	// is already true and the subprocess is already starting by the
	// time this call does.
	dispatchAll(app, app.HandleInput(input.KeyEvent{Key: input.KeyEnter}))

	if !m.replWidget.busy {
		t.Fatal("expected busy=true immediately after submitting, before evaluate() has finished")
	}

	// The event loop must stay responsive while sleep runs in the
	// background -- this dispatch (an ordinary keystroke, a no-op while
	// busy) is what used to be impossible until the whole command
	// finished, back when evaluate() ran inline on this goroutine (or,
	// in an earlier version of this fix, inside the widget's own
	// returned Cmd -- see evalStartedMsg's doc comment for why that's
	// just as bad).
	dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: 'x'}))

	// Retried, not sent once: Env.SetInterruptHandler only gets called
	// once the background goroutine has actually reached cmd.Start()
	// inside runExternalDirect (see its own doc comment), which racing
	// this test goroutine can easily still be a few scheduler ticks away
	// from -- a Ctrl+C that lands before then is simply dropped (matching
	// a real shell's "Ctrl-C at an idle prompt does nothing"), the same
	// as a user's first keypress sometimes losing that exact race. Each
	// dispatched Ctrl+C's own Dispatch call (inside app.HandleInput's
	// underlying handling) is also what picks up TakePendingMsg's
	// evalDoneMsg once the interrupted sleep actually exits (see its own
	// doc comment) -- there's no real App.Run loop or redraw tick running
	// in this test to do that automatically.
	deadline := time.Now().Add(5 * time.Second)
	for m.replWidget.busy && time.Now().Before(deadline) {
		dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: 'c', Mod: input.ModCtrl}))
		time.Sleep(10 * time.Millisecond)
	}
	if m.replWidget.busy {
		t.Fatal("Ctrl+C did not interrupt `sleep 20` within 5s")
	}

	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "9sh>") {
		t.Fatalf("expected the prompt back after the interrupt:\n%s", buf)
	}
}

// TestCtrlCInterruptsRunningForegroundKyuLoop is
// TestCtrlCInterruptsRunningForegroundCommand's counterpart for the
// residual gap left after items 1 and 2: a *foreground* pure-kyu
// compute loop (`while true {}` typed directly at the prompt, not
// backgrounded via &) must also be interruptible now -- via
// Env.CancelFunc (see submit()'s and handleKey's own doc comments),
// not Env.InterruptHandler, which stays nil for this case (there's no
// external command to signal). Unlike the %cmd version, there's no
// race to retry through here: submit() registers CancelFunc
// synchronously, on this same goroutine, before the eval goroutine is
// even spawned -- so it's already set by the time this test's own
// Enter dispatch returns. The retry loop below is kept anyway, purely
// for consistency with the %cmd version above; a single Ctrl+C would
// suffice.
func TestCtrlCInterruptsRunningForegroundKyuLoop(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	// An immediately-invoked closure, not two statements on separate
	// lines: a bare `{ ... }` typed alone (no &) is just a closure
	// *literal* -- ordinary kyu semantics, unrelated to
	// evalBackgroundInproc's own bare-closure auto-invoke special case,
	// which only applies to the backgrounded path -- so this needs an
	// explicit `()` call to actually run its body as one single-line
	// submission.
	for _, r := range `{ i := 0; while true { i = i + 1 } }()` {
		dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: r}))
	}
	dispatchAll(app, app.HandleInput(input.KeyEvent{Key: input.KeyEnter}))

	if !m.replWidget.busy {
		t.Fatal("expected busy=true immediately after submitting, before evaluate() has finished")
	}

	// The event loop must stay responsive while the loop runs.
	dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: 'x'}))

	deadline := time.Now().Add(5 * time.Second)
	for m.replWidget.busy && time.Now().Before(deadline) {
		dispatchAll(app, app.HandleInput(input.KeyEvent{Rune: 'c', Mod: input.ModCtrl}))
		time.Sleep(10 * time.Millisecond)
	}
	if m.replWidget.busy {
		t.Fatal("Ctrl+C did not interrupt the foreground while-loop within 5s")
	}

	forceRenders(app, 1)
	buf := app.Buffer().String()
	if !strings.Contains(buf, "9sh>") {
		t.Fatalf("expected the prompt back after the interrupt:\n%s", buf)
	}
	if !strings.Contains(buf, "context canceled") {
		t.Fatalf("expected the transcript to show the cancellation error:\n%s", buf)
	}
}

// TestHelpOpensAndClosesWithQuestionMark drives the real input path:
// `?` at the empty prompt opens the help modal, and `?` again — now handled by the modal's own
// body, which claims focus while open — closes it.
func TestHelpOpensAndClosesWithQuestionMark(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 80, 24)
	defer app.Close()
	press := func(ev input.KeyEvent) {
		for _, cmd := range app.HandleInput(ev) {
			if cmd != nil {
				app.Dispatch(cmd())
			}
		}
		forceRenders(app, 1)
	}

	press(input.KeyEvent{Rune: '?'})
	if buf := app.Buffer().String(); !strings.Contains(buf, "9sh — help") {
		t.Fatalf("expected help on screen after ?:\n%s", buf)
	}
	press(input.KeyEvent{Rune: '?'})
	if buf := app.Buffer().String(); strings.Contains(buf, "9sh — help") {
		t.Fatalf("expected help closed after a second ?:\n%s", buf)
	}
}
