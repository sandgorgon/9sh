package remote

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	auth "github.com/sandgorgon/9auth"
	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/examples/memfs"
	"github.com/sandgorgon/9p/server"
)

// loadIdentity generates (and persists) a fresh 9auth identity under its
// own isolated config dir — auth.Load resolves its path from process-wide
// env, which can't safely vary per goroutine, so every identity a test
// needs is loaded sequentially, before any concurrent dial/listen starts.
func loadIdentity(t *testing.T, tag string) *auth.Identity {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, tag))
	id, err := auth.Load()
	if err != nil {
		t.Fatalf("loading %s identity: %v", tag, err)
	}
	return id
}

func TestDialListenEndToEnd(t *testing.T) {
	serverID := loadIdentity(t, "server")
	clientID := loadIdentity(t, "client")

	fs := memfs.New()
	if err := writeMemFile(fs, "greeting", []byte("hello from server\n")); err != nil {
		t.Fatalf("seeding memfs: %v", err)
	}

	authorized := auth.AuthorizedPeers{clientID.Fingerprint(): auth.PermWrite}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := listen(ctx, serverID, authorized, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeers := auth.KnownPeers{} // empty: forces the first-connect TOFU prompt path
	prompt := strings.NewReader("yes\n")
	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")

	conn, err := dial(ctx, clientID, knownPeersPath, knownPeers, addr, prompt)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if conn.Fingerprint() != serverID.Fingerprint() {
		t.Fatalf("fingerprint mismatch: got %s, want %s", conn.Fingerprint(), serverID.Fingerprint())
	}

	// The prompt-accept path should have persisted the pin to disk.
	saved, err := auth.LoadKnownPeers(knownPeersPath)
	if err != nil {
		t.Fatalf("LoadKnownPeers: %v", err)
	}
	if saved[addr] != serverID.Fingerprint() {
		t.Fatalf("known-peers not persisted after TOFU accept: got %v", saved)
	}

	root, err := conn.FS().Attach(ctx, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	f, err := root.Walk(ctx, "greeting")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := readAll(ctx, f)
	f.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello from server\n" {
		t.Fatalf("got %q", got)
	}

	// Round-trip a write through the remote-FS adapter's Create/Write path.
	root2, err := conn.FS().Attach(ctx, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	created, err := root2.Create(ctx, "new-file", 0644, p9.OWRITE)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := created.Write(ctx, 0, []byte("written remotely")); err != nil {
		t.Fatalf("write: %v", err)
	}
	created.Close()

	verify, err := readMemFile(fs, "new-file")
	if err != nil {
		t.Fatalf("verifying write landed server-side: %v", err)
	}
	if string(verify) != "written remotely" {
		t.Fatalf("server-side content = %q", verify)
	}
}

func TestListenRejectsUnauthorizedPeer(t *testing.T) {
	serverID := loadIdentity(t, "server")
	clientID := loadIdentity(t, "client")

	fs := memfs.New()

	// clientID's fingerprint is deliberately absent from authorized.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := listen(ctx, serverID, auth.AuthorizedPeers{}, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	_, err = dial(ctx, clientID, knownPeersPath, auth.KnownPeers{}, addr, strings.NewReader("yes\n"))
	if err == nil {
		t.Fatal("expected dial to an unauthorized-peer listener to fail")
	}
}

func TestDialRefusesChangedFingerprint(t *testing.T) {
	serverID := loadIdentity(t, "server")
	clientID := loadIdentity(t, "client")

	fs := memfs.New()
	authorized := auth.AuthorizedPeers{clientID.Fingerprint(): auth.PermRead}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := listen(ctx, serverID, authorized, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	// known-peers already pins addr to a fingerprint that isn't serverID's
	// real one — simulating a changed/impersonated peer.
	known := auth.KnownPeers{addr: "0000000000000000000000000000000000000000000000000000000000000000"}
	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	_, err = dial(ctx, clientID, knownPeersPath, known, addr, strings.NewReader("yes\n"))
	if err == nil {
		t.Fatal("expected dial to refuse a changed fingerprint")
	}
	if !strings.Contains(err.Error(), "FINGERPRINT HAS CHANGED") {
		t.Fatalf("expected a fingerprint-changed error, got: %v", err)
	}
}

func TestAuthFSDeniesWriteWithoutPermission(t *testing.T) {
	serverID := loadIdentity(t, "server")
	clientID := loadIdentity(t, "client")

	fs := memfs.New()
	// Read-only permission: attach succeeds, but any mutation must fail.
	authorized := auth.AuthorizedPeers{clientID.Fingerprint(): auth.PermRead}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := listen(ctx, serverID, authorized, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	conn, err := dial(ctx, clientID, knownPeersPath, auth.KnownPeers{}, addr, strings.NewReader("yes\n"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	root, err := conn.FS().Attach(ctx, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := root.Create(ctx, "nope", 0644, p9.OWRITE); err == nil {
		t.Fatal("expected Create to be denied for a read-only peer")
	}
}

func readAll(ctx context.Context, f server.File) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	var offset int64
	for {
		n, err := f.Read(ctx, offset, tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			offset += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return buf.Bytes(), err
		}
		if n == 0 {
			break
		}
	}
	return buf.Bytes(), nil
}

func writeMemFile(fs server.FileSystem, name string, content []byte) error {
	ctx := context.Background()
	root, err := fs.Attach(ctx, "test", "")
	if err != nil {
		return err
	}
	f, err := root.Create(ctx, name, 0644, p9.OWRITE)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(ctx, 0, content)
	return err
}

func readMemFile(fs server.FileSystem, name string) ([]byte, error) {
	ctx := context.Background()
	root, err := fs.Attach(ctx, "test", "")
	if err != nil {
		return nil, err
	}
	f, err := root.Walk(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		return nil, err
	}
	defer f.Close()
	return readAll(ctx, f)
}
