package eval

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// statRecord builds the Record shape stat(path) and ls(pattern) both
// return, from st (as returned by server.File.Stat or
// ns.ReadDirEntries) and fullPath, the complete namespace path st
// describes (st.Name alone is only the base name — ns.ReadDirEntries
// never saw the directory it came from).
//
// mtime/atime are Unix epoch seconds (p9.Stat's own Mtime/Atime are
// already exactly that, as a uint32) stored as a plain Int, not a
// dedicated kyu time value — kyu doesn't have one yet, only Duration
// (an elapsed span, not a point in time); Int keeps these
// sortable/comparable today without inventing a type for one builtin.
func statRecord(fullPath string, st p9.Stat) *value.Record {
	r := value.NewRecord()
	r.Set("path", value.Path(fullPath))
	r.Set("name", value.String(st.Name))
	r.Set("size", value.Int(st.Length))
	r.Set("is_dir", value.Bool(st.Mode.IsDir()))
	r.Set("mode", value.String(permString(st.Mode)))
	r.Set("mtime", value.Int(st.Mtime))
	r.Set("atime", value.Int(st.Atime))
	r.Set("uid", value.String(st.Uid))
	r.Set("gid", value.String(st.Gid))
	return r
}

// permString renders m's low 9 permission bits (p9.DMPerm = 0777,
// laid out exactly like a Unix mode: owner/group/other rwx) as the
// familiar "rwxr-xr-x" form ls -l shows.
func permString(m p9.Mode) string {
	const bits = "rwxrwxrwx"
	b := make([]byte, 9)
	for i := range b {
		if m&(1<<uint(8-i)) != 0 {
			b[i] = bits[i]
		} else {
			b[i] = '-'
		}
	}
	return string(b)
}

// biStat implements `stat(path)`: one namespace Path in, one Record of
// its real metadata out. This data was always one Stat(ctx) call away
// — checkout() already fetches it for its own internal use (see
// checkout.go) — just never exposed to kyu directly before. Like
// glob/checkout, needs the calling Env's namespace, so it's registered
// specially in NewGlobalEnv rather than the plain builtins map.
//
// A path that doesn't resolve in the namespace is an ordinary in-stream
// ErrorVal, matching glob's own convention for a bad directory rather
// than a hard Go-level error.
func biStat(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("stat: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("stat: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("stat: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, splitPath(string(p)))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("stat: %v", err)}, nil
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("stat: %v", err)}, nil
	}
	return statRecord(string(p), st), nil
}

// biLs implements `ls(pattern)`: glob(pattern)'s richer sibling — same
// directory-plus-final-segment-pattern matching (see biGlob's own doc
// comment for why that's namespace-native, explicit-path-only, no
// recursive **) — but returning a Table (List of Record, via
// statRecord) instead of bare Paths. The metadata is free: the
// directory read this shares with glob (ns.ReadDirEntries) already
// returns a full p9.Stat per entry; glob just discards everything but
// the name. glob(pattern) itself is left alone, not widened to match
// this — plenty of real use is `glob(...) | each { |f| checkout(f, ...) }`
// -shaped, where the element needs to stay a plain Path.
func biLs(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("ls: expected 1 argument (a pattern), got %d", len(args))
	}
	pattern, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("ls: expected a string pattern, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("ls: no namespace attached to this environment")
	}

	dirPath, base := path.Split(string(pattern))
	dirPath = strings.TrimSuffix(dirPath, "/")
	if dirPath == "" {
		dirPath = "/"
	}
	if base == "" {
		return nil, fmt.Errorf("ls: pattern %q has no final segment to match", pattern)
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	dir, err := walkAll(ctx, root, splitPath(dirPath))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("ls: %v", err)}, nil
	}
	entries, err := ns.ReadDirEntries(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("ls: %w", err)
	}

	var matches []p9.Stat
	for _, ent := range entries {
		ok, err := path.Match(base, ent.Name)
		if err != nil {
			return nil, fmt.Errorf("ls: %w", err)
		}
		if ok {
			matches = append(matches, ent)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })

	out := make([]value.Value, len(matches))
	for i, st := range matches {
		full := dirPath + "/" + st.Name
		if dirPath == "/" {
			full = "/" + st.Name
		}
		out[i] = statRecord(full, st)
	}
	return value.NewList(out), nil
}
