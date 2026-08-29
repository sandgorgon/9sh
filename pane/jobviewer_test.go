package pane

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/ns"
)

// newJobViewerTestEnv binds two real job.Manager-backed /jobs trees: one
// at the ordinary local /jobs, one at /n/testhost/jobs — simulating a
// bound remote host purely at the namespace level (BindFS transparently
// creates the missing /n and /n/testhost tree nodes). The job viewer
// only cares that something job-shaped is reachable under /n/<host>/
// jobs, not how it got there (see remote's own tests for a real dial).
func newJobViewerTestEnv(t *testing.T) (*eval.Env, *job.Manager, *job.Manager) {
	t.Helper()
	namespace := ns.New()
	localMgr := job.NewManager()
	if err := namespace.BindFS(job.New(localMgr), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bind /jobs: %v", err)
	}
	remoteMgr := job.NewManager()
	if err := namespace.BindFS(job.New(remoteMgr), "", "/n/testhost/jobs", ns.Replace); err != nil {
		t.Fatalf("bind /n/testhost/jobs: %v", err)
	}
	return eval.NewGlobalEnv(namespace), localMgr, remoteMgr
}

// runRealJob starts a real subprocess job on mgr, through the same
// clone/argv/stdin/ctl/wait file protocol evalBackground and a real 9P
// client both use, and waits for it to finish, so its status file has
// real, terminal data by the time a test reads it back.
//
// This deliberately does not call job.Job's Go-level methods directly
// (AllocSubprocess/SetArgv/Ctl) — closeStdin is unexported, reachable
// only through this file protocol's stdin.Close(), and skipping it
// hangs os/exec's Wait() forever waiting for its internal stdin-
// forwarding goroutine to see EOF, exactly the gotcha job/job_test.go
// and kyu/eval's runExternalViaJob both document. Found by hitting it:
// an earlier version of this helper called the Go API directly and
// hung the entire test run.
func runRealJob(t *testing.T, mgr *job.Manager, argv ...string) {
	t.Helper()
	ctx := context.Background()
	fs := job.New(mgr)
	root, err := fs.Attach(ctx, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	clone, err := openFile(ctx, root, p9.OREAD, "clone")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	idBytes, err := readAllFile(ctx, clone)
	if err != nil {
		t.Fatalf("reading job id: %v", err)
	}
	clone.Close()
	id := strings.TrimSpace(string(idBytes))

	argvFile, err := openFile(ctx, root, p9.OWRITE, id, "argv")
	if err != nil {
		t.Fatalf("open argv: %v", err)
	}
	if _, err := argvFile.Write(ctx, 0, []byte(strings.Join(argv, "\n"))); err != nil {
		t.Fatalf("write argv: %v", err)
	}
	argvFile.Close()

	stdinFile, err := openFile(ctx, root, p9.OWRITE, id, "stdin")
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	stdinFile.Close() // closed without writing — see this func's doc comment

	ctlFile, err := openFile(ctx, root, p9.OWRITE, id, "ctl")
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	if _, err := ctlFile.Write(ctx, 0, []byte("start")); err != nil {
		t.Fatalf("ctl start: %v", err)
	}
	ctlFile.Close()

	waitFile, err := openFile(ctx, root, p9.OREAD, id, "wait")
	if err != nil {
		t.Fatalf("open wait: %v", err)
	}
	if _, err := readAllFile(ctx, waitFile); err != nil {
		t.Fatalf("wait: %v", err)
	}
	waitFile.Close()
}

func TestJobViewerListsLocalAndRemoteJobs(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no `true` on PATH")
	}
	env, localMgr, remoteMgr := newJobViewerTestEnv(t)
	runRealJob(t, localMgr, "true")
	runRealJob(t, remoteMgr, "true")

	rows, err := listAllJobs(env.Namespace())
	if err != nil {
		t.Fatalf("listAllJobs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2 entries", rows)
	}
	var sawLocal, sawRemote bool
	for _, r := range rows {
		if strings.HasPrefix(r, "local") && strings.Contains(r, "true") {
			sawLocal = true
		}
		if strings.HasPrefix(r, "testhost") && strings.Contains(r, "true") {
			sawRemote = true
		}
		if !strings.Contains(r, "done") {
			t.Fatalf("row %q: expected state done for a finished `true`", r)
		}
	}
	if !sawLocal || !sawRemote {
		t.Fatalf("rows = %v, want one local and one testhost row", rows)
	}
}

func TestJobViewerNoJobsIsNotAnError(t *testing.T) {
	env, _, _ := newJobViewerTestEnv(t)
	rows, err := listAllJobs(env.Namespace())
	if err != nil {
		t.Fatalf("listAllJobs: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %v, want none", rows)
	}
}

func TestJobViewerUpdateStoresListing(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no `true` on PATH")
	}
	env, localMgr, _ := newJobViewerTestEnv(t)
	runRealJob(t, localMgr, "true")

	m := New(env, "", JobViewerSpec("jobs", env))
	id := m.panes[0].id

	msg := listJobsCmd(id, env.Namespace())()
	next, _ := m.Update(msg)
	m = next.(Model)

	if len(m.panes[0].jobRows) != 1 {
		t.Fatalf("jobRows = %v, want 1 row", m.panes[0].jobRows)
	}
	if m.panes[0].jobErr != "" {
		t.Fatalf("jobErr = %q, want empty", m.panes[0].jobErr)
	}
}
