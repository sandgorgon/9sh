// Package remote implements 9sh's mutual-TLS bridge between 9auth's
// standing per-install identity and p9's client/server: authenticate once
// at the TLS layer, and Tattach stays a formality — exactly the design
// doc's identity/auth bridge. Dial's trust decision (known-peers, TOFU
// first-connect prompt, loud refusal on a changed fingerprint) mirrors
// 9vcs's cmd/9vcs/sync.go dialPeer byte-for-byte in spirit, so one trust
// model covers VCS sync, namespace mounts, and proxy jobs alike, as
// designed. A verified peer's fingerprint threads through context.Context
// via server.Server's ConnContext hook into every request the connection
// ever makes, including Attach — "uname in Tattach stops being a
// client-asserted string; the server derives identity from the TLS
// session instead."
package remote

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

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

// Dial connects to addr over mutual TLS using this install's 9auth
// identity, verifying the peer against ~/.config/9/known-peers with TOFU
// semantics (a genuinely new address prompts once on stderr/stdin; a known
// address whose presented fingerprint no longer matches is always a loud
// refusal, never a silent pass), then attaches to its root.
func Dial(ctx context.Context, addr string) (*Conn, error) {
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
	return dial(ctx, id, knownPeersPath, known, addr, os.Stdin)
}

// dial is Dial's logic against explicit, already-resolved inputs — split
// out so tests can exercise two distinct identities and known-peers stores
// in one process (auth.Load reads from process-wide env-derived paths,
// which can't safely vary per goroutine) and inject a canned prompt
// response instead of a real terminal.
func dial(ctx context.Context, id *auth.Identity, knownPeersPath string, known auth.KnownPeers, addr string, prompt io.Reader) (*Conn, error) {
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
	return &Conn{client: c, fp: peerFP, fs: &clientFS{c: c, root: rootFid}}, nil
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
// v1 simplification of the design doc's per-exported-namespace-root ACL
// (".9sh/authorized-peers" per root): one allowlist gates every peer that
// attaches to this 9sh's exported namespace at all, fingerprint -> minimum
// permission, the same shape 9vcs's per-repo authorized-peers already
// uses — narrowing to one root per served namespace is an open question
// for later, not a correctness gap (nothing is under-protected, just not
// yet as finely scoped as the design doc ultimately calls for).
func AuthorizedPeersPath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "authorized-peers"), nil
}

// Listen starts serving fs over mutual TLS on addr: only peers listed in
// this install's global authorized-peers file complete the TLS handshake
// at all — an unauthorized fingerprint is refused before any 9P message is
// reachable, matching the design doc's "Tauth becomes a formality" (the
// real gate is the handshake itself). Serving continues until ctx is
// canceled or the listener is otherwise closed.
func Listen(ctx context.Context, addr string, fs server.FileSystem) (net.Listener, error) {
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
	return listen(ctx, id, authorized, addr, fs)
}

func listen(ctx context.Context, id *auth.Identity, authorized auth.AuthorizedPeers, addr string, fs server.FileSystem) (net.Listener, error) {
	tlsCfg := id.ServerTLSConfig(func(fp string) bool {
		return authorized.Allows(fp, auth.PermRead)
	})
	l, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("remote: listen %s: %w", addr, err)
	}

	srv := &server.Server{
		FS: &authFS{fs: fs, authorized: authorized},
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
