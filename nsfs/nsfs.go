// Package nsfs holds the shell-specific views of a namespace: the /ns
// introspection filesystem, the kyu text that replays a namespace's binds
// and bind log, and the constructor that gives a namespace 9sh's identity.
// The namespace itself lives in github.com/sandgorgon/9p/ns.
package nsfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/ns"
	"github.com/sandgorgon/9p/server"
)

// User is the owner 9sh's namespaces report for their synthetic
// directories, and the uname they attach bound filesystems with.
const User = "9sh"

// New returns an empty namespace with 9sh's identity.
func New() *ns.Namespace { return ns.New(ns.WithUser(User)) }

func hashPath(p string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(p))
	return h.Sum64()
}

// FormatBinds renders binds as replayable kyu, one `bind` per layer. A
// layer with no kyu spelling is emitted as a comment so the file still
// shows what's there without pretending it can be sourced back.
func FormatBinds(binds []ns.Bind) string {
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
	ns *ns.Namespace
}

func NewBindsFS(n *ns.Namespace) *BindsFS { return &BindsFS{ns: n} }

// CloneFor implements ns.NamespaceBound: a cloned namespace's /ns reports
// the clone, not the namespace it was copied from.
func (fs *BindsFS) CloneFor(n *ns.Namespace) server.FileSystem { return NewBindsFS(n) }

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
	return p9.Stat{Qid: f.Qid(), Mode: p9.DMDIR | 0555, Name: "/", Uid: User, Gid: User, Muid: User}, nil
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
			Uid: User, Gid: User, Muid: User,
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
			entries = []ns.LogEntry{}
		}
		return jsonLine(struct {
			Dropped uint64        `json:"dropped"`
			Entries []ns.LogEntry `json:"entries"`
		}{dropped, entries})
	}
	binds := f.fs.ns.Binds()
	if f.name == "binds" {
		return []byte(FormatBinds(binds))
	}
	if binds == nil {
		binds = []ns.Bind{}
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
	return p9.Stat{Qid: f.Qid(), Mode: 0444, Name: f.name, Length: uint64(len(data)), Uid: User, Gid: User, Muid: User}, nil
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

// FormatLog renders a log as replayable kyu, one statement per entry
// with the time as a trailing comment, so `source(/ns/log)` re-runs the
// history with each bind's original disposition. Entries with no kyu
// spelling (bootstrap binds) are whole-line comments, like FormatBinds.
func FormatLog(entries []ns.LogEntry, dropped uint64) string {
	var b strings.Builder
	if dropped > 0 {
		fmt.Fprintf(&b, "# %d earlier entries dropped\n", dropped)
	}
	for _, e := range entries {
		switch {
		case e.Op == "unbind":
			fmt.Fprintf(&b, "unbind %s", e.Dst)
		case e.Src == "":
			fmt.Fprintf(&b, "# bind <builtin>, %s%s", e.Dst, bindSuffix(e.Disp, e.RO))
		default:
			fmt.Fprintf(&b, "bind %s, %s%s", e.Src, e.Dst, bindSuffix(e.Disp, e.RO))
		}
		fmt.Fprintf(&b, "  # %s\n", e.Time)
	}
	return b.String()
}

// bindSuffix is the optional ", after" / ", ro" tail of a bind statement:
// replace is the default disposition and is never spelled out.
func bindSuffix(disp string, ro bool) string {
	var s string
	if disp != "" && disp != "replace" {
		s += ", " + disp
	}
	if ro {
		s += ", ro"
	}
	return s
}
