package job

import (
	"context"
	"errors"
	"io"
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
