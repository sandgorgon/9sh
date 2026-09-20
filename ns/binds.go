package ns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

// Bind is one layer of one bind point, as reported by Binds.
type Bind struct {
	Dst string `json:"dst"`
	// Src is the kyu source expression that produced this layer
	// ("/local", `dial("h:1")`), or "" for a Go-bootstrap bind (/jobs,
	// /local, ...) that has no kyu spelling.
	Src string `json:"src"`
	// Disp is the canonical disposition that reproduces the current
	// state when the layers of one Dst are replayed in order: "replace"
	// for the first, "after" for the rest. It is not the disposition the
	// layer was originally bound with — that isn't recoverable once a
	// later bind has spliced around it, and doesn't matter for state.
	Disp string `json:"disp"`
	// RO is whether the layer refuses writes (bound with the ro flag).
	RO bool `json:"ro"`
}

// Binds reports every layer currently bound anywhere in the namespace,
// ordered by when its bind point first received a layer (so replaying
// the list top to bottom respects "this bind's source was made by an
// earlier bind"), and by union order within a single Dst.
func (ns *Namespace) Binds() []Bind {
	type point struct {
		dst    string
		layers []*layer
		first  uint64
	}
	var points []point
	var walk func(n *node, path string)
	walk = func(n *node, path string) {
		n.mu.RLock()
		layers := append([]*layer(nil), n.layers...)
		names := make([]string, 0, len(n.children))
		children := make(map[string]*node, len(n.children))
		for name, c := range n.children {
			names = append(names, name)
			children[name] = c
		}
		n.mu.RUnlock()

		if len(layers) > 0 {
			first := layers[0].seq
			for _, l := range layers[1:] {
				first = min(first, l.seq)
			}
			dst := path
			if dst == "" {
				dst = "/"
			}
			points = append(points, point{dst: dst, layers: layers, first: first})
		}
		sort.Strings(names)
		for _, name := range names {
			walk(children[name], path+"/"+name)
		}
	}
	walk(ns.root, "")
	sort.SliceStable(points, func(i, j int) bool { return points[i].first < points[j].first })

	var out []Bind
	for _, p := range points {
		for i, l := range p.layers {
			disp := "after"
			if i == 0 {
				disp = "replace"
			}
			out = append(out, Bind{Dst: p.dst, Src: l.spec, Disp: disp, RO: l.ro})
		}
	}
	return out
}

// FormatBinds renders binds as replayable kyu, one `bind` per layer. A
// layer with no kyu spelling is emitted as a comment so the file still
// shows what's there without pretending it can be sourced back.
func FormatBinds(binds []Bind) string {
	var b strings.Builder
	for _, x := range binds {
		src, comment := x.Src, ""
		if src == "" {
			src, comment = "<builtin>", "# "
		}
		fmt.Fprintf(&b, "%sbind %s, %s%s\n", comment, src, x.Dst, bindSuffix(x.Disp, x.RO))
	}
	return b.String()
}

// BindsFS is a read-only synthetic filesystem describing a namespace's
// own binds, meant to be bound at /ns:
//
//	/ns/binds       replayable kyu, one `bind` per layer (see FormatBinds)
//	/ns/binds.json  the same layers as a JSON array of Bind, for tools
//	/ns/log         every bind/unbind as replayable kyu, with the
//	                original dispositions and times (see FormatLog)
//	/ns/log.json    the same as {"dropped": N, "entries": [LogEntry]}
//
// Plan 9's /proc/$pid/ns, split in two the way /jobs' status is JSON:
// the text is for people and `source`, the JSON is what binds() reads.
// Content is snapshotted at Open, so a reader sees one consistent view
// even if a bind happens mid-read.
type BindsFS struct {
	ns *Namespace
}

func NewBindsFS(ns *Namespace) *BindsFS { return &BindsFS{ns: ns} }

// cloneFor implements namespaceBound: a cloned namespace's /ns reports
// the clone, not the namespace it was copied from.
func (fs *BindsFS) cloneFor(ns *Namespace) server.FileSystem { return NewBindsFS(ns) }

func (fs *BindsFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	return &bindsRoot{fs: fs}, nil
}

var (
	qidBindsRoot = hashPath("/ns")
	bindsOrder   = []string{"binds", "binds.json", "log", "log.json"}
	bindsFiles   = func() map[string]uint64 {
		m := map[string]uint64{}
		for _, name := range bindsOrder {
			m[name] = hashPath("/ns/" + name)
		}
		return m
	}()
)

type bindsRoot struct{ fs *BindsFS }

func (f *bindsRoot) Qid() p9.Qid { return p9.Qid{Type: p9.QTDIR, Path: qidBindsRoot} }
func (f *bindsRoot) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: f.Qid(), Mode: p9.DMDIR | 0555, Name: "/", Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}
func (f *bindsRoot) WStat(ctx context.Context, st p9.Stat) error {
	return errors.New("nsfs: cannot modify metadata")
}
func (f *bindsRoot) Walk(ctx context.Context, name string) (server.File, error) {
	if name == ".." {
		return f, nil
	}
	if _, ok := bindsFiles[name]; !ok {
		return nil, fmt.Errorf("nsfs: %s: no such file", name)
	}
	return &bindsFile{fs: f.fs, name: name}, nil
}
func (f *bindsRoot) Open(ctx context.Context, mode p9.Mode) error { return nil }
func (f *bindsRoot) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, errors.New("nsfs: read-only")
}
func (f *bindsRoot) Remove(ctx context.Context) error { return errors.New("nsfs: read-only") }
func (f *bindsRoot) Close() error                     { return nil }
func (f *bindsRoot) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("nsfs: read-only")
}
func (f *bindsRoot) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	entries := make([]p9.Stat, 0, len(bindsOrder))
	for _, name := range bindsOrder {
		entries = append(entries, p9.Stat{
			Qid: p9.Qid{Type: p9.QTFILE, Path: bindsFiles[name]}, Mode: 0444, Name: name,
			Uid: sysUser, Gid: sysUser, Muid: sysUser,
		})
	}
	return server.MarshalDir(entries, offset, p)
}

type bindsFile struct {
	fs   *BindsFS
	name string
	snap []byte // set by Open; nil until then
}

func (f *bindsFile) Qid() p9.Qid { return p9.Qid{Type: p9.QTFILE, Path: bindsFiles[f.name]} }

func (f *bindsFile) content() []byte {
	switch f.name {
	case "log", "log.json":
		entries, dropped := f.fs.ns.Log()
		if f.name == "log" {
			return []byte(FormatLog(entries, dropped))
		}
		if entries == nil {
			entries = []LogEntry{}
		}
		return jsonLine(struct {
			Dropped uint64     `json:"dropped"`
			Entries []LogEntry `json:"entries"`
		}{dropped, entries})
	}
	binds := f.fs.ns.Binds()
	if f.name == "binds" {
		return []byte(FormatBinds(binds))
	}
	if binds == nil {
		binds = []Bind{}
	}
	return jsonLine(binds)
}

func jsonLine(v any) []byte {
	b, _ := json.Marshal(v) // plain strings and ints; can't fail
	return append(b, '\n')
}

func (f *bindsFile) Stat(ctx context.Context) (p9.Stat, error) {
	data := f.snap
	if data == nil {
		data = f.content()
	}
	return p9.Stat{Qid: f.Qid(), Mode: 0444, Name: f.name, Length: uint64(len(data)), Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}
func (f *bindsFile) WStat(ctx context.Context, st p9.Stat) error {
	return fmt.Errorf("nsfs: %s: cannot modify metadata", f.name)
}
func (f *bindsFile) Walk(ctx context.Context, name string) (server.File, error) {
	return nil, fmt.Errorf("nsfs: %s: not a directory", f.name)
}
func (f *bindsFile) Open(ctx context.Context, mode p9.Mode) error {
	if mode&3 != p9.OREAD {
		return fmt.Errorf("nsfs: %s: read-only", f.name)
	}
	f.snap = f.content()
	return nil
}
func (f *bindsFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, fmt.Errorf("nsfs: %s: not a directory", f.name)
}
func (f *bindsFile) Remove(ctx context.Context) error {
	return fmt.Errorf("nsfs: %s: cannot remove", f.name)
}
func (f *bindsFile) Close() error { return nil }
func (f *bindsFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, fmt.Errorf("nsfs: %s: read-only", f.name)
}
func (f *bindsFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	data := f.snap
	if data == nil {
		data = f.content()
	}
	if offset >= int64(len(data)) {
		return 0, io.EOF
	}
	return copy(p, data[offset:]), nil
}

// Resolution reports how one path resolves through the bind tree — which
// bind point and which layer of it would serve a Walk to that path.
type Resolution struct {
	Path string
	// Kind is "layer" when the path lands inside a bound filesystem,
	// "bindpoint" when it is exactly a bind point that has layers (a
	// union directory), or "tree" when it is a purely synthetic
	// directory of the bind tree (like /n when only /n/host is bound).
	Kind string
	// Dst is the bind point where resolution left the explicit tree
	// (Kind "layer"), or the path itself (the other kinds).
	Dst string
	// Src is the serving layer's source expression, "" for a bootstrap
	// bind or when there is no layer. Layer is its 0-based position in
	// Dst's union order, -1 when there is none. Layers is how many
	// layers Dst has.
	Src    string
	Layer  int
	Layers int
	// Inner is the path within the serving layer ("/" is its root); ""
	// unless Kind is "layer".
	Inner string
	// RO is whether the serving layer is read-only; for a "bindpoint",
	// whether its first layer is (the one a create would go to).
	RO bool
}

// Resolve answers "what serves this path", by walking exactly the way
// nsFile.Walk does: explicit tree children win over layers, and at the
// first node with no such child the layers are tried in union order —
// the first one whose Walk of the next name succeeds serves the rest of
// the path, with no fallback to a later layer if a deeper element is
// missing there. Reporting the same answer a real Walk would give is
// the point, so this deliberately does not try to be smarter (a union
// directory's *listing* merges layers; a Walk into it does not).
func (ns *Namespace) Resolve(ctx context.Context, path string) (Resolution, error) {
	parts := splitPath(path)
	res := Resolution{Path: "/" + strings.Join(parts, "/"), Layer: -1}
	n := ns.root
	cur := ""
	for i, name := range parts {
		if name == ".." {
			return res, errors.New("ns: '..' is not supported at a namespace bind point")
		}
		n.mu.RLock()
		child, hasChild := n.children[name]
		layers := n.layers
		n.mu.RUnlock()
		if hasChild {
			n = child
			cur += "/" + name
			continue
		}
		dst := cur
		if dst == "" {
			dst = "/"
		}
		var lastErr error
		for li, l := range layers {
			root, err := l.root(ctx)
			if err != nil {
				lastErr = err
				continue
			}
			f, err := root.Walk(ctx, name)
			if err != nil {
				lastErr = err
				continue
			}
			for _, p := range parts[i+1:] {
				if f, err = f.Walk(ctx, p); err != nil {
					return res, fmt.Errorf("ns: %s: %w", res.Path, err)
				}
			}
			res.Kind, res.Dst, res.Src, res.RO = "layer", dst, l.spec, l.ro
			res.Layer, res.Layers = li, len(layers)
			res.Inner = "/" + strings.Join(parts[i:], "/")
			return res, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("ns: %s: no such file", name)
		}
		return res, fmt.Errorf("ns: %s: %w", res.Path, lastErr)
	}
	n.mu.RLock()
	res.Layers = len(n.layers)
	if res.Layers > 0 {
		// No single serving layer here; a create at a bind point goes to
		// the first, so that is the one whose read-only flag matters.
		res.RO = n.layers[0].ro
	}
	n.mu.RUnlock()
	res.Kind, res.Dst = "tree", res.Path
	if res.Layers > 0 {
		res.Kind = "bindpoint"
	}
	return res, nil
}
