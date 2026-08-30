package session

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/job"
)

// closeJobStdin closes a job's stdin through the /jobs filesystem
// interface — the only externally-reachable way to do it: writeStdin/
// closeStdin are unexported on *job.Job, reachable only from within
// package job itself (see job/job_test.go) or, from outside it, via
// this synthetic-file path. Needed because os/exec's own internal
// stdin-forwarding goroutine blocks Wait() until it sees EOF whenever
// Cmd.Stdin isn't an *os.File — the same issue documented in
// job/job_test.go and kyu/eval/namespace.go's evalBackground.
func closeJobStdin(t *testing.T, mgr *job.Manager, id int) {
	t.Helper()
	ctx := context.Background()
	root, err := job.New(mgr).Attach(ctx, "test", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	dir, err := root.Walk(ctx, strconv.Itoa(id))
	if err != nil {
		t.Fatalf("walk job dir: %v", err)
	}
	stdin, err := dir.Walk(ctx, "stdin")
	if err != nil {
		t.Fatalf("walk stdin: %v", err)
	}
	if err := stdin.Open(ctx, p9.OWRITE); err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
}

func skipUnless9vcs(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("9vcs"); err != nil {
		t.Skip("9vcs not on PATH")
	}
}

func vcsLog(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("9vcs", "log")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("9vcs log: %v: %s", err, out)
	}
	return string(out)
}

func vcsBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("9vcs", "branch")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("9vcs branch: %v: %s", err, out)
	}
	return string(out)
}

func TestNewInitializesRepoAndHostBranch(t *testing.T) {
	skipUnless9vcs(t)
	dir := t.TempDir()
	r, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	if _, err := os.Stat(filepath.Join(dir, ".9vcs")); err != nil {
		t.Fatalf(".9vcs missing: %v", err)
	}
	if branches := vcsBranch(t, dir); !strings.Contains(branches, "* myhost") {
		t.Fatalf("branch listing = %q, want current branch myhost", branches)
	}
}

func TestNewIsIdempotentAcrossRelaunches(t *testing.T) {
	skipUnless9vcs(t)
	dir := t.TempDir()

	r1, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	r1.RecordJob(job.Status{ID: 1, Kind: job.KindSubprocess, Argv: []string{"true"}})
	if err := r1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// simulating a fresh 9sh launch against the same, now-populated dir
	r2, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("second New (relaunch) should succeed: %v", err)
	}
	defer r2.Close()

	if branches := vcsBranch(t, dir); !strings.Contains(branches, "* myhost") {
		t.Fatalf("branch listing after relaunch = %q, want current branch myhost", branches)
	}
}

func TestRecordJobWritesShardAndCheckpointCommits(t *testing.T) {
	skipUnless9vcs(t)
	dir := t.TempDir()
	r, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := time.Now()
	st := job.Status{
		ID: 42, Kind: job.KindSubprocess, State: job.StateDone,
		Argv: []string{"echo", "hi"}, Cwd: "/tmp",
		StartedAt: now, FinishedAt: now,
	}
	r.RecordJob(st)

	shard := filepath.Join(dir, historyDirName, dayShard(now))
	data, err := os.ReadFile(shard)
	if err != nil {
		t.Fatalf("reading shard: %v", err)
	}
	var rec Record
	line := strings.TrimSpace(string(data))
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("unmarshal record: %v (line: %q)", err, line)
	}
	if rec.JobID != 42 || rec.Host != "myhost" || len(rec.Argv) != 2 || rec.Argv[0] != "echo" {
		t.Fatalf("record = %+v, want job_id=42 host=myhost argv=[echo hi]", rec)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	logOutput := vcsLog(t, dir)
	if !strings.Contains(logOutput, "checkpoint ") {
		t.Fatalf("9vcs log after Close = %q, want a checkpoint patch", logOutput)
	}
}

func TestCheckpointBatchesMultipleAppendsIntoOnePatch(t *testing.T) {
	skipUnless9vcs(t)
	dir := t.TempDir()
	r, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 1; i <= 5; i++ {
		r.RecordJob(job.Status{ID: i, Kind: job.KindSubprocess, Argv: []string{"true"}})
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	logOutput := vcsLog(t, dir)
	checkpoints := strings.Count(logOutput, "checkpoint ")
	if checkpoints != 1 {
		t.Fatalf("checkpoint patches = %d, want exactly 1 (5 appends batched into one Close-triggered flush), log:\n%s", checkpoints, logOutput)
	}
}

func TestRecordJobCarriesDetachedFlag(t *testing.T) {
	skipUnless9vcs(t)
	dir := t.TempDir()
	r, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	now := time.Now()
	r.RecordJob(job.Status{
		ID: 7, Kind: job.KindSubprocess, State: job.StateDone,
		Argv: []string{"true"}, Detached: true, StartedAt: now, FinishedAt: now,
	})

	shard := filepath.Join(dir, historyDirName, dayShard(now))
	data, err := os.ReadFile(shard)
	if err != nil {
		t.Fatalf("reading shard: %v", err)
	}
	var rec Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if !rec.Detached {
		t.Fatalf("record = %+v, want detached=true", rec)
	}
}

func TestRecordProxyWritesLinkingRecord(t *testing.T) {
	skipUnless9vcs(t)
	dir := t.TempDir()
	r, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	start := time.Now()
	end := start.Add(time.Second)
	code := 0
	r.RecordProxy(ProxyJob{
		Host: "otherhost", RemoteID: 5, Argv: []string{"sh", "-c", "echo hi"},
		TSStart: start, TSEnd: end, Exit: &code,
	})

	shard := filepath.Join(dir, historyDirName, dayShard(end))
	data, err := os.ReadFile(shard)
	if err != nil {
		t.Fatalf("reading shard: %v", err)
	}
	var rec Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if rec.Kind != "proxy" || rec.Host != "myhost" || rec.RemoteHost != "otherhost" || rec.RemoteJobID != 5 {
		t.Fatalf("record = %+v, want kind=proxy host=myhost remote_host=otherhost remote_job_id=5", rec)
	}
	if rec.Exit == nil || *rec.Exit != 0 {
		t.Fatalf("record.Exit = %v, want 0", rec.Exit)
	}
}

func TestRecorderIntegratesWithRealJobManager(t *testing.T) {
	skipUnless9vcs(t)
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("true not on PATH")
	}
	dir := t.TempDir()
	r, err := New(dir, "myhost")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mgr := job.NewManager()
	r.Attach(mgr)

	j := mgr.AllocSubprocess()
	j.SetArgv([]string{"true"})
	j.Ctl("start")
	closeJobStdin(t, mgr, j.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := j.WaitFor(ctx); err != nil {
		t.Fatalf("WaitFor: %v", err)
	}

	// OnFinish runs on its own goroutine (see job.Job.notifyFinished) —
	// give it a moment to actually call RecordJob before checking.
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(filepath.Join(dir, historyDirName, dayShard(time.Now())))
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("no history line appeared after the job finished")
	}
	var rec Record
	if err := json.Unmarshal(data[:strings.IndexByte(string(data), '\n')+1], &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.JobID != j.ID || len(rec.Argv) == 0 || rec.Argv[0] != "true" {
		t.Fatalf("record = %+v, want job_id=%d argv=[true]", rec, j.ID)
	}

	r.Close()
}
