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
// every mutation requires at least auth.PermPropose (Write/Create) or
// auth.PermWrite (Remove/WStat) — see authFile's doc comment for the
// concrete Propose-vs-Write mapping. Only used by Listen — purely
// local/in-process namespace use (kyu's own evaluator, and anything
// reached over the local Unix-domain-socket tier) never passes through
// this: per the design doc, "purely local synthetic namespaces ride a
// Unix-domain socket, bounded by OS permissions, no TLS/identity overhead."
//
// rootPerms optionally overrides authorized for one or more top-level
// path segments (see ListenWithRootPerms) — 9sh's concrete, if narrower-
// than-originally-sketched, take on the design doc's per-exported-
// namespace-root ACL (".9sh/authorized-peers" per root, echoing 9vcs's
// per-repo file). nil means "no overrides," i.e. today's one-flat-file
// behavior, unchanged.
type authFS struct {
	fs         server.FileSystem
	authorized auth.AuthorizedPeers
	rootPerms  map[string]auth.AuthorizedPeers
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
	return &authFile{File: f, fp: fp, perm: perm, authorized: a.authorized, rootPerms: a.rootPerms, atRoot: true}, nil
}

// authFile carries the effective permission for the subtree it's
// positioned at, threading it down through Walk.
//
// Propose-vs-Write mapping (this package's own concrete decision — the
// design doc leaves 9vcs's propose/write distinction "an open question"
// for raw file semantics): PermPropose is enough to Write or Create
// (add/change content), matching its 9vcs meaning of "may contribute,"
// but Remove and WStat (delete a file, or change its metadata — the
// structural-change operations closest to 9vcs's "move a ref directly")
// still require PermWrite. A peer with only read-only PermRead can do
// neither.
type authFile struct {
	server.File
	fp         string
	perm       auth.Permission
	authorized auth.AuthorizedPeers            // the ACL currently governing this subtree
	rootPerms  map[string]auth.AuthorizedPeers // per-top-level-segment overrides; see authFS
	atRoot     bool                            // true only for the File Attach itself returned — Walk always clears it
}

func (f *authFile) requirePropose() error {
	if f.perm < auth.PermPropose {
		return fmt.Errorf("remote: write access denied (peer has %s permission, need at least propose)", f.perm)
	}
	return nil
}

func (f *authFile) requireWrite() error {
	if f.perm < auth.PermWrite {
		return fmt.Errorf("remote: this operation requires write permission (peer has %s)", f.perm)
	}
	return nil
}

func (f *authFile) Walk(ctx context.Context, name string) (server.File, error) {
	child, err := f.File.Walk(ctx, name)
	if err != nil {
		return nil, err
	}
	perm, authorized := f.perm, f.authorized
	// rootPerms is only ever *consulted* at the first path segment below
	// the attach root — atRoot is true only for the File Attach handed
	// back, never for anything reached via Walk, so a subdirectory that
	// happens to share a name with an overridden root (e.g. plain/
	// elevated when "elevated" is itself a registered root) can't
	// spuriously re-trigger the lookup. Once it does apply, though, the
	// resulting perm/authorized are carried forward by every level below
	// unconditionally (the two lines right after this block, which run
	// regardless of atRoot) — an override scopes its whole subtree, not
	// just the one directory entry named after it.
	if f.atRoot {
		if override, ok := f.rootPerms[name]; ok {
			p, ok := override[f.fp]
			if !ok {
				return nil, fmt.Errorf("remote: walk: peer %s is not authorized for /%s", f.fp, name)
			}
			perm, authorized = p, override
		}
	}
	return &authFile{File: child, fp: f.fp, perm: perm, authorized: authorized, rootPerms: f.rootPerms}, nil
}

func (f *authFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if err := f.requirePropose(); err != nil {
		return 0, err
	}
	return f.File.Write(ctx, offset, p)
}

func (f *authFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	if err := f.requirePropose(); err != nil {
		return nil, err
	}
	child, err := f.File.Create(ctx, name, perm, mode)
	if err != nil {
		return nil, err
	}
	return &authFile{File: child, fp: f.fp, perm: f.perm, authorized: f.authorized, rootPerms: f.rootPerms}, nil
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
