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
		Cwd: j.cwd, StartedAt: j.startedAt, FinishedAt: j.finishedAt,
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

func (j *Job) writeStdin(p []byte) (int, error) {
	j.mu.Lock()
	w := j.stdinW
	j.mu.Unlock()
	if w == nil {
		return 0, fmt.Errorf("job %d: stdin closed", j.ID)
	}
	return w.Write(p)
}

func (j *Job) closeStdin() error {
	j.mu.Lock()
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
	case "resize":
		return errors.New("ctl: resize: no pty allocated for this job (pty/tui integration is a later phase)")
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
	j.mu.Unlock()
	j.appendEvent()

	switch j.kind {
	case KindSubprocess:
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
	j.mu.Unlock()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if len(env) > 0 {
		cmd.Env = env
	}
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
// a plain `signal <name>` passes "" and leaves state alone.
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
	j.mu.Unlock()
	if proc == nil || proc.Process == nil {
		return fmt.Errorf("job %d: %s: process not started", j.ID, label)
	}
	if err := proc.Process.Signal(sig); err != nil {
		return fmt.Errorf("job %d: %s: %w", j.ID, label, err)
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
