package eval

import (
	"context"
	"fmt"
	"strings"

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
// If dst is an existing directory, the copy goes into it, as with Unix
// cp: cp(/f, /d) writes /d/f (see resolveDstIntoDir). Everything below
// then applies to that resolved destination.
//
// src may also be a directory: the destination is then created fresh (via
// copyDirTree, tree.go) as a full recursive copy of src's tree, same
// shared primitive mv's cross-directory directory move uses. It must
// not already exist in that case — no merge-into-an-existing-directory
// semantics yet, so cp(/a, /d) with /d/a already present is refused,
// mirroring mkdir's own "already exists and isn't a directory" refusal.
// A directory can't be copied into itself (selfTransferMsg).
//
// For a regular-file src, dst may be an existing file (overwritten) or a
// new name; a resolved destination that is itself a directory is refused.
// Any failure is an ordinary ErrorVal, not a hard error, matching
// stat/glob/ls's own convention for "the path you gave doesn't fit what
// this builtin does" rather than a malformed-call error.
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

	srcParts := splitPath(string(src))
	srcFile, err := walkAll(ctx, root, srcParts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}
	srcSt, err := srcFile.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}
	dstParts := splitPath(string(dst))
	if len(dstParts) == 0 {
		return value.ErrorVal{Msg: "cp: destination cannot be the namespace root"}, nil
	}
	// An existing directory as dst means "into it", as with Unix cp: the
	// real destination is dst/<base name of src>. Errors below name it.
	dstParts, intoDir := resolveDstIntoDir(ctx, root, srcParts, dstParts)
	dst = value.Path("/" + strings.Join(dstParts, "/"))
	if msg := selfTransferMsg(srcSt.Qid.IsDir(), srcParts, dstParts, intoDir); msg != "" {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %s", dst, msg)}, nil
	}

	if srcSt.Qid.IsDir() {
		if _, err := walkAll(ctx, root, dstParts); err == nil {
			return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: already exists (directory copy needs a fresh destination)", dst)}, nil
		}
		dstParent, err := walkAll(ctx, root, dstParts[:len(dstParts)-1])
		if err != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", dst, err)}, nil
		}
		if err := copyDirTree(ctx, srcFile, dstParent, dstParts[len(dstParts)-1]); err != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", dst, err)}, nil
		}
		return value.Null{}, nil
	}
	if err := srcFile.Open(ctx, p9.OREAD); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}
	content, err := readAllFile(ctx, srcFile)
	srcFile.Close()
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", src, err)}, nil
	}

	dstFile, err := resolveOrCreate(ctx, root, dstParts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: %v", dst, err)}, nil
	}
	if dstSt, err := dstFile.Stat(ctx); err == nil && dstSt.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("cp: %s: is a directory (can't overwrite it with a file -- name a different destination)", dst)}, nil
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
