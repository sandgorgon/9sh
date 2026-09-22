package eval

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sandgorgon/9sh/job"
)

// syncBuffer is bytes.Buffer plus a mutex: attachCopyLoop writes to its
// "out" from a background goroutine (see attach.go's own doc comment
// on why job -> out is never synchronized with the caller's return), so
// a test that also reads the buffer from its own goroutine (to poll for
// output) needs real synchronization -- a bare bytes.Buffer would be a
// genuine data race here, unlike production's real os.Stdout target.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --- forwardChunk: pure unit tests, no pty involved ---

func TestForwardChunkPlainBytes(t *testing.T) {
	var buf bytes.Buffer
	if detached := forwardChunk(&buf, []byte("hello")); detached {
		t.Fatal("want not detached")
	}
	if buf.String() != "hello" {
		t.Fatalf("wrote %q, want %q", buf.String(), "hello")
	}
}

func TestForwardChunkLoneDetachByteDetaches(t *testing.T) {
	var buf bytes.Buffer
	if detached := forwardChunk(&buf, []byte{'h', 'i', detachByte, 'x'}); !detached {
		t.Fatal("want detached")
	}
	if buf.String() != "hi" {
		t.Fatalf("wrote %q, want %q (nothing after the detach byte, and not the detach byte itself)", buf.String(), "hi")
	}
}

func TestForwardChunkDoubledDetachByteIsLiteral(t *testing.T) {
	var buf bytes.Buffer
	// "a" + detach + detach + "b" + detach -> "a" + one literal detach
	// byte + "b" written through, then detaches on the trailing lone one.
	chunk := []byte{'a', detachByte, detachByte, 'b', detachByte}
	if detached := forwardChunk(&buf, chunk); !detached {
		t.Fatal("want detached")
	}
	want := []byte{'a', detachByte, 'b'}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("wrote %v, want %v", buf.Bytes(), want)
	}
}

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestForwardChunkWriteFailureDetaches(t *testing.T) {
	if detached := forwardChunk(errWriter{}, []byte("hello")); !detached {
		t.Fatal("want detached on a write failure -- nothing left to attach to")
	}
}

// --- attachCopyLoop: pure unit test against in-memory pipes ---

// fakeJob is a minimal io.ReadWriter standing in for a job's pty in
// attachCopyLoop tests that don't need a real pty (jobPtyFilesEndToEnd
// below covers that separately, against a real &pty job).
type fakeJob struct {
	out *bytes.Buffer // what was written to the "job" (i.e. sent as input)
	in  io.Reader     // what the "job" sends back (i.e. its output)
}

func (f *fakeJob) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeJob) Write(p []byte) (int, error) { return f.out.Write(p) }

func TestAttachCopyLoopForwardsBothWaysAndDetaches(t *testing.T) {
	localIn, localInW := io.Pipe()
	var localOut syncBuffer
	fj := &fakeJob{out: &bytes.Buffer{}, in: strings.NewReader("job says hi")}

	done := make(chan error, 1)
	go func() { done <- attachCopyLoop(localIn, &localOut, fj) }()

	if _, err := localInW.Write([]byte("hello")); err != nil {
		t.Fatalf("write local input: %v", err)
	}
	if _, err := localInW.Write([]byte{detachByte}); err != nil {
		t.Fatalf("write detach byte: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attachCopyLoop returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attachCopyLoop didn't return after detach byte")
	}

	if fj.out.String() != "hello" {
		t.Fatalf("job received %q, want %q", fj.out.String(), "hello")
	}
	// The job -> out copy runs on its own goroutine (see attachCopyLoop's
	// doc comment); give it a moment to actually deliver before checking.
	deadline := time.Now().Add(2 * time.Second)
	for localOut.String() != "job says hi" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := localOut.String(); got != "job says hi" {
		t.Fatalf("local output = %q, want %q", got, "job says hi")
	}
}

// --- jobPtyFiles: end-to-end against a real &pty job ---

// attachTestJob starts a real &pty job (argv) and returns a *jobPtyFiles
// wired to its actual server.File handles -- the same in-process Walk a
// real ns.Namespace-backed kyu job record reaches under the hood
// (jobPtyFilesFor, via buildJobRecord's own opened files), just built
// directly against package job here instead of through the full kyu
// eval/namespace machinery, which this package's own tests don't set up.
func attachTestJob(t *testing.T, argv []string) *jobPtyFiles {
	t.Helper()
	mgr := job.NewManager()
	j := mgr.AllocSubprocess()
	if err := j.SetArgv(argv); err != nil {
		t.Fatalf("SetArgv: %v", err)
	}
	if err := j.Ctl("pty"); err != nil {
		t.Fatalf("ctl pty: %v", err)
	}
	if err := j.Ctl("start"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// A real deadline, not context.Background() (what biAttach's own
	// production ctx would be — an attach session waits indefinitely by
	// design): growBuf.Read honors ctx cancellation, so a hung read here
	// fails this test promptly instead of blocking until the whole test
	// binary's own timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	root, err := job.New(mgr).Attach(ctx, "u", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	dir, err := root.Walk(ctx, strconv.Itoa(j.ID))
	if err != nil {
		t.Fatalf("walk job dir: %v", err)
	}
	stdin, err := dir.Walk(ctx, "stdin")
	if err != nil {
		t.Fatalf("walk stdin: %v", err)
	}
	stdout, err := dir.Walk(ctx, "stdout")
	if err != nil {
		t.Fatalf("walk stdout: %v", err)
	}
	ctl, err := dir.Walk(ctx, "ctl")
	if err != nil {
		t.Fatalf("walk ctl: %v", err)
	}
	return &jobPtyFiles{ctx: ctx, stdin: stdin, stdout: stdout, ctl: ctl}
}

// TestJobPtyFilesEndToEnd is attach's own version of job.TestPtyJobResize
// and job.TestPtyJobResize's kyu-level sibling (eval_test.go's
// TestBackgroundPtyResize): resize through jobPtyFiles.Resize, script
// through jobPtyFiles.Write, read through jobPtyFiles.Read -- the exact
// three operations attachCopyLoop drives, proven here against a real
// pty job rather than a fake one.
func TestJobPtyFilesEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	if _, err := exec.LookPath("stty"); err != nil {
		t.Skip("stty not on PATH")
	}
	files := attachTestJob(t, []string{"sh", "-c", "read x; stty size; echo got $x"})

	if err := files.Resize(40, 120); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := files.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out, err := readUntilContains(t, files, "got hello", 5*time.Second)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if !strings.Contains(out, "40 120") {
		t.Fatalf("output %q should contain the resized \"40 120\"", out)
	}
	if !strings.Contains(out, "got hello") {
		t.Fatalf("output %q should contain \"got hello\"", out)
	}
}

// readUntilContains reads from files until its accumulated output
// contains want or the deadline passes.
func readUntilContains(t *testing.T, files *jobPtyFiles, want string, timeout time.Duration) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	deadline := time.Now().Add(timeout)
	p := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := files.Read(p)
		if n > 0 {
			buf.Write(p[:n])
			if strings.Contains(buf.String(), want) {
				return buf.String(), nil
			}
		}
		if err != nil {
			return buf.String(), err
		}
	}
	return buf.String(), errors.New("timed out waiting for output")
}
