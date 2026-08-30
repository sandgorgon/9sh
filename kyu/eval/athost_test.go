package eval

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	auth "github.com/sandgorgon/9auth"
	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
	"github.com/sandgorgon/9sh/remote"
)

// TestAtHostEndToEnd exercises the whole Phase 5 desugaring in one real
// pass: dial() over a real TLS loopback connection to a second, fully
// independent 9sh-shaped namespace (its own ns.Namespace + job.Manager,
// exactly as cmd/9sh's bootstrap wires one up), bind grafting the remote
// root at /n/testhost, and @testhost { %cmd & } creating and running a job
// on the *remote* Manager purely by re-rooting job creation — no code
// under test here has any notion of "proxy job" as a distinct mechanism.
func TestAtHostEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}

	// Two distinct 9auth identities, loaded sequentially into separate
	// config dirs — see remote_test.go's loadIdentity for why this can't
	// be done concurrently.
	serverDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", serverDir)
	serverID, err := auth.Load()
	if err != nil {
		t.Fatalf("server identity: %v", err)
	}

	clientDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", clientDir)
	clientID, err := auth.Load()
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}

	// Authorize the client on the server side.
	t.Setenv("XDG_CONFIG_HOME", serverDir)
	authPath, err := remote.AuthorizedPeersPath()
	if err != nil {
		t.Fatalf("authorized-peers path: %v", err)
	}
	if err := os.WriteFile(authPath, []byte(clientID.Fingerprint()+" write\n"), 0o600); err != nil {
		t.Fatalf("writing authorized-peers: %v", err)
	}

	// The "remote" 9sh: its own namespace and job manager, served over TLS.
	remoteNamespace := ns.New()
	if err := remoteNamespace.BindFS(job.New(job.NewManager()), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrapping remote /jobs: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := remote.Listen(ctx, "127.0.0.1:0", remoteNamespace)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	// Client side: pre-pin the server's fingerprint so dial() (run inside
	// the kyu script below, which reads os.Stdin for an unknown-peer
	// prompt) never blocks on a real prompt.
	t.Setenv("XDG_CONFIG_HOME", clientDir)
	knownPeersPath, err := auth.KnownPeersPath()
	if err != nil {
		t.Fatalf("known-peers path: %v", err)
	}
	if err := auth.RememberPeer(knownPeersPath, addr, serverID.Fingerprint()); err != nil {
		t.Fatalf("pre-pinning server fingerprint: %v", err)
	}

	env, _ := jobsEnvWithManager(t) // the client's own local namespace/job manager, for bind's target tree

	src := `h := dial("` + addr + `")
bind h, /n/testhost
j := @testhost { %sh "-c" "echo hi from remote" & }
j | wait
j`
	v := runEnv(t, src, env)
	rec, ok := v.(*value.Record)
	if !ok {
		t.Fatalf("expected a job record, got %s (%s)", v.Kind(), v.String())
	}
	statusVal, ok := rec.Get("status")
	if !ok {
		t.Fatalf("job record has no status field")
	}
	status, ok := statusVal.(*value.Record)
	if !ok {
		t.Fatalf("status is a %s, not a record", statusVal.Kind())
	}
	stateVal, _ := status.Get("state")
	if stateVal.String() != "done" {
		t.Fatalf("expected state \"done\", got %q (full status: %s)", stateVal.String(), status.String())
	}

	idVal, ok := status.Get("id")
	if !ok {
		t.Fatalf("status has no id field")
	}
	idInt, ok := idVal.(value.Int)
	if !ok {
		t.Fatalf("status.id is a %s, not an int", idVal.Kind())
	}

	// Confirm the job actually ran server-side and produced the expected
	// output — not just that the client-visible status looked right —
	// by reading its stdout directly through the bound /n/testhost tree.
	ctx2 := context.Background()
	root, err := env.Namespace().Attach(ctx2, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	stdout, err := openFile(ctx2, root, p9.OREAD, "n", "testhost", "jobs", strconv.FormatInt(int64(idInt), 10), "stdout")
	if err != nil {
		t.Fatalf("opening remote stdout: %v", err)
	}
	defer stdout.Close()
	out, err := readAllFile(ctx2, stdout)
	if err != nil {
		t.Fatalf("reading remote stdout: %v", err)
	}
	if got := string(out); got != "hi from remote\n" {
		t.Fatalf("remote stdout = %q, want %q", got, "hi from remote\n")
	}
}

func TestAtHostWithoutBindFailsClearly(t *testing.T) {
	env, _ := jobsEnvWithManager(t)
	err := runEnvErr(t, `@nosuchhost { %true & }`, env)
	if err == nil {
		t.Fatal("expected an error for an unbound host")
	}
}

// setupAtHostTestPeer builds a real remote 9sh-shaped peer (its own
// namespace + job.Manager, served over TLS — the same shape
// TestAtHostEndToEnd exercises) and a client-side Env with it already
// dialed and bound at /n/testhost, ready for @testhost{} blocks — reused
// by the proxy-recording tests below, which don't otherwise care about
// the dial/listen/TOFU machinery TestAtHostEndToEnd's own body walks
// through step by step.
func setupAtHostTestPeer(t *testing.T) *Env {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}

	serverDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", serverDir)
	serverID, err := auth.Load()
	if err != nil {
		t.Fatalf("server identity: %v", err)
	}

	clientDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", clientDir)
	clientID, err := auth.Load()
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", serverDir)
	authPath, err := remote.AuthorizedPeersPath()
	if err != nil {
		t.Fatalf("authorized-peers path: %v", err)
	}
	if err := os.WriteFile(authPath, []byte(clientID.Fingerprint()+" write\n"), 0o600); err != nil {
		t.Fatalf("writing authorized-peers: %v", err)
	}

	remoteNamespace := ns.New()
	if err := remoteNamespace.BindFS(job.New(job.NewManager()), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrapping remote /jobs: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l, err := remote.Listen(ctx, "127.0.0.1:0", remoteNamespace)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	t.Setenv("XDG_CONFIG_HOME", clientDir)
	knownPeersPath, err := auth.KnownPeersPath()
	if err != nil {
		t.Fatalf("known-peers path: %v", err)
	}
	if err := auth.RememberPeer(knownPeersPath, addr, serverID.Fingerprint()); err != nil {
		t.Fatalf("pre-pinning server fingerprint: %v", err)
	}

	env, _ := jobsEnvWithManager(t)
	conn, err := remote.Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := env.Namespace().BindFS(conn.FS(), "", "/n/testhost", ns.Replace); err != nil {
		t.Fatalf("bind /n/testhost: %v", err)
	}
	return env
}

// TestAtHostRecordsLocalProxyLinkingRecordSync exercises the foreground
// (%cmd, no &) path: runExternalViaJob already blocks on the remote
// job's wait file, so it can report the proxy recorder synchronously
// with the real terminal status in hand — see external.go's call to
// isProxyJobRoot/ProxyRecorder.
func TestAtHostRecordsLocalProxyLinkingRecordSync(t *testing.T) {
	env := setupAtHostTestPeer(t)

	type recorded struct {
		host     string
		remoteID int
		argv     []string
		exit     *int
	}
	recCh := make(chan recorded, 1)
	env.SetProxyRecorder(func(host string, remoteID int, argv []string, tsStart, tsEnd time.Time, exitCode *int, signal string) {
		recCh <- recorded{host: host, remoteID: remoteID, argv: append([]string(nil), argv...), exit: exitCode}
	})

	runEnv(t, `@testhost { %sh "-c" "echo hi" }`, env)

	select {
	case r := <-recCh:
		if r.host != "testhost" {
			t.Fatalf("host = %q, want %q", r.host, "testhost")
		}
		if r.remoteID == 0 {
			t.Fatal("remoteID = 0, want a real remote job id")
		}
		if len(r.argv) == 0 || r.argv[0] != "sh" {
			t.Fatalf("argv = %v, want it to start with \"sh\"", r.argv)
		}
		if r.exit == nil || *r.exit != 0 {
			t.Fatalf("exit = %v, want 0", r.exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy recorder was never called")
	}
}

// TestAtHostRecordsLocalProxyLinkingRecordAsync exercises the
// backgrounded (`&`) path: evalBackground never waits on the job itself,
// so the recording has to happen from recordProxyJobAsync's own
// independent goroutine once the remote job's wait file unblocks — this
// confirms that actually fires, not just the synchronous case above.
func TestAtHostRecordsLocalProxyLinkingRecordAsync(t *testing.T) {
	env := setupAtHostTestPeer(t)

	recCh := make(chan int, 1) // remote job id
	env.SetProxyRecorder(func(host string, remoteID int, argv []string, tsStart, tsEnd time.Time, exitCode *int, signal string) {
		recCh <- remoteID
	})

	src := `j := @testhost { %sh "-c" "echo hi" & }
j | wait`
	runEnv(t, src, env)

	select {
	case id := <-recCh:
		if id == 0 {
			t.Fatal("remoteID = 0, want a real remote job id")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy recorder was never called")
	}
}
