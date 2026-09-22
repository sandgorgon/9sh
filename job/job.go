// Package job implements 9sh's local job-control model: a Manager that
// allocates Jobs (native-inproc or native-subprocess), and (in fs.go) the
// server.FileSystem that exposes them as the /jobs synthetic namespace
// described in the design doc's job-control file protocol.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sandgorgon/9sh/pathresolve"
	"github.com/sandgorgon/tui/pty"
	"github.com/sandgorgon/tui/term"
)

type State string

const (
	StatePending State = "pending"
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateKilled  State = "killed"
)

// Terminal reports whether the state is a final one — Job.WaitFor blocks
// until this is true, and the stdout/stderr/events streams close then.
func (s State) Terminal() bool {
	switch s {
	case StateDone, StateFailed, StateKilled:
		return true
	}
	return false
}

type Kind string

const (
	KindSubprocess Kind = "subprocess"
	KindInproc     Kind = "inproc"
)

// InprocFunc is a native-inproc job's body: it runs as a goroutine, not a
// forked process, but otherwise plays the same role as a subprocess —
// reading stdin, writing stdout/stderr, and returning an error on
// failure. It should return promptly once ctx is cancelled: a `kill` ctl
// command on an inproc job has no OS process to signal, so it can only
// cancel ctx and wait for the goroutine to notice cooperatively.
//
// There is no way yet to create an inproc job over the wire (the `clone`
// file only allocates subprocess jobs) — kyu has no syntax yet to hand a
// closure to the job system. AllocInproc is a Go-level entry point,
// exercised directly by this package's own tests, until that syntax
// integration is designed.
type InprocFunc func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error

// Status is a job's point-in-time snapshot, as served by the status and
// wait files and appended (one per transition) to events. The design doc
// calls for NRF/NRL encoding; this uses JSON as a placeholder until that
// codec exists (see kyu/eval/external.go's renderForExternal for the same
// deferral on the %cmd side).
type Status struct {
	ID         int       `json:"id"`
	Kind       Kind      `json:"kind"`
	State      State     `json:"state"`
	Argv       []string  `json:"argv,omitempty"`
	Pid        int       `json:"pid,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Signal     string    `json:"signal,omitempty"`
	Err        string    `json:"error,omitempty"`
	Detached   bool      `json:"detached,omitempty"`
	Pty        bool      `json:"pty,omitempty"`
	Cwd        string    `json:"cwd,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

type Job struct {
	ID   int
	kind Kind

	mu         sync.Mutex
	state      State
	argv       []string
	env        []string
	pid        int
	exitCode   *int
	lastSignal string
	errMsg     string
	detached   bool
	killed     bool // set by kill() before cancel(), to distinguish "we killed it" from a plain nonzero exit
	cwd        string
	startedAt  time.Time
	finishedAt time.Time

	usePty    bool     // opted in via SetPty, before start; subprocess-only
	ptyMaster *pty.Pty // set once startSubprocessPty's pty.Start succeeds; nil until then and for non-pty jobs

	stdinW io.WriteCloser
	stdinR io.ReadCloser
	stdout *growBuf
	stderr *growBuf
	events *growBuf

	waitCh chan struct{}
	cancel context.CancelFunc

	proc *exec.Cmd
	fn   InprocFunc

	mgr *Manager // for notifyFinished's OnFinish callback; never nil (always set by allocLocked)
}

// notifyFinished runs the owning Manager's OnFinish hook (if any),
// asynchronously — the session recorder it exists for does its own
// I/O (an fs append, occasionally a 9vcs record), which must never
// block a job's own finish() and, transitively, whatever's waiting on
// it via WaitFor.
func (j *Job) notifyFinished() {
	fn := j.mgr.getOnFinish()
	if fn == nil {
		return
	}
	go fn(j.Status())
}

func (j *Job) Kind() Kind { return j.kind }

// Status returns a point-in-time snapshot, safe to call from any goroutine.
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Status{
		ID: j.ID, Kind: j.kind, State: j.state,
		Argv: append([]string(nil), j.argv...),
		Pid:  j.pid, ExitCode: j.exitCode, Signal: j.lastSignal, Err: j.errMsg, Detached: j.detached,
		Pty: j.usePty, Cwd: j.cwd, StartedAt: j.startedAt, FinishedAt: j.finishedAt,
	}
}

// WaitFor blocks until the job reaches a terminal state (returning
// immediately if it already has — the Plan-9 zombie-status convention) or
// ctx is cancelled.
func (j *Job) WaitFor(ctx context.Context) (Status, error) {
	select {
	case <-j.waitCh:
		return j.Status(), nil
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
}

func (j *Job) ArgvBytes() []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	return []byte(strings.Join(j.argv, "\n"))
}

func (j *Job) EnvBytes() []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	return []byte(strings.Join(j.env, "\n"))
}

func (j *Job) SetArgv(argv []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StatePending {
		return fmt.Errorf("job %d: cannot set argv: already %s", j.ID, j.state)
	}
	j.argv = argv
	return nil
}

func (j *Job) SetEnv(env []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StatePending {
		return fmt.Errorf("job %d: cannot set env: already %s", j.ID, j.state)
	}
	j.env = env
	return nil
}

// CwdBytes returns the job's working directory. Unlike ArgvBytes/
// EnvBytes, this is populated even before SetCwd is ever called — job
// creation defaults j.cwd to 9sh's own process cwd (a best-effort
// placeholder, see New's doc comment), and SetCwd (a kyu `cd(...)`
// reaching the job protocol's "cwd" file before `ctl start`) overwrites
// that default rather than leaving it unset.
func (j *Job) CwdBytes() []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	return []byte(j.cwd)
}

func (j *Job) SetCwd(cwd string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StatePending {
		return fmt.Errorf("job %d: cannot set cwd: already %s", j.ID, j.state)
	}
	j.cwd = cwd
	return nil
}

// SetPty opts a pending subprocess job into a real pty instead of plain
// pipes: stdout and stderr merge onto the pty's single stream (a real
// terminal has no separate stderr fd — see startSubprocessPty), stdin
// writes reach the pty master so a client can send control characters
// exactly like a real terminal's line discipline (Ctrl-D for EOF
// instead of closing a file, Ctrl-C/Ctrl-Z for real signals), and `ctl
// resize` becomes meaningful instead of erroring. Inproc jobs have no
// OS process to attach a pty to.
func (j *Job) SetPty() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != StatePending {
		return fmt.Errorf("job %d: cannot set pty: already %s", j.ID, j.state)
	}
	if j.kind != KindSubprocess {
		return fmt.Errorf("job %d: pty: not supported for inproc jobs", j.ID)
	}
	j.usePty = true
	return nil
}

func (j *Job) writeStdin(p []byte) (int, error) {
	j.mu.Lock()
	master := j.ptyMaster
	w := j.stdinW
	j.mu.Unlock()
	if master != nil {
		return master.Write(p)
	}
	if w == nil {
		return 0, fmt.Errorf("job %d: stdin closed", j.ID)
	}
	return w.Write(p)
}

// closeStdin closes the plain-pipe stdin's write end, signaling EOF the
// way an ordinary (non-pty) job's stdin always has. A pty job has no
// separate stdin fd to close — the pty master is the same handle stdout
// is read from and resize/signal are delivered through, so closing it
// here would tear down the whole job, not just stdin. EOF for a pty
// job's child comes from a real Ctrl-D byte (0x04) written through
// writeStdin instead, exactly like a real terminal's line discipline.
func (j *Job) closeStdin() error {
	j.mu.Lock()
	if j.ptyMaster != nil {
		j.mu.Unlock()
		return nil
	}
	w := j.stdinW
	j.stdinW = nil
	j.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.Close()
}

// appendEvent snapshots the current status onto the events stream. Called
// after releasing j.mu (it takes its own lock via Status).
func (j *Job) appendEvent() {
	b, _ := json.Marshal(j.Status())
	b = append(b, '\n')
	_, _ = j.events.Write(b)
}

var signalByName = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "HUP": syscall.SIGHUP,
	"KILL": syscall.SIGKILL, "STOP": syscall.SIGSTOP, "CONT": syscall.SIGCONT,
	"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2, "QUIT": syscall.SIGQUIT,
}

// Ctl dispatches one control command — see the design doc's job-control
// file protocol for the vocabulary. A malformed or inapplicable command
// returns a real error, never a silent no-op.
func (j *Job) Ctl(cmd string) error {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return errors.New("ctl: empty command")
	}
	switch fields[0] {
	case "start":
		return j.start()
	case "stop":
		return j.signal(syscall.SIGSTOP, "stop", StateStopped)
	case "resume":
		return j.signal(syscall.SIGCONT, "resume", StateRunning)
	case "kill":
		return j.kill()
	case "signal":
		if len(fields) != 2 {
			return errors.New("ctl: signal: expected a signal name")
		}
		sig, ok := signalByName[strings.ToUpper(fields[1])]
		if !ok {
			return fmt.Errorf("ctl: signal: unknown signal %q", fields[1])
		}
		return j.signal(sig, "signal "+fields[1], "")
	case "priority":
		if len(fields) != 2 {
			return errors.New("ctl: priority: expected a priority number")
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return fmt.Errorf("ctl: priority: %w", err)
		}
		return j.setPriority(n)
	case "pty":
		return j.SetPty()
	case "resize":
		if len(fields) != 3 {
			return errors.New("ctl: resize: expected a row count and a column count")
		}
		rows, err := strconv.Atoi(fields[1])
		if err != nil {
			return fmt.Errorf("ctl: resize: rows: %w", err)
		}
		cols, err := strconv.Atoi(fields[2])
		if err != nil {
			return fmt.Errorf("ctl: resize: cols: %w", err)
		}
		return j.resize(rows, cols)
	case "detach":
		j.mu.Lock()
		j.detached = true
		j.mu.Unlock()
		j.appendEvent()
		return nil
	default:
		return fmt.Errorf("ctl: unknown command %q", fields[0])
	}
}

func (j *Job) start() error {
	j.mu.Lock()
	if j.state != StatePending {
		state := j.state
		j.mu.Unlock()
		return fmt.Errorf("job %d: cannot start: already %s", j.ID, state)
	}
	if j.kind == KindSubprocess && len(j.argv) == 0 {
		j.mu.Unlock()
		return fmt.Errorf("job %d: cannot start: argv is empty", j.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	j.state = StateRunning
	j.startedAt = time.Now()
	usePty := j.usePty // fixed by now: SetPty only succeeds while state == StatePending, checked under this same lock above
	j.mu.Unlock()
	j.appendEvent()

	switch j.kind {
	case KindSubprocess:
		if usePty {
			return j.startSubprocessPty(ctx)
		}
		return j.startSubprocess(ctx)
	case KindInproc:
		go func() {
			err := j.fn(ctx, j.stdinR, j.stdout, j.stderr)
			if err != nil {
				j.finish(StateFailed, nil, "", err.Error())
			} else {
				zero := 0
				j.finish(StateDone, &zero, "", "")
			}
		}()
		return nil
	default:
		return fmt.Errorf("job %d: unknown kind %q", j.ID, j.kind)
	}
}

func (j *Job) startSubprocess(ctx context.Context) error {
	j.mu.Lock()
	argv := append([]string(nil), j.argv...)
	env := append([]string(nil), j.env...)
	cwd := j.cwd
	j.mu.Unlock()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if len(env) > 0 {
		cmd.Env = env
	}
	// exec.CommandContext already resolved argv[0] against *this
	// process's* own PATH above (cmd.Path) -- irrelevant if env carries
	// its own PATH override (kyu's setenv, via /env), which a bare
	// Cmd.Env assignment never affects since Go only consults PATH at
	// construction time. Re-resolve using env's PATH instead (falling
	// back to the real exec.LookPath when env has no PATH entry, so
	// this is a no-op when there's nothing to override) and clear any
	// stale lookup failure from the first attempt, which Start()
	// otherwise returns unconditionally before ever using cmd.Path. See
	// package pathresolve's doc comment.
	if resolved, err := pathresolve.LookPath(argv[0], env); err != nil {
		j.finish(StateFailed, nil, "", err.Error())
		return nil // the failure is job status, not a ctl-command error — matches cmd.Start()'s own failure handling below
	} else {
		cmd.Path = resolved
		cmd.Err = nil
	}
	cmd.Dir = cwd
	cmd.Stdin = j.stdinR
	cmd.Stdout = j.stdout
	cmd.Stderr = j.stderr
	// CommandContext kills the process on ctx cancellation (our `kill`
	// ctl command), so no separate signal-on-cancel wiring is needed.

	if err := cmd.Start(); err != nil {
		j.finish(StateFailed, nil, "", err.Error())
		return nil // the failure is job status, not a ctl-command error
	}
	j.mu.Lock()
	j.pid = cmd.Process.Pid
	j.proc = cmd
	j.mu.Unlock()
	j.appendEvent()

	go func() {
		j.finishFromExec(cmd.Wait())
	}()
	return nil
}

// startSubprocessPty is startSubprocess's counterpart for a job that
// opted in via SetPty: stdin/stdout/stderr attach to a real pty instead
// of plain pipes, giving real line-discipline behavior (Ctrl-D signals
// EOF, Ctrl-C/Ctrl-Z become real signals) and a resizable window
// instead of `ctl resize`'s "jobs run over pipes" answer. stdout and
// stderr are the same growBuf — a real terminal has one output stream,
// not two — so j.stderr is left empty for a pty job; a reader wanting
// the merged stream reads stdout.
func (j *Job) startSubprocessPty(ctx context.Context) error {
	j.mu.Lock()
	argv := append([]string(nil), j.argv...)
	env := append([]string(nil), j.env...)
	cwd := j.cwd
	j.mu.Unlock()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if len(env) > 0 {
		cmd.Env = env
	}
	// See startSubprocess's matching comment: re-resolve against env's
	// own PATH rather than trusting exec.CommandContext's lookup against
	// this process's PATH.
	if resolved, err := pathresolve.LookPath(argv[0], env); err != nil {
		j.finish(StateFailed, nil, "", err.Error())
		return nil
	} else {
		cmd.Path = resolved
		cmd.Err = nil
	}
	cmd.Dir = cwd
	// pty.Start sets Stdin/Stdout/Stderr and the Setsid/Setctty
	// SysProcAttr itself, then calls cmd.Start() — but Cancel must be
	// overridden *before* that call: exec.CommandContext's default
	// Cancel only signals the direct child, while pty.Start's Setsid
	// makes that child its own session and process-group leader, so a
	// `ctl kill` (ctx cancellation) should take the whole group with it —
	// the same "process-group signals" behavior `ctl signal`/Job.signal
	// gets from pty.Pty.Signal below.
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	master, err := pty.Start(cmd)
	if err != nil {
		j.finish(StateFailed, nil, "", err.Error())
		return nil
	}
	j.mu.Lock()
	j.pid = cmd.Process.Pid
	j.proc = cmd
	j.ptyMaster = master
	j.mu.Unlock()
	j.appendEvent()

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, err := io.Copy(j.stdout, master)
		// A pty master read returns EIO, not io.EOF, once the child's
		// last reference to the slave closes on exit — a ptmx quirk (see
		// tty_ioctl(4)), not a real read error worth surfacing.
		if err != nil && !errors.Is(err, syscall.EIO) {
			fmt.Fprintf(os.Stderr, "9sh: job %d: pty read: %v\n", j.ID, err)
		}
	}()

	go func() {
		waitErr := cmd.Wait()
		<-copyDone // drain whatever pty output is still in flight before declaring the job terminal, so stdout isn't truncated
		master.Close()
		j.finishFromExec(waitErr)
	}()
	return nil
}

// resize sets a running pty job's window size, visible to the child via
// TIOCGWINSZ — the kernel delivers SIGWINCH itself on a real change (see
// pty.Pty.Resize's own doc comment), so this never sends one directly.
// "resize <rows> <cols>" deliberately matches stty size's own output
// order, so a client's resize handler can forward that pair straight
// through.
func (j *Job) resize(rows, cols int) error {
	j.mu.Lock()
	usePty := j.usePty
	master := j.ptyMaster
	j.mu.Unlock()
	if !usePty {
		return errors.New("ctl: resize: jobs run over pipes, not a pty, so there is no terminal to resize (opt in with `ctl pty` before start, or use fullscreen_programs for an interactive program)")
	}
	if master == nil {
		return fmt.Errorf("job %d: resize: job has no pty yet (not started)", j.ID)
	}
	return master.Resize(term.Size{Rows: rows, Cols: cols})
}

// finishFromExec classifies cmd.Wait()'s result. StateFailed is reserved
// for a job that never got to run at all (exec.Start() itself failing —
// handled separately, in startSubprocess) or an unexpected non-exit
// error from Wait(); a process that *ran* and exited, even nonzero or
// via a signal it didn't ask for, is StateDone — a nonzero exit code is
// ordinary process output, not a job-control failure (the same
// principle kyu/eval/external.go's %cmd handling already relies on:
// "exit codes are ordinary shell-level data"). Only our own `ctl kill`
// (the killed flag, set before cancel()) produces StateKilled.
func (j *Job) finishFromExec(err error) {
	j.mu.Lock()
	killed := j.killed
	j.mu.Unlock()

	if err == nil {
		zero := 0
		j.finish(StateDone, &zero, "", "")
		return
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code := exitErr.ExitCode()
		sig := signalFromExitError(exitErr)
		if killed {
			j.finish(StateKilled, &code, sig, "")
		} else {
			j.finish(StateDone, &code, sig, "")
		}
		return
	}
	j.finish(StateFailed, nil, "", err.Error())
}

// signalFromExitError reports the signal that terminated a process, or
// "" if it exited normally (a non-zero exit code alone isn't a signal).
func signalFromExitError(err *exec.ExitError) string {
	ws, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	return ws.Signal().String()
}

// finish makes a terminal-state transition, idempotently (the first
// caller wins — a natural exit racing a kill() is expected).
func (j *Job) finish(state State, exitCode *int, signal, errMsg string) {
	j.mu.Lock()
	if j.state.Terminal() {
		j.mu.Unlock()
		return
	}
	j.state = state
	j.exitCode = exitCode
	j.lastSignal = signal
	j.errMsg = errMsg
	j.finishedAt = time.Now()
	j.mu.Unlock()

	close(j.waitCh)
	j.appendEvent()
	j.stdout.Close()
	j.stderr.Close()
	j.events.Close()
	j.notifyFinished()
}

func (j *Job) kill() error {
	j.mu.Lock()
	if j.state.Terminal() {
		state := j.state
		j.mu.Unlock()
		return fmt.Errorf("job %d: kill: already %s", j.ID, state)
	}
	if j.state == StatePending {
		j.state = StateKilled
		j.finishedAt = time.Now()
		j.mu.Unlock()
		close(j.waitCh)
		j.appendEvent()
		j.stdout.Close()
		j.stderr.Close()
		j.events.Close()
		j.notifyFinished()
		return nil
	}
	j.killed = true
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// signal delivers an OS signal to a running subprocess job. wantState, if
// non-empty, is the state to move to on success (used for stop/resume);
// a plain `signal <name>` passes "" and leaves state alone. A pty job
// delivers to its whole process group (pty.Start's Setsid makes the
// child its own group leader), matching what a real controlling
// terminal's line discipline does for an ISIG-triggered signal; a plain
// job signals just the one tracked pid, as before.
func (j *Job) signal(sig syscall.Signal, label string, wantState State) error {
	j.mu.Lock()
	if j.kind != KindSubprocess {
		j.mu.Unlock()
		return fmt.Errorf("job %d: %s: not supported for inproc jobs", j.ID, label)
	}
	if j.state != StateRunning && j.state != StateStopped {
		state := j.state
		j.mu.Unlock()
		return fmt.Errorf("job %d: %s: job is %s, not running", j.ID, label, state)
	}
	proc := j.proc
	master := j.ptyMaster
	j.mu.Unlock()
	if master != nil {
		if err := master.Signal(sig); err != nil {
			return fmt.Errorf("job %d: %s: %w", j.ID, label, err)
		}
	} else {
		if proc == nil || proc.Process == nil {
			return fmt.Errorf("job %d: %s: process not started", j.ID, label)
		}
		if err := proc.Process.Signal(sig); err != nil {
			return fmt.Errorf("job %d: %s: %w", j.ID, label, err)
		}
	}
	if wantState != "" {
		j.mu.Lock()
		j.state = wantState
		j.mu.Unlock()
		j.appendEvent()
	}
	return nil
}

func (j *Job) setPriority(n int) error {
	j.mu.Lock()
	if j.kind != KindSubprocess {
		j.mu.Unlock()
		return fmt.Errorf("job %d: priority: not supported for inproc jobs", j.ID)
	}
	if j.state != StateRunning && j.state != StateStopped {
		state := j.state
		j.mu.Unlock()
		return fmt.Errorf("job %d: priority: job is %s, not running", j.ID, state)
	}
	pid := j.pid
	j.mu.Unlock()
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, n)
}

// Manager allocates and tracks Jobs.
type Manager struct {
	mu       sync.Mutex
	nextID   int
	jobs     map[int]*Job
	onFinish func(Status)
}

func NewManager() *Manager {
	return &Manager{jobs: map[int]*Job{}}
}

// OnFinish registers fn to be called, on its own goroutine, whenever
// any job this Manager owns reaches a terminal state — the hook 9sh's
// session recorder (package session) attaches to build history from,
// so "every job gets a history line" falls out of the job-control
// mechanism itself rather than needing a separate logging path. Only
// one callback is supported; a second call replaces the first.
func (m *Manager) OnFinish(fn func(Status)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onFinish = fn
}

func (m *Manager) getOnFinish() func(Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onFinish
}

func (m *Manager) allocLocked(kind Kind) *Job {
	m.nextID++
	pr, pw := io.Pipe()
	cwd, _ := os.Getwd() // best-effort — kyu has no per-job cwd yet, this is 9sh's own process-wide cwd
	j := &Job{
		ID: m.nextID, kind: kind, state: StatePending,
		stdinR: pr, stdinW: pw,
		stdout: newGrowBuf(), stderr: newGrowBuf(), events: newGrowBuf(),
		waitCh: make(chan struct{}),
		cwd:    cwd,
		mgr:    m,
	}
	m.jobs[j.ID] = j
	j.appendEvent()
	return j
}

func (m *Manager) AllocSubprocess() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allocLocked(KindSubprocess)
}

func (m *Manager) AllocInproc(fn InprocFunc) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.allocLocked(KindInproc)
	j.fn = fn
	return j
}

func (m *Manager) Get(id int) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns every job, sorted by ID.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]int, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]*Job, len(ids))
	for i, id := range ids {
		out[i] = m.jobs[id]
	}
	return out
}
