// Package ns implements 9sh's per-process namespace: a tree built purely
// by bind (no FUSE, no OS mount table), where a path may graft one or
// more underlying server.File trees on top of each other (a union
// directory) with Plan-9's before/after/replace disposition.
//
// A Namespace is itself a server.FileSystem — Attach returns the root of
// the bind tree — so anything holding a server.File (kyu's evaluator,
// directly and in-process; a real 9P client, over the wire once /n/<host>
// exists in a later phase) gets the same transparent view.
package ns

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/sandgorgon/9p/server"
)

type Disposition int

const (
	Replace Disposition = iota
	Before
	After
)

// layer is one filesystem grafted at a bind point. It resolves lazily
// (BindFS, the Go-level bootstrap entry point that attaches a raw
// server.FileSystem) or is already resolved (BindPath, kyu's own `bind`,
// which grafts a File already reachable elsewhere in the namespace —
// captured at bind time, per Plan-9 semantics: a later bind elsewhere
// doesn't retroactively change this one).
type layer struct {
	mu       sync.Mutex
	fs       server.FileSystem
	subpath  []string
	resolved server.File
}

func (l *layer) root(ctx context.Context) (server.File, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.resolved != nil {
		return l.resolved, nil
	}
	f, err := l.fs.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	for _, part := range l.subpath {
		f, err = f.Walk(ctx, part)
		if err != nil {
			return nil, err
		}
	}
	l.resolved = f
	return f, nil
}

// node is one point in the namespace's own explicit tree — built up by
// binds, as opposed to content that lives inside a bound filesystem.
// Walking falls through to a node's layers only once there's no further
// explicit tree child for the next path element; see nsFile.Walk.
type node struct {
	mu       sync.RWMutex
	children map[string]*node
	layers   []*layer
	pathHash uint64 // Qid.Path for this tree node itself, when it has no layer to borrow one from
}

// Namespace is a bind tree. The zero value is not usable; use New.
type Namespace struct {
	root *node
}

func New() *Namespace {
	return &Namespace{root: &node{children: map[string]*node{}, pathHash: hashPath("/")}}
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func hashPath(p string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(p))
	return h.Sum64()
}

func (ns *Namespace) ensureNode(path string) *node {
	n := ns.root
	var acc strings.Builder
	for _, part := range splitPath(path) {
		acc.WriteByte('/')
		acc.WriteString(part)
		n.mu.Lock()
		child, ok := n.children[part]
		if !ok {
			child = &node{children: map[string]*node{}, pathHash: hashPath(acc.String())}
			n.children[part] = child
		}
		n.mu.Unlock()
		n = child
	}
	return n
}

// BindFS grafts a raw server.FileSystem onto dst — 9sh's own Go-level
// bootstrap step (e.g. mounting job.FS at "/jobs"), not reachable from
// kyu script: kyu's `bind` only reshapes what's already in the
// namespace (BindPath). subpath, if non-empty, is walked within fs
// before grafting, so a single fs can be bootstrap-bound at more than
// one point rooted at different subtrees.
func (ns *Namespace) BindFS(fs server.FileSystem, subpath string, dst string, disp Disposition) error {
	n := ns.ensureNode(dst)
	l := &layer{fs: fs, subpath: splitPath(subpath)}
	return n.addLayers([]*layer{l}, disp)
}

// BindPath grafts the namespace node(s) currently reachable at srcPaths
// onto dst, resolving each srcPath by walking the namespace itself —
// this is kyu's `bind`, including the multi-source case from a
// namespace-union expression (`bind (a + b) /dst`).
func (ns *Namespace) BindPath(ctx context.Context, srcPaths []string, dst string, disp Disposition) error {
	if len(srcPaths) == 0 {
		return errors.New("ns: bind: no source path given")
	}
	root, err := ns.Attach(ctx, "9sh", "")
	if err != nil {
		return err
	}
	layers := make([]*layer, len(srcPaths))
	for i, sp := range srcPaths {
		f, err := walkAll(ctx, root, splitPath(sp))
		if err != nil {
			return fmt.Errorf("ns: bind: %s: %w", sp, err)
		}
		layers[i] = &layer{resolved: f}
	}
	n := ns.ensureNode(dst)
	return n.addLayers(layers, disp)
}

// Unbind clears everything bound at path — kyu's `unbind`, the inverse
// of bind. Only removes what's directly bound at path itself; a child
// path with its own separate bind (e.g. /local/sub, if that was itself
// bound independently) is untouched. Unbinding a path with nothing
// bound there is an error, not a silent no-op — matching bind's own
// error-on-misuse convention (a bad bind aborts hard too, not an
// ordinary in-stream ErrorVal like a missing file would be).
func (ns *Namespace) Unbind(path string) error {
	n, ok := ns.findNode(path)
	if !ok {
		return fmt.Errorf("ns: unbind: nothing bound at %s", path)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.layers) == 0 {
		return fmt.Errorf("ns: unbind: nothing bound at %s", path)
	}
	n.layers = nil
	return nil
}

// findNode walks the explicit tree to path without creating anything,
// unlike ensureNode — Unbind shouldn't invent a node just to discover
// there was never anything there.
func (ns *Namespace) findNode(path string) (*node, bool) {
	n := ns.root
	for _, part := range splitPath(path) {
		n.mu.RLock()
		child, ok := n.children[part]
		n.mu.RUnlock()
		if !ok {
			return nil, false
		}
		n = child
	}
	return n, true
}

func (n *node) addLayers(newLayers []*layer, disp Disposition) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch disp {
	case Replace:
		n.layers = newLayers
	case Before:
		n.layers = append(append([]*layer{}, newLayers...), n.layers...)
	case After:
		n.layers = append(n.layers, newLayers...)
	default:
		return fmt.Errorf("ns: unknown disposition %d", disp)
	}
	return nil
}

func walkAll(ctx context.Context, root server.File, parts []string) (server.File, error) {
	f := root
	for _, part := range parts {
		var err error
		f, err = f.Walk(ctx, part)
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

// Attach implements server.FileSystem: it returns the root of the bind
// tree, so a Namespace can be handed to anything that wants a
// server.File — kyu's evaluator in-process, or a real server.Server for
// a remote peer once /n/<host> exists.
func (ns *Namespace) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	return &nsFile{ns: ns, n: ns.root}, nil
}
