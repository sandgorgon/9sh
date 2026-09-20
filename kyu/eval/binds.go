package eval

import (
	"context"
	"encoding/json"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biBinds implements `binds()`: every layer bound in the namespace, as a
// Table of Records (dst, src, disp, ro, dev) — the structured view of /ns/binds
// the way ps() is of /jobs. Reads /ns/binds.json through the namespace,
// never the Namespace value directly, so it works on anything that
// serves a /ns the same way. src is Null for a bootstrap bind with no
// kyu spelling (/jobs, /env, ...).
//
// disp is the canonical replay disposition, not the one a layer was
// originally bound with — see ns.Bind. dev is the id ls/stat stamp on the
// layer's files, 0 for a layer that binds an existing path (see ns/dev.go).
//
// With a Path argument, only layers bound at that path or anywhere
// beneath it are returned (`binds(/n)` is every remote mount; `/nfs`
// doesn't match — whole path segments only). It's a filter on the same
// list, so an unbound or never-bound path is an empty List, not an
// error.
func biBinds(env *Env, args []value.Value) (value.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("binds: expected at most 1 argument (a path), got %d", len(args))
	}
	var under []string
	if len(args) == 1 {
		p, ok := args[0].(value.Path)
		if !ok {
			return nil, fmt.Errorf("binds: expected a path, got %s", args[0].Kind())
		}
		under = splitPath(string(p))
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("binds: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := openFile(ctx, root, p9.OREAD, "ns", "binds.json")
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("binds: %v", err)}, nil
	}
	defer f.Close()
	b, err := readAllFile(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("binds: %w", err)
	}
	var binds []ns.Bind
	if err := json.Unmarshal(b, &binds); err != nil {
		return nil, fmt.Errorf("binds: %w", err)
	}
	out := make([]value.Value, 0, len(binds))
	for _, x := range binds {
		if !hasPathPrefix(splitPath(x.Dst), under) {
			continue
		}
		r := value.NewRecord()
		r.Set("dst", value.Path(x.Dst))
		if x.Src == "" {
			r.Set("src", value.Null{})
		} else {
			r.Set("src", value.String(x.Src))
		}
		r.Set("disp", value.String(x.Disp))
		r.Set("ro", value.Bool(x.RO))
		r.Set("dev", value.Int(x.Dev))
		out = append(out, r)
	}
	return value.NewList(out), nil
}

// hasPathPrefix reports whether path starts with every element of
// prefix, by whole elements (so /nfs is not under /n). An empty prefix
// matches everything.
func hasPathPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}
