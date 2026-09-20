package ns

import (
	"context"
	"encoding/binary"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

// Plan 9's Dir has a dev field naming which mounted server a file came
// from — the kernel's mount driver stamps it on everything it serves, and
// `ls -l` prints it. The 9P spec has servers leave it zero because it is
// the client-side "kernel" that knows. This namespace is that kernel: every
// file reached through a layer that wraps a real filesystem (BindFS, or
// kyu's `bind dial(...)`/`dir(...)`) carries that layer's id in Stat.Dev,
// in a Stat and in the entries of a directory read alike.
//
// Two rules follow Plan 9's own:
//   - bind vs. mount: a layer made by binding an existing namespace path
//     (BindPath) has no id of its own — bind doesn't change which server a
//     file lives on, so its files keep the id of the layer that really
//     serves them. Its dev() is 0, meaning "pass through".
//   - the stamp is per-namespace: the ids mean nothing outside this
//     process, so every export (see Unstamped) sends Dev as zero, and a
//     peer dialing us stamps its own.
//
// Synthetic tree nodes (the directories that exist only because something
// is bound below them) are not served by any layer: dev 0.

// dev is the id stamped onto files this layer serves: its bind sequence
// number, which is never reused (unbind then rebind gets a new one, so a
// stale id can't alias a later layer). 0 for a layer that binds an
// existing namespace path — see the rules above.
func (l *layer) dev() uint32 {
	if l.fs == nil {
		return 0
	}
	return uint32(l.seq)
}

// setDirDev overwrites the dev field of every whole Stat entry in b, a
// (possibly partial) directory read. The field sits at a fixed offset in
// both plain 9P2000 and 9P2000.u entries — size[2] type[2] dev[4] — so it
// is patched in place without decoding: byte lengths, and so the offsets a
// client resumes reading from, are unchanged. A trailing partial entry is
// left alone.
func setDirDev(b []byte, dev uint32) {
	const devOff = 4
	for len(b) >= 2 {
		total := 2 + int(binary.LittleEndian.Uint16(b))
		if total > len(b) || total < devOff+4 {
			return
		}
		binary.LittleEndian.PutUint32(b[devOff:], dev)
		b = b[total:]
	}
}

// Unstamped wraps fs so nothing it serves carries a dev: Stat and
// directory reads report zero, as a 9P server should. Use it on anything
// exported to another process (remote.Listen/ListenUnix do) — the ids are
// this namespace's own.
func Unstamped(fs server.FileSystem) server.FileSystem { return unstampedFS{fs} }

type unstampedFS struct{ fs server.FileSystem }

func (u unstampedFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	f, err := u.fs.Attach(ctx, uname, aname)
	if err != nil {
		return nil, err
	}
	return unstampedFile{f}, nil
}

type unstampedFile struct{ server.File }

func (f unstampedFile) Stat(ctx context.Context) (p9.Stat, error) {
	st, err := f.File.Stat(ctx)
	st.Dev = 0
	return st, err
}

func (f unstampedFile) Walk(ctx context.Context, name string) (server.File, error) {
	child, err := f.File.Walk(ctx, name)
	if err != nil {
		return nil, err
	}
	return unstampedFile{child}, nil
}

func (f unstampedFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	child, err := f.File.Create(ctx, name, perm, mode)
	if err != nil {
		return nil, err
	}
	return unstampedFile{child}, nil
}

func (f unstampedFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	n, err := f.File.Read(ctx, offset, p)
	if n > 0 && f.File.Qid().IsDir() {
		setDirDev(p[:n], 0)
	}
	return n, err
}
