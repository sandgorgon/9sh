package eval

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biGlob implements `glob(pattern)`: pattern is a full namespace path
// whose final segment may contain glob metacharacters (*, ?, [...] —
// path.Match's own syntax), e.g. glob("/local/*.go"). Namespace-native
// by design, not filepath.Glob against the real OS filesystem — kyu's
// whole namespace/bind/checkout/env-var story has consistently avoided
// raw-OS operations in favor of walking the namespace itself (see
// SetCwd's and /env's own doc comments for the same reasoning), and
// most namespace subtrees (/jobs, /env, a remote /n/host mount) have no
// corresponding real directory to search with filepath.Glob at all.
//
// Deliberately explicit-path-only, not resolved against any "current
// directory": kyu has no notion of a current position within the
// namespace (cd's cwd is a raw OS path for subprocess Dir, a completely
// separate address space — see Env.SetCwd's doc comment), and inventing
// one as Env-global state would carry the same cross-pane surprise
// cd's cwd already does (every kyu-repl pane in a TUI session shares
// one root Env), just for a second axis. A scoped construct would be
// the right fix if this is ever wanted, not a quick bolt-on here.
//
// Matches only within the named directory — no recursive `**`. Like
// cd/checkout, it needs the calling Env's namespace, so it's registered
// specially in NewGlobalEnv rather than living in the plain builtins
// map. Results are sorted and returned as a List of Path, immediately
// pipeable into where/select/etc. like any other kyu list.
func biGlob(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("glob: expected 1 argument (a pattern), got %d", len(args))
	}
	pattern, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("glob: expected a string pattern, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("glob: no namespace attached to this environment")
	}

	dirPath, base := path.Split(string(pattern))
	dirPath = strings.TrimSuffix(dirPath, "/")
	if dirPath == "" {
		dirPath = "/"
	}
	if base == "" {
		return nil, fmt.Errorf("glob: pattern %q has no final segment to match", pattern)
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	dir, err := walkAll(ctx, root, splitPath(dirPath))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("glob: %v", err)}, nil
	}
	entries, err := ns.ReadDirEntries(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}

	var matches []string
	for _, ent := range entries {
		ok, err := path.Match(base, ent.Name)
		if err != nil {
			return nil, fmt.Errorf("glob: %w", err)
		}
		if ok {
			matches = append(matches, ent.Name)
		}
	}
	sort.Strings(matches)

	out := make([]value.Value, len(matches))
	for i, name := range matches {
		if dirPath == "/" {
			out[i] = value.Path("/" + name)
		} else {
			out[i] = value.Path(dirPath + "/" + name)
		}
	}
	return value.NewList(out), nil
}
