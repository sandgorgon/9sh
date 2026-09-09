package eval

import (
	"context"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biRm implements `rm(path)`: removes one namespace file — read side
// mirrors biCp/biCat (walkAll to the target), the op itself is
// server.File.Remove(ctx), already implemented by every backend a
// namespace can bind (ns/file.go, remote/client_fs.go, remote/auth_fs.go,
// job/fs.go) for exactly this, just never exposed to kyu directly before.
//
// v1 scope, deliberately narrow like cp's own: path must be a regular
// file, not a directory (recursive namespace directory removal needs
// namespace-directory-walk-and-remove semantics nothing in this codebase
// has yet). Any failure is an ordinary ErrorVal, not a hard error,
// matching cp/stat/glob's convention.
func biRm(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("rm: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("rm: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("rm: no namespace attached to this environment")
	}
	parts := splitPath(string(p))
	if len(parts) == 0 {
		return value.ErrorVal{Msg: "rm: cannot remove the namespace root"}, nil
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, parts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("rm: %s: %v", p, err)}, nil
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("rm: %s: %v", p, err)}, nil
	}
	if st.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("rm: %s: is a directory (directory removal not yet supported)", p)}, nil
	}
	if err := f.Remove(ctx); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("rm: %s: %v", p, err)}, nil
	}
	return value.Null{}, nil
}
