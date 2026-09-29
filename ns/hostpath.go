package ns

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/9p/server"
)

// HostDirer is implemented by a filesystem that is a view of one real
// host directory. It is how HostPath finds an OS path: dirfs.FS keeps its
// root private, so 9sh's own directory binds go through NewDirFS, which
// remembers it.
type HostDirer interface {
	HostDir() string
}

type hostDirFS struct {
	*dirfs.FS
	dir string
}

func (h hostDirFS) HostDir() string { return h.dir }

// NewDirFS is dirfs.New that also records the absolute host directory, so
// a path served by the result can be mapped back by HostPath. dir()
// and the /local bootstrap bind use it.
func NewDirFS(dir string) (server.FileSystem, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	fs, err := dirfs.New(abs)
	if err != nil {
		return nil, err
	}
	return hostDirFS{FS: fs, dir: abs}, nil
}

// maxHostPathHops bounds following a path-bind (bind /local, /work)
// back to its source, so a pathological bind loop can't spin forever.
const maxHostPathHops = 32

// HostPath maps a namespace path to the real OS path that backs it: the
// directory of the serving dir()/bootstrap layer plus the path within it.
// A path-bind (bind /local, /work) is followed to its source. For a bind
// point that is exactly a layer's root, the first layer answers.
//
// It fails, rather than guessing, for anything with no OS path: a remote
// (dial) layer, /jobs, /env, a synthetic directory, or a union-expression
// bind (bind a + b, /x) whose several sources have no single answer.
// Like Resolve it follows only the first layer that serves the path, so
// it names the file a real Walk reads.
func (ns *Namespace) HostPath(ctx context.Context, path string) (string, error) {
	for hop := 0; hop < maxHostPathHops; hop++ {
		res, l, err := ns.resolve(ctx, path)
		if err != nil {
			return "", err
		}
		if l == nil {
			return "", fmt.Errorf("ns: %s: synthetic directory, no host path", res.Path)
		}
		inner := res.Inner
		if res.Kind == "bindpoint" {
			inner = "/"
		}
		if h, ok := l.fs.(HostDirer); ok {
			return filepath.Join(append([]string{h.HostDir()}, append(l.subpath, splitPath(inner)...)...)...), nil
		}
		if l.fs == nil && l.spec != "" && !strings.Contains(l.spec, " + ") && strings.HasPrefix(l.spec, "/") {
			// A path bind: its layer is the source path's own node.
			path = filepath.Join(l.spec, inner)
			continue
		}
		return "", fmt.Errorf("ns: %s: served by %s, which has no host path", res.Path, describeLayer(l))
	}
	return "", fmt.Errorf("ns: %s: bind chain too deep", path)
}

func describeLayer(l *layer) string {
	if l.spec != "" {
		return l.spec
	}
	return "a built-in filesystem"
}
