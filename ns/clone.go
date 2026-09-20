package ns

import (
	"github.com/sandgorgon/9p/server"
)

// namespaceBound is implemented by a filesystem that holds a *Namespace
// of its own (BindsFS reads the namespace it is bound in), so a cloned
// namespace can point its copy at the clone rather than at the original.
type namespaceBound interface {
	cloneFor(ns *Namespace) server.FileSystem
}

// Clone returns an independent copy of the namespace: binds and unbinds
// made in either afterwards don't show up in the other. It is what a
// process-private namespace is (Plan 9's rfork): the copy starts out
// identical, so everything already bound — including a live /jobs, whose
// underlying filesystem is shared, not duplicated — is reachable in it.
//
// What is copied is the bind *tree*: nodes and their layer lists. The
// bound filesystems themselves are shared, so a write through a shared
// layer is visible from both namespaces (a bind is a view, not a
// snapshot of content). Three details keep the copy coherent:
//
//   - A layer bound from another namespace path (`bind /work, /alias`)
//     captured a live node of the tree, not a frozen copy. Those are
//     remapped to the corresponding node of the clone, so rebinding
//     /work inside the clone changes /alias inside the clone too — and
//     not the original's.
//   - A layer whose filesystem holds a namespace (BindsFS, behind /ns)
//     is re-created against the clone, so /ns/binds inside the clone
//     describes the clone.
//   - The bind log and sequence counter carry over, so the clone's /ns/log
//     is the original's history plus what happens in the clone.
//
// Read-only layers stay read-only. The clone does not serve anything: a
// listener started from the original keeps serving the original.
func (ns *Namespace) Clone() *Namespace {
	c := &Namespace{}
	c.seq.Store(ns.seq.Load())

	// Two passes: copy every node first so layer sources that point at
	// another node can be remapped to its copy regardless of order.
	remap := map[*node]*node{}
	c.root = cloneNode(ns.root, remap)
	fix := func(n *node) {}
	fix = func(n *node) {
		n.mu.Lock()
		for i, l := range n.layers {
			n.layers[i] = cloneLayer(l, c, remap)
		}
		children := make([]*node, 0, len(n.children))
		for _, ch := range n.children {
			children = append(children, ch)
		}
		n.mu.Unlock()
		for _, ch := range children {
			fix(ch)
		}
	}
	fix(c.root)

	ns.log.mu.Lock()
	c.log.entries = append([]LogEntry(nil), ns.log.entries...)
	c.log.dropped = ns.log.dropped
	ns.log.mu.Unlock()
	return c
}

// cloneNode copies n's tree shape and its layer list (still the original
// layer pointers — cloneLayer replaces them in a second pass), recording
// old→new in remap.
func cloneNode(n *node, remap map[*node]*node) *node {
	n.mu.RLock()
	defer n.mu.RUnlock()
	cp := &node{
		children: make(map[string]*node, len(n.children)),
		layers:   append([]*layer(nil), n.layers...),
		pathHash: n.pathHash,
	}
	remap[n] = cp
	for name, ch := range n.children {
		cp.children[name] = cloneNode(ch, remap)
	}
	return cp
}

func cloneLayer(l *layer, c *Namespace, remap map[*node]*node) *layer {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := &layer{fs: l.fs, subpath: l.subpath, spec: l.spec, seq: l.seq, ro: l.ro}
	if nb, ok := l.fs.(namespaceBound); ok {
		// Re-resolve lazily against the clone; a resolved root from the
		// original would still point at the original's namespace.
		cp.fs = nb.cloneFor(c)
		return cp
	}
	cp.resolved = remapFile(l.resolved, c, remap)
	return cp
}

// remapFile rebuilds a resolved layer root so any reference into the
// original's bind tree points at the clone's matching node instead. A
// file inside a bound filesystem (nsFile.real) has no tree reference and
// is shared as is.
func remapFile(f server.File, c *Namespace, remap map[*node]*node) server.File {
	switch x := f.(type) {
	case *nsFile:
		if x.n != nil {
			if nn, ok := remap[x.n]; ok {
				return &nsFile{ns: c, n: nn}
			}
		}
		return x
	case roFile:
		return roFile{remapFile(x.File, c, remap)}
	default:
		return f
	}
}
