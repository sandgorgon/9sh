package eval

import (
	"context"
	"fmt"
	"path"
	"sort"

	"github.com/sandgorgon/9p/server"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biFind implements `find(dir, pattern)`: like glob(pattern), but walks
// every subdirectory beneath dir recursively, matching pattern
// (path.Match syntax) against each entry's base name at every depth —
// the recursive walk glob deliberately doesn't do (see biGlob's own doc
// comment: no **, single directory only). Kept as a separate
// two-argument builtin rather than widening glob's single-pattern-string
// syntax with a ** convention, per this codebase's "prefer a builtin
// over new grammar" principle — an explicit directory argument plus an
// ordinary path.Match pattern needs no new mini-syntax at all.
//
// Namespace-native for the same reason glob/ls/stat are: most of what's
// worth searching recursively (/jobs, a remote /n/host mount) has no
// real OS path to hand filepath.WalkDir.
//
// Both directories and files are eligible to match pattern (same as
// Unix find's default), and a matching directory is still recursed
// into. No bind-cycle detection — matches glob's own scope, and a
// namespace union has no structural reason to cycle back on itself.
func biFind(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("find: expected 2 arguments (a directory path, a name pattern), got %d", len(args))
	}
	dirPath, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("find: first argument must be a path, got %s", args[0].Kind())
	}
	pattern, ok := args[1].(value.String)
	if !ok {
		return nil, fmt.Errorf("find: second argument must be a string pattern, got %s", args[1].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("find: no namespace attached to this environment")
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	dir, err := walkAll(ctx, root, splitPath(string(dirPath)))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("find: %v", err)}, nil
	}

	var matches []string
	if err := findWalk(ctx, dir, string(dirPath), string(pattern), &matches); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("find: %v", err)}, nil
	}
	sort.Strings(matches)

	out := make([]value.Value, len(matches))
	for i, m := range matches {
		out[i] = value.Path(m)
	}
	return value.NewList(out), nil
}

// findWalk recursively walks dir (whose full namespace path is dirPath,
// no trailing slash except at the root "/"), appending every descendant
// whose base name matches pattern to matches.
func findWalk(ctx context.Context, dir server.File, dirPath, pattern string, matches *[]string) error {
	entries, err := ns.ReadDirEntries(ctx, dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		full := dirPath + "/" + ent.Name
		if dirPath == "/" {
			full = "/" + ent.Name
		}
		ok, err := path.Match(pattern, ent.Name)
		if err != nil {
			return err
		}
		if ok {
			*matches = append(*matches, full)
		}
		if ent.Qid.IsDir() {
			child, err := dir.Walk(ctx, ent.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", full, err)
			}
			if err := findWalk(ctx, child, full, pattern, matches); err != nil {
				return err
			}
		}
	}
	return nil
}
