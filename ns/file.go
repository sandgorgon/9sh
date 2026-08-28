package ns

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

const sysUser = "9sh"

// nsFile is a server.File over the namespace: either still inside the
// explicit bind tree (n set), or already delegating to a real bound
// filesystem (real set) — the two are mutually exclusive, and once real
// is set every further Walk stays inside that filesystem, never
// re-consulting the bind tree.
//
// ".." is deliberately unsupported while still inside the bind tree
// (Plan-9's own namespace has famously inconsistent ".." behavior across
// binds — different bound trees can disagree about what's "up" — so this
// doesn't try to invent a answer); once real is set, ".." is delegated
// to the underlying filesystem, which may or may not support it on its
// own terms (dirfs, for instance, refuses to leave its export root).
type nsFile struct {
	ns   *Namespace
	n    *node
	real server.File
}

func (f *nsFile) Qid() p9.Qid {
	if f.real != nil {
		return f.real.Qid()
	}
	return p9.Qid{Type: p9.QTDIR, Path: f.n.pathHash}
}

func (f *nsFile) Stat(ctx context.Context) (p9.Stat, error) {
	if f.real != nil {
		return f.real.Stat(ctx)
	}
	return p9.Stat{Qid: f.Qid(), Mode: p9.DMDIR | 0755, Name: "/", Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}

func (f *nsFile) WStat(ctx context.Context, st p9.Stat) error {
	if f.real != nil {
		return f.real.WStat(ctx, st)
	}
	return errors.New("ns: cannot modify metadata of a namespace tree node")
}

func (f *nsFile) Walk(ctx context.Context, name string) (server.File, error) {
	if f.real != nil {
		child, err := f.real.Walk(ctx, name)
		if err != nil {
			return nil, err
		}
		return &nsFile{ns: f.ns, real: child}, nil
	}
	if name == ".." {
		return nil, errors.New("ns: '..' is not supported at a namespace bind point")
	}

	f.n.mu.RLock()
	child, hasChild := f.n.children[name]
	layers := f.n.layers
	f.n.mu.RUnlock()

	if hasChild {
		return &nsFile{ns: f.ns, n: child}, nil
	}
	var lastErr error
	for _, l := range layers {
		root, err := l.root(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		got, err := root.Walk(ctx, name)
		if err != nil {
			lastErr = err
			continue
		}
		return &nsFile{ns: f.ns, real: got}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ns: %s: no such file", name)
	}
	return nil, lastErr
}

func (f *nsFile) Open(ctx context.Context, mode p9.Mode) error {
	if f.real != nil {
		return f.real.Open(ctx, mode)
	}
	return nil
}

func (f *nsFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	if f.real != nil {
		child, err := f.real.Create(ctx, name, perm, mode)
		if err != nil {
			return nil, err
		}
		return &nsFile{ns: f.ns, real: child}, nil
	}
	f.n.mu.RLock()
	layers := f.n.layers
	f.n.mu.RUnlock()
	if len(layers) == 0 {
		return nil, errors.New("ns: cannot create here: this namespace directory has nothing bound to it")
	}
	root, err := layers[0].root(ctx)
	if err != nil {
		return nil, err
	}
	child, err := root.Create(ctx, name, perm, mode)
	if err != nil {
		return nil, err
	}
	return &nsFile{ns: f.ns, real: child}, nil
}

func (f *nsFile) Remove(ctx context.Context) error {
	if f.real != nil {
		return f.real.Remove(ctx)
	}
	return errors.New("ns: cannot remove a namespace bind point directly (no unbind yet)")
}

func (f *nsFile) Close() error {
	if f.real != nil {
		return f.real.Close()
	}
	return nil
}

func (f *nsFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if f.real != nil {
		return f.real.Write(ctx, offset, p)
	}
	return 0, errors.New("ns: cannot write to a namespace tree node (nothing bound here)")
}

func (f *nsFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	if f.real != nil {
		return f.real.Read(ctx, offset, p)
	}
	entries, err := f.listLocalDir(ctx)
	if err != nil {
		return 0, err
	}
	return server.MarshalDir(entries, offset, p)
}

// listLocalDir merges this tree node's explicit children with each
// bound layer's own directory listing, in layer order — a tree child
// shadows a layer entry of the same name, matching Walk's own
// tree-before-layers precedence.
func (f *nsFile) listLocalDir(ctx context.Context) ([]p9.Stat, error) {
	f.n.mu.RLock()
	children := make(map[string]*node, len(f.n.children))
	maps.Copy(children, f.n.children)
	layers := f.n.layers
	f.n.mu.RUnlock()

	seen := make(map[string]bool, len(children))
	var entries []p9.Stat

	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := children[name]
		entries = append(entries, p9.Stat{
			Qid: p9.Qid{Type: p9.QTDIR, Path: child.pathHash}, Mode: p9.DMDIR | 0755, Name: name,
			Uid: sysUser, Gid: sysUser, Muid: sysUser,
		})
		seen[name] = true
	}

	for _, l := range layers {
		root, err := l.root(ctx)
		if err != nil {
			continue // a layer that fails to attach is skipped in a listing, not fatal to the whole read
		}
		layerEntries, err := ReadDirEntries(ctx, root)
		if err != nil {
			continue
		}
		for _, e := range layerEntries {
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// ReadDirEntries reads a directory File to completion and decodes its
// concatenated Stat blobs, matching the length-prefix convention the p9
// client library itself uses to decode a Tread reply in its own ReadDir.
// Exported for any consumer that holds a raw server.File tree directly
// (kyu's eval package, for checkout's materialize step) rather than a
// real 9P client connection.
func ReadDirEntries(ctx context.Context, f server.File) ([]p9.Stat, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 8192)
	var offset int64
	for {
		n, err := f.Read(ctx, offset, tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}

	data := buf.Bytes()
	var entries []p9.Stat
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.New("ns: truncated directory entry")
		}
		size := int(binary.LittleEndian.Uint16(data))
		total := 2 + size
		if total > len(data) {
			return nil, errors.New("ns: truncated directory entry")
		}
		st, err := p9.UnmarshalStat(data[:total])
		if err != nil {
			return nil, err
		}
		entries = append(entries, st)
		data = data[total:]
	}
	return entries, nil
}
