package eval

import (
	"context"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biCp implements `cp(src, dst)`: reads src's whole content and writes
// it to dst, both ordinary namespace Paths — read side mirrors biCat/
// biGetenv (walkAll, Stat to reject a directory, Open+read), write
// side reuses resolveOrCreate plus the Open(OWRITE|OTRUNC)+Write
// pattern biSetenv/checkout.go's writeBackFile already established, so
// dst may be an existing file (overwritten) or a brand-new one at an
// already-existing directory level.
//
// Namespace-native payoff: src and dst can be on opposite sides of a
// bind — a real OS file, a remote /n/host mount, anything — with no
// separate transfer protocol, since they're already the same namespace
// once bound (see bind/dial's own doc comments). Real cp/scp can't
// reach a namespace-only endpoint (/jobs, /env) at all without staging
// through checkout() first.
//
// v1 scope, deliberately narrow like checkout's own write-back: src
// must be a regular file, not a directory (recursive tree copy would
// need namespace directory creation, which nothing in this codebase
// does yet — see resolveOrCreate's own "doesn't create new
// subdirectories" doc comment), and dst may not itself be an existing
// directory (name the destination file explicitly). Both surface as an
// ordinary ErrorVal, not a hard error, matching stat/glob/ls's own
// convention for "the path you gave doesn't fit what this builtin
// does" rather than a malformed-call error.
func biCp(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("cp: expected 2 arguments (source path, destination path), got %d", len(args))
	}
	src, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("cp: first argument must be a path, got %s", args[0].Kind())
	}
	dst, ok := args[1].(value.Path)
	if !ok {
		return nil, fmt.Errorf("cp: second argument must be a path, got %s", args[1].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("cp: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}

	srcFile, err := walkAll(ctx, root, splitPath(string(src)))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}
	srcSt, err := srcFile.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}
	if srcSt.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: is a directory (directory copy not yet supported)", src)}, nil
	}
	if err := srcFile.Open(ctx, p9.OREAD); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}
	content, err := readAllFile(ctx, srcFile)
	srcFile.Close()
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}

	dstParts := splitPath(string(dst))
	if len(dstParts) == 0 {
		return value.ErrorVal{Msg: "cp: destination cannot be the namespace root"}, nil
	}
	dstFile, err := resolveOrCreate(ctx, root, dstParts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", dst, err)}, nil
	}
	if dstSt, err := dstFile.Stat(ctx); err == nil && dstSt.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: is a directory (copying into a directory not yet supported -- name the destination file explicitly)", dst)}, nil
	}
	if err := dstFile.Open(ctx, p9.OWRITE|p9.OTRUNC); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", dst, err)}, nil
	}
	defer dstFile.Close()
	if _, err := dstFile.Write(ctx, 0, content); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", dst, err)}, nil
	}
	return value.Null{}, nil
}
