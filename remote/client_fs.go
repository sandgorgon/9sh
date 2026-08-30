package remote

import (
	"context"
	"fmt"
	"io"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/client"
	"github.com/sandgorgon/9p/server"
)

// clientFS adapts a dialed *client.Client into a server.FileSystem, so
// ns.Namespace.BindFS can graft a remote peer's namespace exactly like any
// other backend — the "Native tier" from the design doc's architecture
// section, just reached over the wire instead of in-process. Only root is
// needed here: clientFile does all its own work through Fid/File methods
// (see its doc comment), with no separate *client.Client reference at all.
type clientFS struct {
	root *client.Fid // the client's attach root
}

// Attach returns a fresh clone of the attach root as a clientFile. fs.root
// itself is never handed out and never clunked — every clientFile (this
// one included) gets its own independent fid, cloned from fs.root, so
// Closing any single clientFile (including a root-positioned one from an
// earlier Attach) can never invalidate fs.root as the shared anchor other
// clientFiles still use for their own path-based re-walks (Walk/"..").
// See clientFile's doc comment.
func (fs *clientFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	rootClone, err := fs.root.WalkContext(ctx) // zero names: clone, per Fid.WalkContext's own doc
	if err != nil {
		return nil, fmt.Errorf("remote: attach: %w", err)
	}
	st, err := rootClone.StatContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("remote: attach: stat: %w", err)
	}
	return &clientFile{root: fs.root, fid: rootClone, st: st}, nil
}

// clientFile adapts one node of a remote attach tree into a local
// server.File.
//
// Open and Create reuse f.fid directly via Fid.OpenFile/CreateFile
// (github.com/sandgorgon/9p v0.7.0+) rather than discarding it and
// re-walking from the attach root by path string through
// Client.Open/Create — one real Twalk round-trip saved per open, and for
// Create, one full extra walk-for-metadata avoided entirely (see Create's
// own doc comment). Filed as sandgorgon/9p#4, fixed in
// github.com/sandgorgon/9p#5 — this adapter no longer needs joinPath or
// the *client.Client at all for I/O, only for the one remaining
// Client-level operation, Attach's root clone (see clientFS).
type clientFile struct {
	root *client.Fid // shared anchor for path-based re-walks; never Closed by any clientFile — see clientFS.Attach
	fid  *client.Fid // this node's own handle — metadata before Open/Create, and (once one succeeds) the same fid *client.File wraps for I/O; see Open/Create/Close
	path []string    // relative to the attach root; nil = root itself
	st   p9.Stat

	open *client.File // non-nil once Open/Create has succeeded; always wraps fid itself, never a second independent fid — see Close
}

func (f *clientFile) Qid() p9.Qid { return f.st.Qid }

func (f *clientFile) Stat(ctx context.Context) (p9.Stat, error) {
	st, err := f.fid.StatContext(ctx)
	if err != nil {
		return p9.Stat{}, err
	}
	f.st = st
	return st, nil
}

func (f *clientFile) WStat(ctx context.Context, st p9.Stat) error {
	return f.fid.WStatContext(ctx, st)
}

func (f *clientFile) Walk(ctx context.Context, name string) (server.File, error) {
	if name == ".." {
		if len(f.path) == 0 {
			return f, nil // ".." at root is a no-op, per Plan-9 convention
		}
		return f.walkTo(ctx, f.path[:len(f.path)-1])
	}
	newPath := append(append([]string{}, f.path...), name)
	return f.walkTo(ctx, newPath)
}

func (f *clientFile) walkTo(ctx context.Context, path []string) (server.File, error) {
	fid, err := f.root.WalkContext(ctx, path...)
	if err != nil {
		return nil, err
	}
	st, err := fid.StatContext(ctx)
	if err != nil {
		return nil, err
	}
	return &clientFile{root: f.root, fid: fid, path: path, st: st}, nil
}

// Open opens f.fid itself for I/O (Fid.OpenFile) rather than discarding it
// and re-walking the path from the attach root — f.open ends up wrapping
// the exact same fid f.fid already names, not a second one; see Close.
func (f *clientFile) Open(ctx context.Context, mode p9.Mode) error {
	of, err := f.fid.OpenFileContext(ctx, mode)
	if err != nil {
		return err
	}
	f.open = of
	if st, serr := of.Stat(); serr == nil {
		f.st = st
	}
	return nil
}

// Create clones f.fid first — Fid.CreateFile creates name under the fid
// it's called on and then repositions that same fid onto the new child
// (matching Tcreate's wire semantics), so calling it directly on f.fid
// would silently repoint this directory's own handle at the child it just
// created. Cloning (a cheap zero-element Walk) keeps f usable as the
// directory it already is, and still costs one fewer round trip overall
// than the old path-string approach: that walked to the parent, created,
// then walked the whole new path again from the root purely to get a
// metadata fid for the returned node — clone+create is two round trips
// total, not three, and the resulting fid already serves both metadata
// and I/O for the new child.
func (f *clientFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	dirClone, err := f.fid.WalkContext(ctx)
	if err != nil {
		return nil, err
	}
	of, err := dirClone.CreateFileContext(ctx, name, perm, mode)
	if err != nil {
		dirClone.ClunkContext(ctx)
		return nil, err
	}
	st, _ := of.Stat()
	newPath := append(append([]string{}, f.path...), name)
	return &clientFile{root: f.root, fid: dirClone, path: newPath, st: st, open: of}, nil
}

// Read seeks the open *client.File to offset and uses its plain io.Reader
// semantics (Read), never ReadAt: ReadAt honors Go's strict io.ReaderAt
// contract where any short read means EOF, which is the wrong contract for
// a remote file that may still be growing (a live job stdout stream read
// through a bound /n/host in particular) — the exact class of bug
// job/growbuf.go documents on the serving side of this same problem.
// Seek(SeekStart) never does a wire round trip, so this costs nothing extra
// for the common sequential-read case.
func (f *clientFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	if f.open == nil {
		return 0, fmt.Errorf("remote: read: not open")
	}
	if _, err := f.open.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return f.open.Read(p)
}

func (f *clientFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if f.open == nil {
		return 0, fmt.Errorf("remote: write: not open")
	}
	return f.open.WriteAt(p, offset)
}

func (f *clientFile) Remove(ctx context.Context) error {
	return f.fid.RemoveContext(ctx)
}

// Close clunks f's fid exactly once. Unlike before Open/Create switched to
// Fid.OpenFile/CreateFile, f.open (once set) always wraps f.fid itself,
// not a second independent fid — File.Close already clunks it, so a
// separate f.fid.ClunkContext here would double-clunk the same fid and
// surface a spurious "unknown fid" error from the second one.
func (f *clientFile) Close() error {
	if f.open != nil {
		return f.open.Close()
	}
	if f.fid != nil {
		return f.fid.ClunkContext(context.Background())
	}
	return nil
}
