package remote

import (
	"context"
	"fmt"

	auth "github.com/sandgorgon/9auth"
	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

// authFS wraps a served FileSystem so every attach is gated by the caller's
// TLS-verified fingerprint (see PeerFingerprint) against authorized, and
// every mutation requires at least auth.PermWrite. Only used by Listen —
// purely local/in-process namespace use (kyu's own evaluator, and anything
// reached over the local Unix-domain-socket tier) never passes through
// this: per the design doc, "purely local synthetic namespaces ride a
// Unix-domain socket, bounded by OS permissions, no TLS/identity overhead."
//
// v1 simplification: auth.PermPropose (9vcs's patch-graph-specific "may
// propose but not move a ref directly" tier) doesn't map cleanly onto raw
// file semantics, so it's treated as read-only here, the same as
// PermRead — an open question, not a settled mapping.
type authFS struct {
	fs         server.FileSystem
	authorized auth.AuthorizedPeers
}

func (a *authFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	fp, ok := PeerFingerprint(ctx)
	if !ok {
		return nil, fmt.Errorf("remote: attach: no verified peer identity on this connection")
	}
	perm, ok := a.authorized[fp]
	if !ok {
		// Shouldn't normally happen: the TLS handshake's accept callback
		// already required at least PermRead for the connection to reach
		// here at all. Checked again in case authorized changes shape
		// later (e.g. per-subtree ACLs) without the handshake gate
		// changing to match.
		return nil, fmt.Errorf("remote: attach: peer %s is not authorized", fp)
	}
	f, err := a.fs.Attach(ctx, uname, aname)
	if err != nil {
		return nil, err
	}
	return &authFile{File: f, perm: perm}, nil
}

type authFile struct {
	server.File
	perm auth.Permission
}

func (f *authFile) requireWrite() error {
	if f.perm < auth.PermWrite {
		return fmt.Errorf("remote: write access denied (peer has %s permission)", f.perm)
	}
	return nil
}

func (f *authFile) Walk(ctx context.Context, name string) (server.File, error) {
	child, err := f.File.Walk(ctx, name)
	if err != nil {
		return nil, err
	}
	return &authFile{File: child, perm: f.perm}, nil
}

func (f *authFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if err := f.requireWrite(); err != nil {
		return 0, err
	}
	return f.File.Write(ctx, offset, p)
}

func (f *authFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	if err := f.requireWrite(); err != nil {
		return nil, err
	}
	child, err := f.File.Create(ctx, name, perm, mode)
	if err != nil {
		return nil, err
	}
	return &authFile{File: child, perm: f.perm}, nil
}

func (f *authFile) Remove(ctx context.Context) error {
	if err := f.requireWrite(); err != nil {
		return err
	}
	return f.File.Remove(ctx)
}

func (f *authFile) WStat(ctx context.Context, st p9.Stat) error {
	if err := f.requireWrite(); err != nil {
		return err
	}
	return f.File.WStat(ctx, st)
}
