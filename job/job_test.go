package job

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func withTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSubprocessLifecycle(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if j.Status().State != StatePending {
		t.Fatalf("new job state = %v, want pending", j.Status().State)
	}
	if err := j.SetArgv([]string{"echo", "hello"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// echo doesn't read stdin, but os/exec's internal stdin-forwarding
	// goroutine still blocks Wait() until it sees EOF — a caller with
	// nothing to send must close stdin, exactly like a real unclosed pipe.
	if err := j.closeStdin(); err != nil {
		t.Fatalf("closeStdin: %v", err)
	}

	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateDone {
		t.Fatalf("final state = %v, want done (err=%s)", st.State, st.Err)
	}
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", st.ExitCode)
	}

	out, err := io.ReadAll(&growBufReader{ctx: withTimeout(t), buf: j.stdout})
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(out) != "hello\n" {
		t.Fatalf("stdout = %q, want %q", out, "hello\n")
	}
}

func TestSubprocessCannotStartWithoutArgv(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.Ctl("start"); err == nil {
		t.Fatal("start with empty argv should error")
	}
}

func TestSubprocessKill(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.SetArgv([]string{"sleep", "30"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	j.closeStdin()
	// give the process a moment to actually exec before killing it
	deadline := time.Now().Add(2 * time.Second)
	for j.Status().Pid == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := j.Ctl("kill"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateKilled {
		t.Fatalf("final state = %v, want killed", st.State)
	}
}

func TestCtlUnknownCommand(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.Ctl("frobnicate"); err == nil {
		t.Fatal("unknown ctl command should error, not silently no-op")
	}
}

func TestInprocLifecycle(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocInproc(func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		_, err = stdout.Write([]byte("echo:" + string(b)))
		return err
	})
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := j.writeStdin([]byte("hi")); err != nil {
		t.Fatalf("writeStdin: %v", err)
	}
	if err := j.closeStdin(); err != nil {
		t.Fatalf("closeStdin: %v", err)
	}
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateDone {
		t.Fatalf("final state = %v, want done (err=%s)", st.State, st.Err)
	}
	out, err := io.ReadAll(&growBufReader{ctx: withTimeout(t), buf: j.stdout})
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(out) != "echo:hi" {
		t.Fatalf("stdout = %q, want %q", out, "echo:hi")
	}
}

func TestInprocKillIsCooperativeCancellation(t *testing.T) {
	mgr := NewManager()
	started := make(chan struct{})
	j := mgr.AllocInproc(func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	<-started
	if err := j.Ctl("kill"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateFailed {
		t.Fatalf("final state = %v, want failed (ctx.Err() surfaces as a plain failure for inproc)", st.State)
	}
}

func TestInprocRejectsSubprocessOnlyCtl(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocInproc(func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		<-ctx.Done()
		return nil
	})
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := j.Ctl("stop"); err == nil {
		t.Fatal("stop should not be supported on an inproc job")
	}
	j.Ctl("kill")
}

func TestWaitIsImmediateOnceTerminal(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	j.SetArgv([]string{"true"})
	j.Ctl("start")
	j.closeStdin()
	if _, err := j.WaitFor(withTimeout(t)); err != nil {
		t.Fatalf("first WaitFor: %v", err)
	}
	// second call must return immediately (Plan-9 zombie-status convention),
	// not block again
	done := make(chan struct{})
	go func() {
		j.WaitFor(withTimeout(t))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second WaitFor on an already-terminal job blocked")
	}
}

func TestManagerListSortedByID(t *testing.T) {
	mgr := NewManager()
	a := mgr.AllocSubprocess()
	b := mgr.AllocSubprocess()
	c := mgr.AllocSubprocess()
	list := mgr.List()
	if len(list) != 3 || list[0].ID != a.ID || list[1].ID != b.ID || list[2].ID != c.ID {
		t.Fatalf("List() = %v, want sorted [%d %d %d]", ids(list), a.ID, b.ID, c.ID)
	}
}

func TestStatusCarriesCwdAndTimestamps(t *testing.T) {
	wantCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if j.Status().Cwd != wantCwd {
		t.Fatalf("Cwd = %q, want %q", j.Status().Cwd, wantCwd)
	}
	if !j.Status().StartedAt.IsZero() {
		t.Fatal("StartedAt should be zero before start()")
	}

	j.SetArgv([]string{"true"})
	before := time.Now()
	j.Ctl("start")
	j.closeStdin()
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	after := time.Now()

	if st.StartedAt.Before(before) || st.StartedAt.After(after) {
		t.Errorf("StartedAt = %v, want between %v and %v", st.StartedAt, before, after)
	}
	if st.FinishedAt.Before(st.StartedAt) || st.FinishedAt.After(after) {
		t.Errorf("FinishedAt = %v, want between StartedAt and %v", st.FinishedAt, after)
	}
}

// TestSetCwdOverridesDefault locks in cd's job-protocol plumbing: SetCwd
// (the job/fs.go "cwd" file's Write handler) overrides the default
// os.Getwd()-derived cwd TestStatusCarriesCwdAndTimestamps checks above,
// and startSubprocess actually honors it — a subprocess run there sees
// the overridden directory, not 9sh's own process cwd.
func TestSetCwdOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.SetCwd(dir); err != nil {
		t.Fatalf("SetCwd: %v", err)
	}
	if got := string(j.CwdBytes()); got != dir {
		t.Fatalf("CwdBytes = %q, want %q", got, dir)
	}
	if j.Status().Cwd != dir {
		t.Fatalf("Status().Cwd = %q, want %q", j.Status().Cwd, dir)
	}

	j.SetArgv([]string{"pwd"})
	j.Ctl("start")
	j.closeStdin()
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateDone {
		t.Fatalf("final state = %v, want done (err=%s)", st.State, st.Err)
	}
	out, err := io.ReadAll(&growBufReader{ctx: withTimeout(t), buf: j.stdout})
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(out) != dir+"\n" {
		t.Fatalf("pwd output = %q, want %q", out, dir+"\n")
	}
}

func TestSetCwdRejectsAfterStart(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	j.SetArgv([]string{"true"})
	j.Ctl("start")
	j.closeStdin()
	if _, err := j.WaitFor(withTimeout(t)); err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if err := j.SetCwd("/tmp"); err == nil {
		t.Fatal("SetCwd after start should error, got nil")
	}
}

// TestNonzeroExitIsDoneNotFailed locks in a real bug fix: a process
// that runs to completion and exits nonzero (grep's "no match"
// convention, sh's `exit N`, ...) is StateDone with that ExitCode, not
// StateFailed — a nonzero exit is ordinary process output, not a
// job-control failure. StateFailed is reserved for a job that never
// got to run at all. This matters beyond job's own semantics: kyu's
// %cmd (kyu/eval/external.go's runExternalViaJob) treats StateFailed
// specifically as an in-stream ErrorVal, so misclassifying a plain
// nonzero exit as failed would make ordinary shell exit codes look
// like %cmd itself broke.
func TestNonzeroExitIsDoneNotFailed(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	j.SetArgv([]string{"sh", "-c", "exit 3"})
	j.Ctl("start")
	j.closeStdin()

	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateDone {
		t.Fatalf("state = %v, want done (err=%s)", st.State, st.Err)
	}
	if st.ExitCode == nil || *st.ExitCode != 3 {
		t.Fatalf("exit code = %v, want 3", st.ExitCode)
	}
	if st.Err != "" {
		t.Errorf("Err = %q, want empty for a plain nonzero exit", st.Err)
	}
}

func TestSignalCapturedOnKill(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	j.SetArgv([]string{"sleep", "30"})
	j.Ctl("start")
	j.closeStdin()

	deadline := time.Now().Add(2 * time.Second)
	for j.Status().Pid == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := j.Ctl("kill"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateKilled {
		t.Fatalf("state = %v, want killed", st.State)
	}
	if st.Signal != "killed" {
		t.Fatalf("Signal = %q, want %q (syscall.SIGKILL.String())", st.Signal, "killed")
	}
}

func TestOnFinishFiresForEveryTerminalJob(t *testing.T) {
	mgr := NewManager()
	statuses := make(chan Status, 8)
	mgr.OnFinish(func(st Status) { statuses <- st })

	j1 := mgr.AllocSubprocess()
	j1.SetArgv([]string{"true"})
	j1.Ctl("start")
	j1.closeStdin()

	j2 := mgr.AllocInproc(func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		return nil
	})
	j2.Ctl("start")

	seen := map[int]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < 2 {
		select {
		case st := <-statuses:
			if !st.State.Terminal() {
				t.Fatalf("OnFinish delivered a non-terminal status: %+v", st)
			}
			seen[st.ID] = true
		case <-deadline:
			t.Fatalf("timed out waiting for OnFinish callbacks, got %d/2: %v", len(seen), seen)
		}
	}
	if !seen[j1.ID] || !seen[j2.ID] {
		t.Fatalf("expected callbacks for both jobs, got %v", seen)
	}
}

func TestOnFinishDoesNotBlockWaitFor(t *testing.T) {
	mgr := NewManager()
	mgr.OnFinish(func(st Status) {
		time.Sleep(200 * time.Millisecond) // deliberately slow
	})
	j := mgr.AllocSubprocess()
	j.SetArgv([]string{"true"})
	j.Ctl("start")
	j.closeStdin()

	start := time.Now()
	if _, err := j.WaitFor(withTimeout(t)); err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("WaitFor took %v — a slow OnFinish callback should not block it (it runs on its own goroutine)", elapsed)
	}
}

func ids(jobs []*Job) []int {
	out := make([]int, len(jobs))
	for i, j := range jobs {
		out[i] = j.ID
	}
	return out
}

// growBufReader adapts growBuf's (ctx, offset, p)-style Read to io.Reader
// for use with io.ReadAll in tests.
type growBufReader struct {
	ctx    context.Context
	buf    *growBuf
	offset int64
}

func (r *growBufReader) Read(p []byte) (int, error) {
	n, err := r.buf.Read(r.ctx, r.offset, p)
	r.offset += int64(n)
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, err
}

// TestCtlResizeExplainsThereIsNoPty: jobs run over pipes, so resize is a
// recognized command that always answers with a clear, permanent error —
// in any state — rather than "unknown command" or a promise of a later
// phase.
func TestCtlResizeExplainsThereIsNoPty(t *testing.T) {
	mgr := NewManager()
	pending := mgr.AllocSubprocess()

	running := mgr.AllocSubprocess()
	if err := running.SetArgv([]string{"sleep", "30"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := running.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer running.Ctl("kill")

	for name, j := range map[string]*Job{"pending": pending, "running": running} {
		err := j.Ctl("resize 24 80")
		if err == nil {
			t.Fatalf("%s job: resize should error, there is no pty", name)
		}
		if !strings.Contains(err.Error(), "no terminal to resize") {
			t.Errorf("%s job: error %q should say there is no terminal to resize", name, err)
		}
		if strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "later phase") {
			t.Errorf("%s job: error %q should be neither \"unknown command\" nor a promise of a later phase", name, err)
		}
	}
}

// TestPtyJobResize is the "opt-in pty job with resize, proven with stty
// size" step: a job that opts in via `ctl pty` gets a real pty instead
// of plain pipes, so `ctl resize` actually changes what the child sees
// via TIOCGWINSZ — verified by asking the real stty(1) binary, not by
// inspecting our own ioctl call. The child blocks on a line of stdin
// before running stty, so the resize (issued right after start, while
// pty.Start's own synchronous setup has already wired up j.ptyMaster)
// is guaranteed to land before stty reads the window size — otherwise
// this would be racing the child's own exec/read against the test.
func TestPtyJobResize(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.SetArgv([]string{"sh", "-c", "read x; stty size"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := j.Ctl("pty"); err != nil {
		t.Fatalf("ctl pty: %v", err)
	}
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !j.Status().Pty {
		t.Fatalf("status.pty = false, want true once opted in")
	}
	if err := j.Ctl("resize 40 120"); err != nil {
		t.Fatalf("ctl resize: %v", err)
	}
	if _, err := j.writeStdin([]byte("\n")); err != nil {
		t.Fatalf("writeStdin: %v", err)
	}

	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateDone {
		t.Fatalf("final state = %v, want done (err=%s)", st.State, st.Err)
	}

	out, err := io.ReadAll(&growBufReader{ctx: withTimeout(t), buf: j.stdout})
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "40 120" {
		t.Fatalf("stty size = %q, want %q", got, "40 120")
	}
}

// TestPtyJobMergesStdoutStderr: a real terminal has one output stream,
// not two, so a pty job's stderr writes land on the same growBuf as its
// stdout, and j.stderr (the field a plain job's stderr file reads from)
// stays untouched.
func TestPtyJobMergesStdoutStderr(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.SetArgv([]string{"sh", "-c", "echo out; echo err >&2"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := j.Ctl("pty"); err != nil {
		t.Fatalf("ctl pty: %v", err)
	}
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := j.closeStdin(); err != nil {
		t.Fatalf("closeStdin: %v", err) // a pty job's closeStdin is a documented no-op, not an error
	}

	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if st.State != StateDone {
		t.Fatalf("final state = %v, want done (err=%s)", st.State, st.Err)
	}

	out, err := io.ReadAll(&growBufReader{ctx: withTimeout(t), buf: j.stdout})
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if !strings.Contains(string(out), "out") || !strings.Contains(string(out), "err") {
		t.Fatalf("stdout = %q, want both \"out\" and \"err\" merged in", out)
	}
	stderrOut, err := io.ReadAll(&growBufReader{ctx: withTimeout(t), buf: j.stderr})
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if len(stderrOut) != 0 {
		t.Fatalf("stderr = %q, want empty — a pty job merges onto stdout", stderrOut)
	}
}

// TestPtyJobSignalIsProcessGroupWide: ctl signal on a pty job delivers
// via pty.Pty.Signal (kill(2) to the negative pid), not
// proc.Process.Signal, since pty.Start's Setsid makes the child its own
// process-group leader — checked indirectly here by confirming the
// signal actually reaches and terminates the child well before its own
// sleep would, the same "did the signal really land" shape
// TestCtlResizeExplainsThereIsNoPty's sibling tests use elsewhere in
// this file for plain jobs.
func TestPtyJobSignalIsProcessGroupWide(t *testing.T) {
	mgr := NewManager()
	j := mgr.AllocSubprocess()
	if err := j.SetArgv([]string{"sleep", "30"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := j.Ctl("pty"); err != nil {
		t.Fatalf("ctl pty: %v", err)
	}
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}

	start := time.Now()
	if err := j.Ctl("signal TERM"); err != nil {
		t.Fatalf("ctl signal TERM: %v", err)
	}
	st, err := j.WaitFor(withTimeout(t))
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("WaitFor took %v — signal TERM should have ended sleep 30 almost immediately", elapsed)
	}
	if st.State != StateDone {
		t.Fatalf("final state = %v, want done (a signal the job didn't ask for via kill is ordinary termination, not StateKilled)", st.State)
	}
}

func TestPtyJobOptInRejectedOnceRunningOrForInproc(t *testing.T) {
	mgr := NewManager()

	running := mgr.AllocSubprocess()
	if err := running.SetArgv([]string{"sleep", "30"}); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := running.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer running.Ctl("kill")
	if err := running.Ctl("pty"); err == nil {
		t.Fatalf("ctl pty on a running job should error, not silently do nothing")
	}

	inproc := mgr.AllocInproc(func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		return nil
	})
	if err := inproc.Ctl("pty"); err == nil || !strings.Contains(err.Error(), "inproc") {
		t.Fatalf("ctl pty on an inproc job: err = %v, want an error naming inproc jobs unsupported", err)
	}
}
