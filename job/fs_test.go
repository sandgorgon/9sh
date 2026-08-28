package job

import (
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/client"
	"github.com/sandgorgon/9p/server"
)

// dialJobFS starts a real 9P server over a Unix-domain socket — the
// transport the design doc specifies for purely local synthetic
// namespaces like /jobs — backed by a fresh Manager, and returns a
// connected client.
func dialJobFS(t *testing.T) (*client.Client, *Manager) {
	t.Helper()
	mgr := NewManager()
	sockPath := filepath.Join(t.TempDir(), "jobs.sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	srv := &server.Server{FS: New(mgr)}
	go srv.Serve(l)

	c, err := client.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if _, err := c.Attach("glenda", ""); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return c, mgr
}

// cloneJob drives /clone -> read id -> write argv, returning the job's id
// and its base path ("/<id>").
func cloneJob(t *testing.T, c *client.Client, argv []string) (int, string) {
	t.Helper()
	cf, err := c.Open("/clone", p9.OREAD)
	if err != nil {
		t.Fatalf("open /clone: %v", err)
	}
	idBytes, err := io.ReadAll(cf)
	if err != nil {
		t.Fatalf("read /clone: %v", err)
	}
	cf.Close()
	id, err := strconv.Atoi(strings.TrimSpace(string(idBytes)))
	if err != nil {
		t.Fatalf("parse job id from %q: %v", idBytes, err)
	}
	base := "/" + strconv.Itoa(id)

	if len(argv) > 0 {
		af, err := c.Open(base+"/argv", p9.OWRITE)
		if err != nil {
			t.Fatalf("open argv: %v", err)
		}
		if _, err := af.Write([]byte(strings.Join(argv, "\n"))); err != nil {
			t.Fatalf("write argv: %v", err)
		}
		af.Close()
	}
	return id, base
}

func writeCtl(t *testing.T, c *client.Client, base, cmd string) error {
	t.Helper()
	f, err := c.Open(base+"/ctl", p9.OWRITE)
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	defer f.Close()
	_, err = f.Write([]byte(cmd))
	return err
}

func TestFS_EndToEndSubprocess(t *testing.T) {
	c, _ := dialJobFS(t)
	id, base := cloneJob(t, c, []string{"echo", "hello over 9P"})
	if id != 1 {
		t.Fatalf("first cloned job id = %d, want 1", id)
	}

	// close stdin before waiting — see job_test.go's note on why an
	// unclosed non-file Stdin blocks os/exec's Wait() forever.
	sf, err := c.Open(base+"/stdin", p9.OWRITE)
	if err != nil {
		t.Fatalf("open stdin: %v", err)
	}
	sf.Close()

	if err := writeCtl(t, c, base, "start"); err != nil {
		t.Fatalf("ctl start: %v", err)
	}

	wf, err := c.Open(base+"/wait", p9.OREAD)
	if err != nil {
		t.Fatalf("open wait: %v", err)
	}
	waitBytes, err := io.ReadAll(wf)
	if err != nil {
		t.Fatalf("read wait: %v", err)
	}
	if !strings.Contains(string(waitBytes), `"state":"done"`) {
		t.Fatalf("wait output = %q, want state done", waitBytes)
	}

	of, err := c.Open(base+"/stdout", p9.OREAD)
	if err != nil {
		t.Fatalf("open stdout: %v", err)
	}
	out, err := io.ReadAll(of)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(out) != "hello over 9P\n" {
		t.Fatalf("stdout = %q, want %q", out, "hello over 9P\n")
	}
}

func TestFS_StatusIsLiveAcrossReads(t *testing.T) {
	c, _ := dialJobFS(t)
	_, base := cloneJob(t, c, nil)

	statusOf := func() string {
		f, err := c.Open(base+"/status", p9.OREAD)
		if err != nil {
			t.Fatalf("open status: %v", err)
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		return string(b)
	}

	if s := statusOf(); !strings.Contains(s, `"state":"pending"`) {
		t.Fatalf("status = %q, want pending", s)
	}

	af, _ := c.Open(base+"/argv", p9.OWRITE)
	af.Write([]byte("true"))
	af.Close()
	writeCtl(t, c, base, "start")

	sf, _ := c.Open(base+"/stdin", p9.OWRITE)
	sf.Close()

	wf, _ := c.Open(base+"/wait", p9.OREAD)
	io.ReadAll(wf)

	if s := statusOf(); !strings.Contains(s, `"state":"done"`) {
		t.Fatalf("status after completion = %q, want done", s)
	}
}

func TestFS_CtlRejectsMalformedCommand(t *testing.T) {
	c, _ := dialJobFS(t)
	_, base := cloneJob(t, c, []string{"true"})
	if err := writeCtl(t, c, base, "not-a-real-command"); err == nil {
		t.Fatal("malformed ctl command should return a 9P error, not succeed silently")
	}
}

func TestFS_RootDirectoryListsCloneAndJobs(t *testing.T) {
	c, _ := dialJobFS(t)
	cloneJob(t, c, []string{"true"})
	cloneJob(t, c, []string{"true"})

	root, err := c.Open("/", p9.OREAD)
	if err != nil {
		t.Fatalf("open /: %v", err)
	}
	entries, err := root.ReadDir()
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"clone", "1", "2"} {
		if !names[want] {
			t.Errorf("root listing %v missing %q", names, want)
		}
	}
}

func TestFS_EventsTailAcrossStateTransitions(t *testing.T) {
	c, _ := dialJobFS(t)
	_, base := cloneJob(t, c, []string{"true"})

	ef, err := c.Open(base+"/events", p9.OREAD)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer ef.Close()

	// read the "pending" event recorded at clone time
	buf := make([]byte, 4096)
	n, err := ef.Read(buf)
	if err != nil {
		t.Fatalf("read initial event: %v", err)
	}
	if !strings.Contains(string(buf[:n]), `"state":"pending"`) {
		t.Fatalf("first event = %q, want pending", buf[:n])
	}

	// start the job concurrently and confirm the tail sees the rest of
	// the transitions without needing to reopen the file
	done := make(chan struct{})
	go func() {
		defer close(done)
		sf, _ := c.Open(base+"/stdin", p9.OWRITE)
		sf.Close()
		writeCtl(t, c, base, "start")
	}()

	var tail strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(tail.String(), `"state":"done"`) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out tailing events; got so far: %s", tail.String())
		}
		n, err := ef.Read(buf)
		if err != nil && err != io.EOF {
			t.Fatalf("tail read: %v", err)
		}
		tail.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	<-done
	if !strings.Contains(tail.String(), `"state":"running"`) {
		t.Errorf("event tail = %q, missing a running transition", tail.String())
	}
}
