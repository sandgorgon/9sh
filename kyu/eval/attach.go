package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sandgorgon/9p/server"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/tui/term"
)

// detachByte is attach()'s "give me back my prompt" key: Ctrl-]
// (0x1d), telnet's own long-standing convention for the same purpose —
// deliberately different from 9mux's Ctrl+\ pane-nav key (a different
// process, so no real collision, but the same key meaning two
// different things across this project's own tools would be its own
// kind of confusing) and from every control character a real pty's
// line discipline needs to keep interpreting unchanged (Ctrl-C,
// Ctrl-D, Ctrl-Z all still have to reach the child for attach() to
// behave like a real terminal). Typed twice in a row, it reaches the
// child as a literal 0x1d instead of detaching — the same escape
// telnet itself uses.
const detachByte = 0x1d

// jobPtyFiles is attach's view of a &pty job: the same server.File
// handles the job record's own "stdin"/"stdout"/"ctl" fields already
// opened (via buildJobRecord), reached through value.Record.Backing
// rather than reopening fresh ones from a path — those are already
// correctly rooted even for an @host-remote job, which reconstructing
// a path from the calling Env's *current* JobRoot could get wrong once
// the @host{} block that built the job has since ended (see
// evalBackgroundSubprocess's own note on the same "the record, not the
// environment, is the source of truth" reasoning).
type jobPtyFiles struct {
	ctx       context.Context
	stdin     server.File
	stdout    server.File
	stdoutOff int64
	ctl       server.File
}

// jobPtyFilesFor extracts a &pty job's raw files from rec, or a clear
// error if rec isn't a pty job record at all (not a job, or a job
// without a pty — see job.SetPty/the kyu &pty syntax).
func jobPtyFilesFor(rec *value.Record) (*jobPtyFiles, error) {
	statusV, ok := rec.Get("status")
	if !ok {
		return nil, errors.New("attach: expected a job record (no status field)")
	}
	status, ok := statusV.(*value.Record)
	if !ok {
		return nil, errors.New("attach: expected a job record (status isn't a record)")
	}
	if ptyV, _ := status.Get("pty"); ptyV != value.Bool(true) {
		return nil, errors.New("attach: job has no pty — start it with &pty (or `ctl pty` before `ctl start`)")
	}

	stdinF, err := jobFileBacking(rec, "stdin")
	if err != nil {
		return nil, err
	}
	stdoutF, err := jobFileBacking(rec, "stdout")
	if err != nil {
		return nil, err
	}
	ctlF, err := jobFileBacking(rec, "ctl")
	if err != nil {
		return nil, err
	}
	return &jobPtyFiles{ctx: stdinF.ctx, stdin: stdinF.file, stdout: stdoutF.file, ctl: ctlF.file}, nil
}

// rawBackedField is the common shape bytesField and textField (both in
// namespace.go) share under the hood — just enough for attach to reach
// their already-open file and ctx, whichever concrete type backs name.
type rawBackedField struct {
	ctx  context.Context
	file server.File
}

func jobFileBacking(rec *value.Record, name string) (rawBackedField, error) {
	b, ok := rec.Backing(name)
	if !ok {
		return rawBackedField{}, fmt.Errorf("attach: job record has no %q field", name)
	}
	switch f := b.(type) {
	case *bytesField:
		return rawBackedField{ctx: f.ctx, file: f.file}, nil
	case *textField:
		return rawBackedField{ctx: f.ctx, file: f.file}, nil
	default:
		return rawBackedField{}, fmt.Errorf("attach: %q field has unexpected backing %T", name, b)
	}
}

// Read implements io.Reader against the job's live stdout stream —
// blocking for more output exactly like growBuf.Read already does for
// every other reader of a job's stdout (see job/growbuf.go), which is
// what makes this behave like a real pty's own blocking read.
func (f *jobPtyFiles) Read(p []byte) (int, error) {
	n, err := f.stdout.Read(f.ctx, f.stdoutOff, p)
	f.stdoutOff += int64(n)
	return n, err
}

// Write sends p to the job's stdin — reaching a real pty job's line
// discipline directly (Ctrl-D becomes EOF, Ctrl-C/Ctrl-Z become real
// signals), exactly like typing at a real terminal. offset is always 0:
// job/fs.go's stdinFile.Write ignores it (see job.Job.writeStdin).
func (f *jobPtyFiles) Write(p []byte) (int, error) {
	return f.stdin.Write(f.ctx, 0, p)
}

// Resize writes `resize ROWS COLS` to the job's ctl file — see
// job.Job.resize's own doc comment for why that order matches stty
// size's own output order.
func (f *jobPtyFiles) Resize(rows, cols int) error {
	_, err := f.ctl.Write(f.ctx, 0, []byte(fmt.Sprintf("resize %d %d", rows, cols)))
	return err
}

// Close clunks all three of the job's own files. It does not touch the
// job itself (no kill, no signal) — exactly like detaching, this only
// ever releases attach's own view of it. Every error is attempted and
// the first one returned, so one failed clunk doesn't leak the other
// two.
func (f *jobPtyFiles) Close() error {
	var firstErr error
	for _, file := range []server.File{f.stdin, f.stdout, f.ctl} {
		if err := file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var _ AttachStream = (*jobPtyFiles)(nil)

// attachCopyLoop shuttles bytes between the attaching terminal (in/out
// — already in whatever mode the caller wants; biAttach's real caller
// puts them in raw mode first) and a job's pty (job) until in reaches
// EOF/errors, the user sends detachByte, or writing to job fails (the
// job is gone). Returns nil in every case except a real, unexpected
// read error on in.
//
// The two directions run without any tighter synchronization than
// "both eventually stop": job -> out is a fire-and-forget io.Copy on
// its own goroutine, and in -> job is this call's own loop, which is
// what decides when to return. There is no portable, race-free way to
// cancel a blocked read on a real terminal's stdin from another
// goroutine (closing the fd out from under a concurrent Read is
// undefined enough behavior across platforms not to rely on), so this
// never tries to. Consequences, both accepted and worth knowing before
// calling this: (1) job -> out simply stops delivering once the job's
// own pty closes — nothing depends on that goroutine finishing, so it
// isn't waited on, and a slow/never-draining case just leaks a goroutine
// no worse than an ordinary unclosed io.Copy would; (2) if the job
// exits while the user is mid-session and not actively typing, nothing
// forces control back to the prompt automatically — the user notices
// (no more output) and either presses detach or types anything, which
// then bounces them back via the resulting Write failure below. Both
// are the direct cost of there being no cancellable stdin read to build
// on, not an oversight.
func attachCopyLoop(in io.Reader, out io.Writer, job interface {
	io.Reader
	io.Writer
}) error {
	go func() { _, _ = io.Copy(out, job) }()

	buf := make([]byte, 4096)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if detached := forwardChunk(job, buf[:n]); detached {
				return nil
			}
		}
		if err != nil {
			return nil // local input closed/errored — nothing more to forward
		}
	}
}

// forwardChunk writes chunk to job, splitting on any detachByte it
// contains: a lone detachByte ends the session (returns true, without
// writing it through); a doubled one (telnet's own escape convention)
// writes a single literal detachByte through and keeps going. Also
// returns true — attachCopyLoop treats the two identically, ending the
// session either way — on a job.Write failure (the job is gone, so
// there's nothing left to distinguish "detached" from "nothing left to
// attach to" by the time control gets back to the caller).
func forwardChunk(job io.Writer, chunk []byte) (detached bool) {
	for len(chunk) > 0 {
		idx := bytes.IndexByte(chunk, detachByte)
		if idx < 0 {
			if _, err := job.Write(chunk); err != nil {
				return true
			}
			return false
		}
		if idx > 0 {
			if _, err := job.Write(chunk[:idx]); err != nil {
				return true
			}
		}
		rest := chunk[idx+1:]
		if len(rest) > 0 && rest[0] == detachByte {
			if _, err := job.Write([]byte{detachByte}); err != nil {
				return true
			}
			chunk = rest[1:]
			continue
		}
		return true
	}
	return false
}

// biAttach implements attach(job): takes over the local terminal and
// streams raw bytes directly to/from a &pty job's own pty — the
// "no ssh needed" client for pty jobs (see job.Job.SetPty's own doc
// comment), local or, via @host{}, remote, since jobPtyFilesFor's
// files are already correctly rooted at whichever host built the job
// record regardless of where attach() itself is called from.
//
// Inside the interactive TUI (env.PassthroughBlocked() non-empty —
// direct stdio inheritance isn't safe there, see its own doc comment),
// this hands off to whatever AttachHandlerFunc package replui has
// registered (a real terminal-emulator widget, via tui's pty.Stream
// seam) instead of driving os.Stdin/os.Stdout raw-mode passthrough
// itself, the same "checkout-and-handoff instead of blocking
// synchronously" shape runExternalFullscreen already uses for a
// fullscreen %cmd. No handler registered (a plain build without
// replui's TUI, or a bug in the registration) is a clear error, not a
// silent no-op or a corrupted terminal.
func biAttach(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("attach: expected 1 argument (a job), got %d", len(args))
	}
	rec, ok := args[0].(*value.Record)
	if !ok {
		return nil, fmt.Errorf("attach: expected a job record, got %s", args[0].Kind())
	}
	files, err := jobPtyFilesFor(rec)
	if err != nil {
		return nil, err
	}
	if reason := env.PassthroughBlocked(); reason != "" {
		handler := env.AttachHandler()
		if handler == nil {
			return nil, fmt.Errorf("attach: %s", reason)
		}
		handler(files, func(err error) {})
		return value.Null{}, nil
	}
	if !term.IsTerminal(os.Stdin) {
		return nil, errors.New("attach: stdin isn't a terminal")
	}

	saved, err := term.MakeRaw(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	defer term.Restore(os.Stdin, saved)

	if sz, err := term.GetSize(os.Stdout); err == nil {
		_ = files.Resize(sz.Rows, sz.Cols)
	}
	watcher := term.NewWatcher()
	defer watcher.Stop()
	resizeQuit := make(chan struct{})
	defer close(resizeQuit)
	go func() {
		for {
			select {
			case <-watcher.Resize():
				if sz, err := term.GetSize(os.Stdout); err == nil {
					_ = files.Resize(sz.Rows, sz.Cols)
				}
			case <-resizeQuit:
				return
			}
		}
	}()

	fmt.Fprint(os.Stdout, "\r\n[attached — Ctrl-] to detach]\r\n")
	_ = attachCopyLoop(os.Stdin, os.Stdout, files)
	fmt.Fprint(os.Stdout, "\r\n[detached]\r\n")
	return value.Null{}, nil
}
