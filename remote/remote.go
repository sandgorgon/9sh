// Package remote implements 9sh's two transports for reaching a namespace
// that isn't your own, split by whether the other end is on this machine:
//
// Cross-machine (Dial on a host:port / Listen) is a mutual-TLS bridge
// between 9auth's standing per-install identity and p9's client/server:
// authenticate once at the TLS layer, and Tattach stays a formality —
// exactly the design doc's identity/auth bridge. Dial's trust decision
// (known-peers, TOFU first-connect prompt, loud refusal on a changed
// fingerprint) mirrors 9vcs's cmd/9vcs/sync.go dialPeer byte-for-byte in
// spirit, so one trust model covers VCS sync, namespace mounts, and proxy
// jobs alike, as designed. A verified peer's fingerprint threads through
// context.Context via server.Server's ConnContext hook into every request
// the connection ever makes, including Attach — "uname in Tattach stops
// being a client-asserted string; the server derives identity from the
// TLS session instead."
//
// Same-machine (Dial on a Unix-socket path / ListenUnix) skips all of
// that: a Unix socket's own file permissions are already the trust
// boundary, the same category a local-directory bind sits in. ListenUnix
// reinforces it (an explicit chmod 0600 plus an SO_PEERCRED check per
// connection) rather than relying on filesystem permissions alone — see
// its own doc comment for the reasoning, including why access is
// deliberately *not* scoped down to local-only namespace content.
package remote

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	auth "github.com/sandgorgon/9auth"
	"github.com/sandgorgon/9p/client"
	"github.com/sandgorgon/9p/server"
)

type peerFPKey struct{}

// PeerFingerprint returns the fingerprint of the peer that opened the
// connection ctx belongs to, if this ctx descends from a Listen-served
// connection's ConnContext. Not set for purely local/in-process namespace
// use — see authFS's doc comment.
func PeerFingerprint(ctx context.Context) (string, bool) {
	fp, ok := ctx.Value(peerFPKey{}).(string)
	return fp, ok
}

// Conn is an authenticated connection to a remote 9sh/9P peer.
type Conn struct {
	client *client.Client
	fs     server.FileSystem
	fp     string
}

// FS is what bind grafts into the local namespace (see
// ns.Namespace.BindFS) — the "Native tier" reached over the wire.
func (c *Conn) FS() server.FileSystem { return c.fs }

// Fingerprint is the peer's TLS-verified identity fingerprint.
func (c *Conn) Fingerprint() string { return c.fp }

func (c *Conn) Close() error { return c.client.Close() }

// Dial connects to addr and attaches to its root. Two address shapes are
// recognized, dispatched by shape alone — no separate builtin or call site
// needs to know which: a bare "host:port" dials over mutual TLS using this
// install's 9auth identity, verifying the peer against
// ~/.config/9/known-peers with TOFU semantics (a genuinely new address
// prompts once on stderr/stdin; a known address whose presented
// fingerprint no longer matches is always a loud refusal, never a silent
// pass); an absolute path or a "unix:"-prefixed path dials a local
// Unix-domain-socket 9P server instead, with no TLS/9auth involved at all
// — see dialUnix's doc comment for why that's the right trust tier. The
// two shapes can never collide: a TCP address never starts with "/" and
// never carries a "unix:" prefix.
func Dial(ctx context.Context, addr string) (*Conn, error) {
	if path, ok := unixSocketPath(addr); ok {
		return dialUnix(ctx, path)
	}
	id, err := auth.Load()
	if err != nil {
		return nil, err
	}
	knownPeersPath, err := auth.KnownPeersPath()
	if err != nil {
		return nil, err
	}
	known, err := auth.LoadKnownPeers(knownPeersPath)
	if err != nil {
		return nil, err
	}
	return dialTCP(ctx, id, knownPeersPath, known, addr, os.Stdin)
}

// unixSocketPath reports whether addr names a Unix-domain socket rather
// than a TCP host:port, returning the filesystem path to dial if so.
func unixSocketPath(addr string) (path string, ok bool) {
	if rest, found := strings.CutPrefix(addr, "unix:"); found {
		return rest, true
	}
	if strings.HasPrefix(addr, "/") {
		return addr, true
	}
	return "", false
}

// maxUnixSocketPathLen is the largest usable AF_UNIX path on Linux:
// sockaddr_un.sun_path is a fixed 108-byte buffer holding a
// null-terminated C string, so one byte is reserved for the trailing NUL.
// Dialing or listening on a longer path fails at the syscall layer with a
// bare "invalid argument" and no hint why (hit for real verifying #3, with
// a scratchpad path a few bytes over) — checking up front turns that into
// an actionable error instead.
const maxUnixSocketPathLen = 107

func checkUnixSocketPathLen(path string) error {
	if len(path) > maxUnixSocketPathLen {
		return fmt.Errorf("remote: socket path %q is %d bytes, over the %d-byte Unix-domain-socket limit (sockaddr_un.sun_path) — use a shorter path, e.g. under $XDG_RUNTIME_DIR", path, len(path), maxUnixSocketPathLen)
	}
	return nil
}

// dialUnix connects to a local Unix-domain-socket 9P server at path with
// no TLS handshake and no 9auth identity involved: the socket's own file
// permissions are already the trust boundary, the same category dirfs's
// plain local-directory bind (cmd/9sh/main.go's bootstrap) sits in — a
// locally-run 9P server reached over a Unix socket has nothing more to
// authenticate than a local directory does. Matches 9p's own resolution of
// "no unix-socket transport" (9p#3) and 9pc's reasoning for why a local
// single-machine 9P server has no reason to open a TCP port or link 9auth
// at all.
func dialUnix(ctx context.Context, path string) (*Conn, error) {
	if err := checkUnixSocketPathLen(path); err != nil {
		return nil, err
	}
	var d net.Dialer
	rawConn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("remote: dial %s: %w", path, err)
	}
	c, err := client.NewClient(rawConn)
	if err != nil {
		rawConn.Close()
		return nil, err
	}
	rootFid, err := c.AttachContext(ctx, "9sh", "")
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("remote: attaching to %s: %w", path, err)
	}
	return &Conn{client: c, fs: &clientFS{root: rootFid}}, nil
}

// dialTCP is Dial's TCP+TLS logic against explicit, already-resolved
// inputs — split out so tests can exercise two distinct identities and
// known-peers stores in one process (auth.Load reads from process-wide
// env-derived paths, which can't safely vary per goroutine) and inject a
// canned prompt response instead of a real terminal.
func dialTCP(ctx context.Context, id *auth.Identity, knownPeersPath string, known auth.KnownPeers, addr string, prompt io.Reader) (*Conn, error) {
	var verifyErr error
	var peerFP string
	accept := func(presented string) bool {
		if err := verifyPeer(knownPeersPath, known, addr, presented, prompt); err != nil {
			verifyErr = err
			return false
		}
		peerFP = presented
		return true
	}

	var d net.Dialer
	rawConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("remote: dial %s: %w", addr, err)
	}
	tlsConn := tls.Client(rawConn, id.ClientTLSConfig(accept))
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		if verifyErr != nil {
			return nil, verifyErr
		}
		return nil, fmt.Errorf("remote: %s: %w", addr, err)
	}

	c, err := client.NewClient(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}
	rootFid, err := c.AttachContext(ctx, "9sh", "")
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("remote: attaching to %s: %w (the peer may have rejected this connection — check its authorized-peers)", addr, err)
	}
	return &Conn{client: c, fp: peerFP, fs: &clientFS{root: rootFid}}, nil
}

// verifyPeer decides whether to trust addr's presented fingerprint,
// consulting the known-peers store first, then — on a genuine first
// connection — prompting on prompt. A mismatch against an existing
// known-peers entry is always a refusal, never a prompt: a changed
// fingerprint for an address already vouched for is exactly what TOFU
// exists to catch loudly, not paper over. Mirrors 9vcs's cmd/9vcs/
// sync.go verifyPeer.
func verifyPeer(knownPeersPath string, known auth.KnownPeers, addr, presented string, prompt io.Reader) error {
	if fp, ok := known[addr]; ok {
		if presented != fp {
			return fmt.Errorf("REMOTE FINGERPRINT HAS CHANGED for %s\n  known:     %s\n  presented: %s\nsomeone may be impersonating this peer, or it legitimately regenerated its identity", addr, fp, presented)
		}
		return nil
	}
	trust, err := promptTrustPeer(prompt, addr, presented)
	if err != nil {
		return fmt.Errorf("reading trust prompt response: %w", err)
	}
	if !trust {
		return fmt.Errorf("connection to %s declined: fingerprint %s not trusted", addr, presented)
	}
	return auth.RememberPeer(knownPeersPath, addr, presented)
}

func promptTrustPeer(in io.Reader, addr, fingerprint string) (bool, error) {
	fmt.Fprintf(os.Stderr, "The authenticity of peer %q can't be established.\nFingerprint: %s\n", addr, fingerprint)
	fmt.Fprint(os.Stderr, "Trust this peer and remember it for future connections? [y/N] ")
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes", nil
}

// AuthorizedPeersPath returns this install's global authorized-peers file.
// One allowlist gates every peer that attaches to this 9sh's exported
// namespace at all (fingerprint -> minimum permission, the same shape
// 9vcs's per-repo authorized-peers already uses) and is still the only
// file the TLS handshake's own accept gate ever consults — see
// ListenWithRootPerms's doc comment for why that specific gate can't be
// scoped per-root without a bigger change to the trust model, and for
// what a root-specific file (this design doc's ".9sh/authorized-peers"
// per exported root, echoing 9vcs's per-repo file) *can* still do once a
// peer is past it.
func AuthorizedPeersPath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "authorized-peers"), nil
}

// ListenUnix serves fs over a Unix-domain socket at path, restricted to
// connections from this process's own UID — the mirror image of dialUnix's
// trust reasoning (issue #2), for the reverse direction (issue #3): purely
// local namespace access needs no TLS handshake or 9auth identity at all,
// per authFS's own doc comment ("purely local synthetic namespaces ride a
// Unix-domain socket, bounded by OS permissions, no TLS/identity
// overhead").
//
// Deliberately unscoped: fs is served exactly as given, remote-sourced
// binds (anything grafted under /n/<host> via an earlier dial+bind)
// included. A same-UID connection is trusted to exercise whatever trust
// relationships this process already holds — the same shape ssh-agent/
// gpg-agent forwarding already relies on — rather than being narrowed to
// "local-only" content. Carving out /n/* specifically would be security
// theater on top of an already-full grant (/jobs alone is live process
// control: signal/kill/stdin over every job this shell runs) while
// breaking the tier model's "everything bound behaves uniformly"
// invariant.
//
// Two things make "same-UID" a correctly-enforced boundary rather than an
// accidental one, since net.Listen("unix", ...) alone gives neither: the
// socket file is explicitly chmod'd 0600 after bind on every platform
// (its mode otherwise depends on the caller's ambient umask, not a fixed
// value), and on Linux every accepted connection is additionally checked
// via SO_PEERCRED against os.Getuid() (peercred_linux.go), rejected
// (never surfaced as a fatal Accept error) on any mismatch —
// belt-and-suspenders beyond the filesystem permission check, and a real
// identity to reject on even without a 9auth handshake. There's no
// portable stdlib equivalent to SO_PEERCRED, so elsewhere (darwin, ...)
// this second check is a no-op and the 0600 permission check alone is
// the boundary (peercred_other.go) — the same trust level every other
// platform-agnostic local bind (dirfs's /local, for one) already relies
// on throughout this codebase.
//
// Serving continues until ctx is canceled or the listener is otherwise
// closed.
func ListenUnix(ctx context.Context, path string, fs server.FileSystem) (net.Listener, error) {
	if err := checkUnixSocketPathLen(path); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("remote: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("remote: chmod %s: %w", path, err)
	}

	srv := &server.Server{FS: fs}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	go srv.Serve(&peerCredListener{Listener: l})
	return l, nil
}

// removeStaleSocket clears a leftover socket file from an earlier,
// uncleanly-exited ListenUnix so this one can rebind the same path — the
// same recovery dockerd and most other Unix-socket servers perform.
// Dialing first (rather than unconditionally unlinking) distinguishes
// "nothing here," "a dead socket file," and "a live peer already
// listening": only the dead-socket case is safe to remove automatically.
// Anything ambiguous (permission denied, path exists but isn't a socket,
// ...) is surfaced instead of guessed at, since guessing wrong either
// clobbers a live peer's socket or silently hands this call a
// non-functional path.
func removeStaleSocket(path string) error {
	conn, err := net.Dial("unix", path)
	if err == nil {
		conn.Close()
		return fmt.Errorf("remote: %s: address already in use (another process is listening)", path)
	}
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil
	case errors.Is(err, syscall.ECONNREFUSED):
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("remote: removing stale socket %s: %w", path, rmErr)
		}
		return nil
	default:
		return fmt.Errorf("remote: checking existing socket %s: %w", path, err)
	}
}

// peerCredListener wraps a Unix-socket net.Listener so only connections
// from this process's own UID are ever handed to server.Server — see
// ListenUnix's doc comment. A rejected connection is closed and the
// accept loop continues; it's never surfaced as an Accept error, which
// would otherwise (per server.Server.Serve's contract) tear down the
// whole listener over a single unwelcome connection attempt.
type peerCredListener struct {
	net.Listener
}

func (l *peerCredListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			c.Close() // shouldn't happen: this listener only ever accepts unix connections
			continue
		}
		uid, err := peerUID(uc)
		if err != nil || uid != uint32(os.Getuid()) {
			c.Close()
			continue
		}
		return c, nil
	}
}

// Listen starts serving fs over mutual TLS on addr: only peers listed in
// this install's global authorized-peers file complete the TLS handshake
// at all — an unauthorized fingerprint is refused before any 9P message is
// reachable, matching the design doc's "Tauth becomes a formality" (the
// real gate is the handshake itself). Serving continues until ctx is
// canceled or the listener is otherwise closed. Equivalent to
// ListenWithRootPerms with no root overrides.
func Listen(ctx context.Context, addr string, fs server.FileSystem) (net.Listener, error) {
	return ListenWithRootPerms(ctx, addr, fs, nil)
}

// ListenWithRootPerms is Listen plus rootPerms: a map from one of fs's
// top-level path segments (e.g. "local") to a distinct authorized-peers
// file, scoping that one subtree's permissions differently from the
// connection-wide default AuthorizedPeersPath loads — 9sh's concrete take
// on the design doc's per-exported-namespace-root ACL, beyond today's
// single flat file. A segment absent from rootPerms falls back to the
// connection-wide file exactly as Listen already behaves.
//
// Scope, deliberate: the TLS handshake's own accept callback (below)
// still checks only the connection-wide file — a peer entirely absent
// from it can't complete a handshake at all, regardless of what a
// root-specific file might otherwise grant, since the handshake runs
// before any 9P message (and so any path) is reachable at all. A root
// override can only broaden or narrow what an *already-connected* peer
// may do inside that one subtree, never grant admission by itself.
// Lifting that fully would mean moving authorization entirely out of the
// TLS accept callback and into Attach/Walk — a bigger change to the
// trust model than this generalizes; left as a further open question.
func ListenWithRootPerms(ctx context.Context, addr string, fs server.FileSystem, rootPerms map[string]string) (net.Listener, error) {
	id, err := auth.Load()
	if err != nil {
		return nil, err
	}
	authPath, err := AuthorizedPeersPath()
	if err != nil {
		return nil, err
	}
	authorized, err := auth.LoadAuthorizedPeers(authPath)
	if err != nil {
		return nil, err
	}
	roots := make(map[string]auth.AuthorizedPeers, len(rootPerms))
	for seg, path := range rootPerms {
		ap, err := auth.LoadAuthorizedPeers(path)
		if err != nil {
			return nil, fmt.Errorf("remote: loading root ACL for /%s: %w", seg, err)
		}
		roots[seg] = ap
	}
	return listen(ctx, id, authorized, roots, addr, fs)
}

func listen(ctx context.Context, id *auth.Identity, authorized auth.AuthorizedPeers, rootPerms map[string]auth.AuthorizedPeers, addr string, fs server.FileSystem) (net.Listener, error) {
	tlsCfg := id.ServerTLSConfig(func(fp string) bool {
		return authorized.Allows(fp, auth.PermRead)
	})
	l, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("remote: listen %s: %w", addr, err)
	}

	srv := &server.Server{
		FS: &authFS{fs: fs, authorized: authorized, rootPerms: rootPerms},
		ConnContext: func(connCtx context.Context, c net.Conn) context.Context {
			tc, ok := c.(*tls.Conn)
			if !ok {
				return connCtx // shouldn't happen: l is always a tls.Listener
			}
			// Force the handshake to complete synchronously here, before
			// any 9P message is read on this connection — tls.Listener's
			// Accept returns before the handshake runs (it's normally
			// lazy, on first Read/Write), and VerifyPeerCertificate (the
			// real accept/refuse gate, wired in ServerTLSConfig) only
			// runs as part of that handshake. An unauthorized peer's
			// handshake fails here; Serve then hits an immediate read
			// error on the connection and tears it down without ever
			// reaching FS.Attach.
			if err := tc.HandshakeContext(connCtx); err != nil {
				return connCtx
			}
			state := tc.ConnectionState()
			if len(state.PeerCertificates) == 0 {
				return connCtx
			}
			fp, err := auth.FingerprintOf(state.PeerCertificates[0])
			if err != nil {
				return connCtx
			}
			return context.WithValue(connCtx, peerFPKey{}, fp)
		},
	}
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	go srv.Serve(l)
	return l, nil
}
