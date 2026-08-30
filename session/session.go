// Package session implements 9sh's per-job history: a day-sharded NRL
// (Native Record Log — placeholder JSON-lines, same deferral as the rest
// of the codebase; see kyu/job's own doc comments) repo at
// ~/.config/9/session, checkpointed into 9vcs.
//
// A Recorder is meant to attach directly to a job.Manager's OnFinish
// hook: every job that reaches a terminal state — whether backgrounded
// via kyu's `&` or a synchronous foreground %cmd routed through /jobs
// (see kyu/eval/external.go) — gets a history line for free, with no
// separate logging path, exactly as the design doc calls for.
//
// A job run via `@host{}` is the one exception RecordJob can't cover:
// it's created and tracked entirely on the remote peer's own
// job.Manager, not this Recorder's, so it never reaches this
// Recorder's OnFinish hook at all — the remote peer's own Recorder (if
// it has one) logs an ordinary entry for it on its own side instead.
// RecordProxy is the separate, explicitly-called path
// (kyu/eval.ProxyRecorderFunc, wired in cmd/9sh's bootstrap) that
// appends the missing local-side "I ran X on host Y" linking record.
//
// 9vcs itself is a CLI-only tool (its repo/patch-graph logic lives in
// cmd/9vcs's own package main, not an importable library — confirmed
// by reading the source before writing this package), so checkpointing
// shells out to the `9vcs` binary rather than calling into it as a Go
// API, the same integration shape kyu's own %cmd already uses for
// arbitrary external tools.
package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sandgorgon/9sh/job"
)

// Record is one completed job's history entry — the design doc's
// per-job schema. No stdout/stderr capture by default (an opt-in for
// later, content-addressed via checkout, not v1).
//
// RemoteHost/RemoteJobID are set only on a "proxy" record (Kind ==
// "proxy") — see ProxyJob/RecordProxy — the local-side "I ran X on host
// Y" linking record for a job started via @host{}. Host stays this
// Recorder's own host (which branch the row lives on), matching every
// other record; RemoteHost names where the job actually ran, and
// RemoteJobID is the job's own ID in *that* peer's numbering, letting a
// reader cross-reference the remote's own history for anything this
// linking record doesn't carry (stdout, e.g.) with RemoteHost's own
// session repo.
type Record struct {
	TSStart     time.Time `json:"ts_start"`
	TSEnd       time.Time `json:"ts_end"`
	Host        string    `json:"host"`
	JobID       int       `json:"job_id"`
	Cwd         string    `json:"cwd,omitempty"`
	Argv        []string  `json:"argv,omitempty"`
	Exit        *int      `json:"exit,omitempty"`
	Signal      string    `json:"signal,omitempty"`
	Kind        string    `json:"kind"`
	Detached    bool      `json:"detached,omitempty"`
	RemoteHost  string    `json:"remote_host,omitempty"`
	RemoteJobID int       `json:"remote_job_id,omitempty"`
}

// Checkpoint policy (design doc: "idle ~30s, shell exit, or ~5min
// periodic"). pollInterval is this package's own implementation detail
// (how often the background loop wakes to check whether either
// threshold has been crossed), not part of the design doc's policy
// itself.
const (
	idleThreshold  = 30 * time.Second
	maxThreshold   = 5 * time.Minute
	pollInterval   = 10 * time.Second
	historyDirName = "history"
	nrlDateLayout  = "2006-01-02"
)

// Recorder appends a history line for every job it's told about
// (RecordJob, wired to job.Manager.OnFinish — see Attach) and
// checkpoints the accumulated lines into 9vcs on the policy above.
type Recorder struct {
	dir  string
	host string

	mu             sync.Mutex
	dirty          bool
	lastAppend     time.Time
	lastCheckpoint time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// New sets up (or reopens) the session repo at dir — typically
// ~/.config/9/session — on a branch named after host, and starts the
// background checkpoint loop. It requires the `9vcs` binary on PATH;
// callers should treat a non-nil error as "session history isn't
// available this run" and continue without it (see cmd/9sh's
// bootstrap), not as fatal to starting the shell at all.
func New(dir, host string) (*Recorder, error) {
	if _, err := exec.LookPath("9vcs"); err != nil {
		return nil, fmt.Errorf("session: %w (9vcs not found on PATH)", err)
	}
	if err := ensureRepo(dir); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if err := ensureHostBranch(dir, host); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	r := &Recorder{
		dir: dir, host: host,
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	go r.checkpointLoop()
	return r, nil
}

// Attach registers r.RecordJob as mgr's OnFinish hook.
func (r *Recorder) Attach(mgr *job.Manager) {
	mgr.OnFinish(r.RecordJob)
}

// RecordJob appends st as a history line. It matches job.Manager's
// OnFinish signature directly, so Attach can pass it as the callback
// with no adapter.
//
// A detached job (st.Detached — set by a `ctl detach` write) still gets
// recorded here exactly like any other: OnFinish fires on every terminal
// transition regardless of the flag (see job.Job.finish), and detach
// only ever meant "don't tie this job's lifetime to whatever's watching
// it," not "stop tracking it in history" — the same distinction `nohup`/
// `disown` draw in a POSIX shell. Detached is carried through onto the
// record so a reader can tell the two cases apart after the fact.
func (r *Recorder) RecordJob(st job.Status) {
	rec := Record{
		TSStart: st.StartedAt, TSEnd: st.FinishedAt, Host: r.host, JobID: st.ID,
		Cwd: st.Cwd, Argv: st.Argv, Exit: st.ExitCode, Signal: st.Signal, Kind: string(st.Kind),
		Detached: st.Detached,
	}
	if err := r.append(rec); err != nil {
		fmt.Fprintln(os.Stderr, "9sh: session: appending history:", err)
	}
}

// ProxyJob describes a job run via `@host{}` for RecordProxy — the local
// side of the design doc's "proxy jobs" (see kyu/eval/athost.go): the
// job itself is created and tracked entirely on the remote peer's own
// job.Manager, so it already gets an ordinary Phase-4 history line for
// free in *that peer's own* session repo (nothing special needed there,
// per the design doc). What's genuinely missing, and what this
// captures, is a record on the *local* side saying "I ran X on host Y".
type ProxyJob struct {
	Host     string // the remote peer's own hostname/address, e.g. from @host{}'s literal host
	RemoteID int    // the job's ID in the remote peer's own numbering
	Argv     []string
	TSStart  time.Time
	TSEnd    time.Time
	Exit     *int
	Signal   string
}

// RecordProxy appends pj as a "proxy"-kind history line. kyu/eval wires
// this in as an eval.ProxyRecorderFunc callback (see cmd/9sh's
// bootstrap) rather than eval importing package session directly — the
// same shape job.Manager.OnFinish's callback already uses for the same
// reason (job doesn't import session either).
func (r *Recorder) RecordProxy(pj ProxyJob) {
	rec := Record{
		TSStart: pj.TSStart, TSEnd: pj.TSEnd, Host: r.host,
		RemoteHost: pj.Host, RemoteJobID: pj.RemoteID,
		Argv: pj.Argv, Exit: pj.Exit, Signal: pj.Signal, Kind: "proxy",
	}
	if err := r.append(rec); err != nil {
		fmt.Fprintln(os.Stderr, "9sh: session: appending proxy history:", err)
	}
}

func (r *Recorder) append(rec Record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := filepath.Join(r.dir, historyDirName, dayShard(rec.TSEnd))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return err
	}

	r.mu.Lock()
	r.lastAppend = time.Now()
	r.dirty = true
	r.mu.Unlock()
	return nil
}

func dayShard(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Format(nrlDateLayout) + ".nrl"
}

func (r *Recorder) checkpointLoop() {
	defer close(r.doneCh)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.maybeCheckpoint()
		case <-r.stopCh:
			r.checkpoint() // "shell exit" is one of the three checkpoint triggers
			return
		}
	}
}

func (r *Recorder) maybeCheckpoint() {
	r.mu.Lock()
	due := r.dirty && (time.Since(r.lastAppend) >= idleThreshold || time.Since(r.lastCheckpoint) >= maxThreshold)
	r.mu.Unlock()
	if due {
		r.checkpoint()
	}
}

// checkpoint runs `9vcs record`, batching every append since the last
// checkpoint into one patch (design doc: avoids one-patch-per-command
// noise). A failed checkpoint leaves dirty set so the next tick (or the
// final Close flush) retries — nothing here needs to succeed
// immediately, only eventually.
func (r *Recorder) checkpoint() {
	r.mu.Lock()
	dirty := r.dirty
	r.mu.Unlock()
	if !dirty {
		return
	}

	msg := "checkpoint " + time.Now().UTC().Format(time.RFC3339)
	if err := runVCS(r.dir, "record", "-m", msg); err != nil {
		fmt.Fprintln(os.Stderr, "9sh: session: checkpoint failed:", err)
		return
	}

	r.mu.Lock()
	r.dirty = false
	r.lastCheckpoint = time.Now()
	r.mu.Unlock()
}

// Close stops the background checkpoint loop and performs one final
// flush. Call it on shell shutdown, from every entry point (script,
// line REPL, tui) — see cmd/9sh's bootstrap.
func (r *Recorder) Close() error {
	r.stopOnce.Do(func() { close(r.stopCh) })
	<-r.doneCh
	return nil
}

// ---- 9vcs process plumbing ----

func ensureRepo(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".9vcs")); err == nil {
		return nil // already initialized
	}
	return runVCS(dir, "init")
}

// ensureHostBranch switches to (creating first, if needed) the branch
// named host. It checks the current branch via `9vcs branch` first and
// does nothing if already there — deliberately avoiding an unconditional
// `9vcs checkout host` on every launch, since checkout refuses to run
// with uncommitted changes and this repo's working tree is expected to
// have an uncommitted tail most of the time (appends happen directly,
// independent of the checkpoint cycle — see append/checkpoint above).
func ensureHostBranch(dir, host string) error {
	out, err := vcsOutput(dir, "branch")
	if err != nil {
		return fmt.Errorf("listing branches: %w", err)
	}
	exists := false
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		if line == "* "+host {
			return nil // already on it
		}
		if strings.TrimPrefix(strings.TrimPrefix(line, "* "), "  ") == host {
			exists = true
		}
	}
	if !exists {
		if err := runVCS(dir, "checkout", "-b", host); err != nil {
			return fmt.Errorf("creating host branch %q: %w", host, err)
		}
		return nil
	}
	if err := runVCS(dir, "checkout", host); err != nil {
		return fmt.Errorf("switching to host branch %q: %w", host, err)
	}
	return nil
}

func runVCS(dir string, args ...string) error {
	_, err := vcsOutput(dir, args...)
	return err
}

func vcsOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("9vcs", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("9vcs %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}
