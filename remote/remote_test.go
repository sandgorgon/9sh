package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
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
	l, err := listen(ctx, serverID, authorized, nil, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeers := auth.KnownPeers{} // empty: forces the first-connect TOFU prompt path
	prompt := strings.NewReader("yes\n")
	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")

	conn, err := dialTCP(ctx, clientID, knownPeersPath, knownPeers, addr, prompt)
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
	l, err := listen(ctx, serverID, auth.AuthorizedPeers{}, nil, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	_, err = dialTCP(ctx, clientID, knownPeersPath, auth.KnownPeers{}, addr, strings.NewReader("yes\n"))
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
	l, err := listen(ctx, serverID, authorized, nil, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	// known-peers already pins addr to a fingerprint that isn't serverID's
	// real one — simulating a changed/impersonated peer.
	known := auth.KnownPeers{addr: "0000000000000000000000000000000000000000000000000000000000000000"}
	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	_, err = dialTCP(ctx, clientID, knownPeersPath, known, addr, strings.NewReader("yes\n"))
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
	l, err := listen(ctx, serverID, authorized, nil, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	conn, err := dialTCP(ctx, clientID, knownPeersPath, auth.KnownPeers{}, addr, strings.NewReader("yes\n"))
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

// TestAuthFSProposePermitsWriteButNotDestructive checks the concrete
// Propose-vs-Write mapping authFile's doc comment settles: PermPropose is
// enough for Write/Create, but Remove and WStat still need PermWrite.
func TestAuthFSProposePermitsWriteButNotDestructive(t *testing.T) {
	serverID := loadIdentity(t, "server")
	clientID := loadIdentity(t, "client")

	fs := memfs.New()
	if err := writeMemFile(fs, "existing", []byte("content")); err != nil {
		t.Fatalf("seeding memfs: %v", err)
	}
	authorized := auth.AuthorizedPeers{clientID.Fingerprint(): auth.PermPropose}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := listen(ctx, serverID, authorized, nil, "127.0.0.1:0", fs)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()

	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	conn, err := dialTCP(ctx, clientID, knownPeersPath, auth.KnownPeers{}, addr, strings.NewReader("yes\n"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	root, err := conn.FS().Attach(ctx, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	created, err := root.Create(ctx, "new-file", 0644, p9.OWRITE)
	if err != nil {
		t.Fatalf("expected Create to be permitted for a propose-level peer: %v", err)
	}
	if _, err := created.Write(ctx, 0, []byte("hi")); err != nil {
		t.Fatalf("expected Write to be permitted for a propose-level peer: %v", err)
	}
	created.Close()

	root2, err := conn.FS().Attach(ctx, "9sh", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	existing, err := root2.Walk(ctx, "existing")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if err := existing.Remove(ctx); err == nil {
		t.Fatal("expected Remove to be denied for a propose-level peer")
	}
}

// TestListenWithRootPermsScopesPerRoot checks that a root-specific ACL
// governs its whole subtree (a peer with global read-only access can get
// elevated permission throughout one particular exported root, nested
// content included) without spilling over to a different, non-overridden
// root — even one containing a same-named subdirectory, which only ever
// matters if the override check ran again at that depth.
func TestListenWithRootPermsScopesPerRoot(t *testing.T) {
	// Unlike the other tests in this file, ListenWithRootPerms is the
	// real exported entry point (not the explicit-id/authorized `listen`
	// test helper), so it resolves its own identity and global
	// authorized-peers file from env-derived paths (auth.Load/
	// AuthorizedPeersPath) exactly as a real caller would — XDG_CONFIG_HOME
	// has to be pointed at the server's own config dir for that call
	// specifically, the same constraint loadIdentity's own doc comment
	// flags for auth.Load in general.
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

	fs := memfs.New()
	// /plain/elevated: a same-named subdirectory nested inside a
	// *different*, non-overridden root — proves the override check only
	// ever fires exactly at the top level, not by name match at any
	// depth. /elevated/nested: proves an override, once it does apply,
	// correctly governs everything beneath that root, not just the
	// top-level directory entry itself.
	root, err := fs.Attach(context.Background(), "test", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	plainDir, err := root.Create(context.Background(), "plain", p9.DMDIR|0755, p9.ORDWR)
	if err != nil {
		t.Fatalf("creating plain dir: %v", err)
	}
	if _, err := plainDir.Create(context.Background(), "elevated", p9.DMDIR|0755, p9.ORDWR); err != nil {
		t.Fatalf("creating plain/elevated dir: %v", err)
	}
	elevatedDir, err := root.Create(context.Background(), "elevated", p9.DMDIR|0755, p9.ORDWR)
	if err != nil {
		t.Fatalf("creating elevated dir: %v", err)
	}
	if _, err := elevatedDir.Create(context.Background(), "nested", p9.DMDIR|0755, p9.ORDWR); err != nil {
		t.Fatalf("creating elevated/nested dir: %v", err)
	}

	// Global ACL: read-only. Root override for "elevated": write.
	t.Setenv("XDG_CONFIG_HOME", serverDir)
	globalPath, err := AuthorizedPeersPath()
	if err != nil {
		t.Fatalf("AuthorizedPeersPath: %v", err)
	}
	if err := os.WriteFile(globalPath, []byte(clientID.Fingerprint()+" read\n"), 0o600); err != nil {
		t.Fatalf("writing global authorized-peers: %v", err)
	}
	rootPath := filepath.Join(t.TempDir(), "elevated-authorized-peers")
	if err := os.WriteFile(rootPath, []byte(clientID.Fingerprint()+" write\n"), 0o600); err != nil {
		t.Fatalf("writing root authorized-peers: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, err := ListenWithRootPerms(ctx, "127.0.0.1:0", fs, map[string]string{"elevated": rootPath})
	if err != nil {
		t.Fatalf("ListenWithRootPerms: %v", err)
	}
	addr := l.Addr().String()

	knownPeersPath := filepath.Join(t.TempDir(), "known-peers")
	conn, err := dialTCP(ctx, clientID, knownPeersPath, auth.KnownPeers{}, addr, strings.NewReader("yes\n"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if conn.Fingerprint() != serverID.Fingerprint() {
		t.Fatalf("fingerprint mismatch: got %s, want %s", conn.Fingerprint(), serverID.Fingerprint())
	}

	attach := func() server.File {
		t.Helper()
		f, err := conn.FS().Attach(ctx, "9sh", "")
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		return f
	}

	// The plain root has no override: global read-only applies, so a
	// write must fail.
	plain, err := attach().Walk(ctx, "plain")
	if err != nil {
		t.Fatalf("walk plain: %v", err)
	}
	if _, err := plain.Create(ctx, "nope", 0644, p9.OWRITE); err == nil {
		t.Fatal("expected Create under the un-overridden root to be denied (global read-only)")
	}

	// A same-named "elevated" subdirectory nested inside "plain" must
	// not spuriously pick up the override just by matching its name —
	// only exactly-first-segment walks from the attach root ever
	// consult rootPerms at all.
	plainElevated, err := plain.Walk(ctx, "elevated")
	if err != nil {
		t.Fatalf("walk plain/elevated: %v", err)
	}
	if _, err := plainElevated.Create(ctx, "nope", 0644, p9.OWRITE); err == nil {
		t.Fatal("expected Create under plain/elevated to be denied (nested, not the overridden root itself)")
	}

	// The overridden root grants write, at the root itself...
	elevatedRoot, err := attach().Walk(ctx, "elevated")
	if err != nil {
		t.Fatalf("walk elevated: %v", err)
	}
	if _, err := elevatedRoot.Create(ctx, "ok", 0644, p9.OWRITE); err != nil {
		t.Fatalf("expected Create under the overridden root to be permitted: %v", err)
	}

	// ...and that grant correctly propagates to content nested beneath
	// it — a root override scopes the whole subtree, not just the one
	// top-level directory entry.
	nested, err := attach().Walk(ctx, "elevated")
	if err != nil {
		t.Fatalf("walk elevated (2nd): %v", err)
	}
	nested, err = nested.Walk(ctx, "nested")
	if err != nil {
		t.Fatalf("walk elevated/nested: %v", err)
	}
	if _, err := nested.Create(ctx, "ok", 0644, p9.OWRITE); err != nil {
		t.Fatalf("expected Create under elevated/nested to be permitted (inherits the root override): %v", err)
	}
}

// TestDialUnixEndToEnd exercises the whole path issue #9sh#2 asked for:
// a plain, unauthenticated 9P server on a Unix socket (exactly what a
// program like 9ed serves) reached through Dial with no TLS handshake and
// no 9auth identity involved at all — the socket's own permissions are
// the only trust boundary, same category as dirfs's local-directory bind.
func TestDialUnixEndToEnd(t *testing.T) {
	fs := memfs.New()
	if err := writeMemFile(fs, "greeting", []byte("hello from unix socket\n")); err != nil {
		t.Fatalf("seeding memfs: %v", err)
	}

	sockPath := filepath.Join(t.TempDir(), "9p.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	srv := &server.Server{FS: fs}
	go srv.Serve(l)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := Dial(ctx, sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if fp := conn.Fingerprint(); fp != "" {
		t.Fatalf("expected no fingerprint for an unauthenticated unix-socket dial, got %q", fp)
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
	if string(got) != "hello from unix socket\n" {
		t.Fatalf("got %q", got)
	}
}

// TestDialUnixSchemePrefix checks the "unix:"-prefixed form works
// identically to a bare absolute path — both should reach dialUnix, never
// dialTCP (which would otherwise try to resolve "unix:/..." as a
// host:port and fail confusingly).
func TestDialUnixSchemePrefix(t *testing.T) {
	fs := memfs.New()
	sockPath := filepath.Join(t.TempDir(), "9p.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	srv := &server.Server{FS: fs}
	go srv.Serve(l)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := Dial(ctx, "unix:"+sockPath)
	if err != nil {
		t.Fatalf("Dial with unix: prefix: %v", err)
	}
	conn.Close()
}

func TestUnixSocketPath(t *testing.T) {
	cases := []struct {
		addr     string
		wantPath string
		wantOK   bool
	}{
		{"127.0.0.1:1234", "", false},
		{"host.example.com:9999", "", false},
		{"/run/user/1000/9ed/12345.sock", "/run/user/1000/9ed/12345.sock", true},
		{"unix:/run/user/1000/9ed/12345.sock", "/run/user/1000/9ed/12345.sock", true},
	}
	for _, c := range cases {
		path, ok := unixSocketPath(c.addr)
		if ok != c.wantOK || path != c.wantPath {
			t.Errorf("unixSocketPath(%q) = (%q, %v), want (%q, %v)", c.addr, path, ok, c.wantPath, c.wantOK)
		}
	}
}

// TestListenUnixEndToEnd exercises #3's server side: fs served over
// ListenUnix, reached back through Dial (which already covers the client
// half in TestDialUnixEndToEnd) — no TLS, no 9auth, socket permissions
// plus a same-UID SO_PEERCRED check as the only gate.
func TestListenUnixEndToEnd(t *testing.T) {
	fs := memfs.New()
	if err := writeMemFile(fs, "greeting", []byte("hello from ListenUnix\n")); err != nil {
		t.Fatalf("seeding memfs: %v", err)
	}

	sockPath := filepath.Join(t.TempDir(), "9sh.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l, err := ListenUnix(ctx, sockPath, fs)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer l.Close()

	if st, err := os.Stat(sockPath); err != nil {
		t.Fatalf("stat socket: %v", err)
	} else if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket permissions = %o, want 0600", perm)
	}

	conn, err := Dial(ctx, sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

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
	if string(got) != "hello from ListenUnix\n" {
		t.Fatalf("got %q", got)
	}
}

// TestPeerUIDMatchesSelf sanity-checks the SO_PEERCRED plumbing directly:
// a same-process loopback connection over a Unix socket should report the
// running process's own UID. (A genuine cross-UID rejection can't be
// exercised in-process — it needs a second real user — so this is the
// closest unit-level check available; TestListenUnixEndToEnd covers the
// same-UID accept path end to end.)
func TestPeerUIDMatchesSelf(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "peercred.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	var serverUID uint32
	var serverErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			serverErr = err
			return
		}
		defer c.Close()
		uc, ok := c.(*net.UnixConn)
		if !ok {
			serverErr = fmt.Errorf("accepted conn is %T, not *net.UnixConn", c)
			return
		}
		serverUID, serverErr = peerUID(uc)
	}()

	c, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()
	<-done

	if serverErr != nil {
		t.Fatalf("peerUID: %v", serverErr)
	}
	if want := uint32(os.Getuid()); serverUID != want {
		t.Fatalf("peerUID = %d, want %d (os.Getuid())", serverUID, want)
	}
}

// TestListenUnixRecoversStaleSocket checks removeStaleSocket's core
// promise: a leftover socket file from an earlier, uncleanly-exited
// ListenUnix (no live listener behind it) doesn't block rebinding the
// same path.
func TestListenUnixRecoversStaleSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "stale.sock")

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l.Close() // closes the listener but leaves sockPath's file on disk

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l2, err := ListenUnix(ctx, sockPath, memfs.New())
	if err != nil {
		t.Fatalf("ListenUnix over a stale socket: %v", err)
	}
	l2.Close()
}

// TestListenUnixRefusesWhenAlreadyListening checks the other half of
// removeStaleSocket's dial-first check: a socket with a live listener
// behind it must never be silently unlinked and stolen.
func TestListenUnixRefusesWhenAlreadyListening(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "live.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l1, err := ListenUnix(ctx, sockPath, memfs.New())
	if err != nil {
		t.Fatalf("first ListenUnix: %v", err)
	}
	defer l1.Close()

	if _, err := ListenUnix(ctx, sockPath, memfs.New()); err == nil {
		t.Fatal("expected a second ListenUnix on the same live path to fail")
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
