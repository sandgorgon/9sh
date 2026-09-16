package eval

import (
	"context"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biRmdir implements `rmdir(path)` / `rmdir(path, recursive)`: removes
// one namespace directory — walkAll to the target the same way biRm
// does, but requiring (rather than rejecting) a directory Qid.
//
// With no second argument (or recursive == false), this only removes
// an empty directory: the same generic server.File.Remove(ctx) every
// backend already implements, a thin os.Remove wrapper on dirfs, so a
// non-empty directory surfaces as an ordinary ErrorVal from the real
// filesystem (ENOTEMPTY) rather than a special case here. Passing
// recursive == true instead walks and removes every entry beneath path
// first (removeTree, tree.go) — the explicit opt-in mirrors `rm -r`
// requiring its own flag rather than plain `rm`/`rmdir` recursing by
// default, so a typo'd path can't silently take an entire subtree with
// it.
func biRmdir(env *Env, args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("rmdir: expected 1 argument (a path) or 2 (a path, a recursive flag), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("rmdir: expected a path, got %s", args[0].Kind())
	}
	recursive := false
	if len(args) == 2 {
		b, ok := args[1].(value.Bool)
		if !ok {
			return nil, fmt.Errorf("rmdir: second argument must be a bool (recursive), got %s", args[1].Kind())
		}
		recursive = bool(b)
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("rmdir: no namespace attached to this environment")
	}
	parts := splitPath(string(p))
	if len(parts) == 0 {
		return value.ErrorVal{Msg: "rmdir: cannot remove the namespace root"}, nil
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, parts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("rmdir: %s: %v", p, err)}, nil
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("rmdir: %s: %v", p, err)}, nil
	}
	if !st.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("rmdir: %s: not a directory (use rm instead)", p)}, nil
	}
	if recursive {
		if err := removeTree(ctx, f); err != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("rmdir: %s: %v", p, err)}, nil
		}
		return value.Null{}, nil
	}
	if err := f.Remove(ctx); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("rmdir: %s: %v", p, err)}, nil
	}
	return value.Null{}, nil
}
