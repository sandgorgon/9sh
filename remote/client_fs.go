package remote

import (
	"context"
	"fmt"
	"io"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/client"
	"github.com/sandgorgon/9p/server"
)

// clientFS adapts a dialed *client.Client into a server.FileSystem, so
// ns.Namespace.BindFS can graft a remote peer's namespace exactly like any
// other backend — the "Native tier" from the design doc's architecture
// section, just reached over the wire instead of in-process.
type clientFS struct {
	c    *client.Client
	root *client.Fid // the client's attach root
}

// Attach returns a fresh clone of the attach root as a clientFile. fs.root
// itself is never handed out and never clunked — every clientFile (this
// one included) gets its own independent fid, cloned from fs.root, so
// Closing any single clientFile (including a root-positioned one from an
// earlier Attach) can never invalidate fs.root as the shared anchor other
// clientFiles still use for their own path-based re-walks (Open/Create/
// ".."). See clientFile's doc comment.
func (fs *clientFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	rootClone, err := fs.root.WalkContext(ctx) // zero names: clone, per Fid.WalkContext's own doc
	if err != nil {
		return nil, fmt.Errorf("remote: attach: %w", err)
	}
	st, err := rootClone.StatContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("remote: attach: stat: %w", err)
	}
	return &clientFile{c: fs.c, root: fs.root, fid: rootClone, st: st}, nil
}

// clientFile adapts one node of a remote attach tree into a local
// server.File.
//
// p9's client package intentionally exposes two disjoint handle types —
// *client.Fid (Walk/Stat/WStat/Remove/Create: cheap metadata operations,
// no I/O) and *client.File (Read/Write/Seek: obtained only via
// Client.Open/Create against a path string from the attach root) — with no
// public way to convert one into the other. This adapter stays entirely
// within that exported surface: Walk keeps a *client.Fid (no wire I/O
// needed for pure metadata/traversal), while Open/Create re-walk from the
// attach root by path string to obtain a *client.File. That re-walk on
// Open is a real, known inefficiency versus reusing the Fid Walk already
// produced — an accepted v1 simplification, not a correctness gap; filing
// this as a p9 API gap is worth doing if it turns out to matter in
// practice.
type clientFile struct {
	c    *client.Client
	root *client.Fid // shared anchor for path-based re-walks; never Closed by any clientFile — see clientFS.Attach
	fid  *client.Fid // this node's own independent metadata handle; Closed by Close()
	path []string    // relative to the attach root; nil = root itself
	st   p9.Stat

	open *client.File // non-nil once Open/Create has succeeded
}

func joinPath(parts []string) string { return strings.Join(parts, "/") }

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
	return &clientFile{c: f.c, root: f.root, fid: fid, path: path, st: st}, nil
}

func (f *clientFile) Open(ctx context.Context, mode p9.Mode) error {
	of, err := f.c.OpenContext(ctx, joinPath(f.path), mode)
	if err != nil {
		return err
	}
	f.open = of
	if st, serr := of.Stat(); serr == nil {
		f.st = st
	}
	return nil
}

func (f *clientFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	newPath := append(append([]string{}, f.path...), name)
	of, err := f.c.CreateContext(ctx, joinPath(newPath), perm, mode)
	if err != nil {
		return nil, err
	}
	st, _ := of.Stat()
	// Client.Create only hands back I/O, not a Fid — walk to the new path
	// separately so the returned File also supports Stat/WStat/Remove/
	// further Walk like any other node.
	fid, err := f.root.WalkContext(ctx, newPath...)
	if err != nil {
		of.Close()
		return nil, err
	}
	return &clientFile{c: f.c, root: f.root, fid: fid, path: newPath, st: st, open: of}, nil
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

func (f *clientFile) Close() error {
	var err error
	if f.open != nil {
		err = f.open.Close()
	}
	if f.fid != nil {
		if cerr := f.fid.ClunkContext(context.Background()); err == nil {
			err = cerr
		}
	}
	return err
}
