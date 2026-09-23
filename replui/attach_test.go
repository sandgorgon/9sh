package replui

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/term"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/ns"
)

// jobsEnv is eval_test.go's own jobsEnvWithManager, minimally
// reproduced here (package eval's version is unexported and this is a
// different package) -- a namespace with /jobs bound to a real
// job.Manager, the minimum a backgrounded %cmd (&, &pty included)
// needs to work at all.
func jobsEnv(t *testing.T) *eval.Env {
	t.Helper()
	mgr := job.NewManager()
	namespace := ns.New()
	if err := namespace.BindFS(job.New(mgr), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrap bind /jobs: %v", err)
	}
	return eval.NewGlobalEnv(namespace)
}

// fakeStream is a minimal tui/pty.Stream for exercising the attach
// takeover without a real job or pty -- see widget's own fakeStream
// (terminal_stream_test.go) for the sibling used one layer down; this
// one is duplicated rather than shared since it's small and the two
// live in different modules (tui vs 9sh).
type fakeStream struct {
	r *io.PipeReader
	w *io.PipeWriter

	mu      sync.Mutex
	written bytes.Buffer
	closed  bool
}

func newFakeStream() *fakeStream {
	r, w := io.Pipe()
	return &fakeStream{r: r, w: w}
}

func (f *fakeStream) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *fakeStream) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.Write(p)
}
func (f *fakeStream) Resize(term.Size) error { return nil }
func (f *fakeStream) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.w.Close()
}

// TestAttachStreamRendersOutputAndRestoresOnExit is
// TestFullscreenAttachRendersTerminalAndRestoresOnExit's Stream-path
// sibling: an attach() job (here, a fake stream standing in for one)
// takes over the whole screen exactly like a fullscreen %cmd does, its
// output renders, and once the stream ends, control comes back to the
// normal kyu-repl prompt.
func TestAttachStreamRendersOutputAndRestoresOnExit(t *testing.T) {
	m := New(eval.NewGlobalEnv(nil))
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	fs := newFakeStream()
	var onDoneCalled bool
	app.Dispatch(startFullscreenMsg{attach: &fullscreenAttach{
		stream: fs,
		onDone: func(error) { onDoneCalled = true },
	}})

	if _, err := fs.w.Write([]byte("hello-from-job")); err != nil {
		t.Fatalf("write to fake stream: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(app.Buffer().String(), "hello-from-job") {
		app.Dispatch(struct{}{})
		time.Sleep(20 * time.Millisecond)
	}
	if buf := app.Buffer().String(); !strings.Contains(buf, "hello-from-job") {
		t.Fatalf("expected the stream's output on screen:\n%s", buf)
	}

	// End the stream (the job "exits") -- Terminal's readLoop sees EOF,
	// reports OnExit, and Model.Update's fullscreenExitedMsg handling
	// calls onDone and clears m.fullscreen (see terminal.go's own
	// io.EOF-is-a-clean-exit handling for the Stream path).
	fs.w.Close()

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !onDoneCalled {
		app.Dispatch(struct{}{})
		time.Sleep(20 * time.Millisecond)
	}
	if !onDoneCalled {
		t.Fatalf("onDone was never called after the stream ended:\n%s", app.Buffer().String())
	}
	forceRenders(app, 1)
	if buf := app.Buffer().String(); !strings.Contains(buf, "9sh>") {
		t.Fatalf("expected the kyu-repl prompt back on screen after the stream ended:\n%s", buf)
	}
}

// TestAttachBuiltinInsideTUITakesOverScreen is the full wiring proof:
// attach(job), called as real kyu code inside the interactive TUI,
// reaches biAttach's PassthroughBlocked/AttachHandler branch (see
// attach.go), which reaches replui's SetAttachHandler registration
// (kyurepl.go's evaluate), which reaches Model.fullscreen/View exactly
// like a fullscreen %cmd -- proven here against a *real* &pty job (not
// a fake stream), so the whole path from kyu source to pixels on
// screen is exercised in one test, the replui-side sibling of
// kyu/eval's own TestBackgroundPtyResize/attach_test.go coverage one
// layer down.
func TestAttachBuiltinInsideTUITakesOverScreen(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	m := New(env)
	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	// PassthroughBlocked must be set for attach() to reach the TUI
	// handoff branch at all -- see biAttach's own doc comment; cmd/9sh's
	// real runTUI does this before starting the TUI, this test does the
	// same thing directly since there's no real runTUI here.
	env.SetPassthroughBlocked("inside the test TUI")

	typeLineAndSubmit(t, app, m.replWidget, `j := %sh "-c" "echo hi-from-pty-job; read x" &pty`)
	typeLineAndSubmit(t, app, m.replWidget, `attach(j)`)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(app.Buffer().String(), "hi-from-pty-job") {
		app.Dispatch(struct{}{})
		time.Sleep(20 * time.Millisecond)
	}
	if buf := app.Buffer().String(); !strings.Contains(buf, "hi-from-pty-job") {
		t.Fatalf("expected the real &pty job's own output on screen:\n%s", buf)
	}

	// Unblock the job's "read x" so it exits on its own, ending the
	// stream from the job side (rather than this test simulating a
	// detach) -- the other half of the real round trip: a job that
	// finishes while attached should return control just like one the
	// user explicitly detaches from. Real key input, the same
	// app.HandleInput/dispatchAll path typeLineAndSubmit uses -- the
	// fullscreen Terminal is what's focused once m.fullscreen != nil
	// (see View()), so this reaches the job's stdin exactly like a real
	// keystroke would.
	dispatchAll(app, app.HandleInput(input.KeyEvent{Key: input.KeyEnter}))

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(app.Buffer().String(), "9sh>") {
		app.Dispatch(struct{}{})
		time.Sleep(20 * time.Millisecond)
	}
	if buf := app.Buffer().String(); !strings.Contains(buf, "9sh>") {
		t.Fatalf("expected the kyu-repl prompt back on screen after the job exited:\n%s", buf)
	}
}
