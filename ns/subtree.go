package ns

import (
	"context"
	"fmt"
	"strings"

	"github.com/sandgorgon/9p/server"
)

// Subtree returns a server.FileSystem that serves only the part of the
// namespace at path, as if it were the whole namespace — what a peer
// dialing a `-listen-root /work` listener sees as its "/". Nothing
// outside path is reachable, including by walking "..".
//
// path is resolved at each Attach, not now: a listener starts before the
// startup configs run, so the directory it names may be bound by a
// dotfile later. A path that doesn't resolve to a directory then is an
// Attach error the connecting peer sees, not a startup failure.
//
// readOnly additionally refuses every write through it (see roFile) —
// for sharing a tree that peers may read but never change. The
// namespace itself, and anything reached by its ordinary paths, is
// untouched by either.
func (ns *Namespace) Subtree(path string, readOnly bool) server.FileSystem {
	return &subtreeFS{ns: ns, parts: splitPath(path), ro: readOnly}
}

type subtreeFS struct {
	ns    *Namespace
	parts []string
	ro    bool
}

func (s *subtreeFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	root, err := s.ns.Attach(ctx, uname, aname)
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, s.parts)
	if err != nil {
		return nil, fmt.Errorf("ns: serving /%s: %w", strings.Join(s.parts, "/"), err)
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return nil, err
	}
	if !st.Qid.IsDir() {
		return nil, fmt.Errorf("ns: serving /%s: not a directory", strings.Join(s.parts, "/"))
	}
	var out server.File = &rootedFile{File: f}
	if s.ro {
		out = roFile{out}
	}
	return out, nil
}

// rootedFile pins a served subtree's root: ".." at the root stays at the
// root (9P's own convention for a filesystem root), and it tracks depth
// so ".." from below can only climb back up to that root, never past it
// into whatever contains the subtree in the real namespace — which the
// underlying files would otherwise happily walk into.
type rootedFile struct {
	server.File
	depth int
}

func (f *rootedFile) Walk(ctx context.Context, name string) (server.File, error) {
	if name == ".." {
		if f.depth == 0 {
			return f, nil
		}
		up, err := f.File.Walk(ctx, "..")
		if err != nil {
			return nil, err
		}
		return &rootedFile{File: up, depth: f.depth - 1}, nil
	}
	child, err := f.File.Walk(ctx, name)
	if err != nil {
		return nil, err
	}
	return &rootedFile{File: child, depth: f.depth + 1}, nil
}
