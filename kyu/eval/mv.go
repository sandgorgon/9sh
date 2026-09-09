package eval

import (
	"context"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
)

// dontTouchStat returns a p9.Stat with every field set to its 9P2000
// "leave unchanged" wire sentinel (an empty string for the string
// fields, ^uint32(0)/^uint64(0) for the numeric ones — see
// client.Fid.WStat's own doc comment for the same convention spelled
// out client-side). biMv starts from this and overwrites only Name, so
// a rename touches nothing else about the file.
func dontTouchStat() p9.Stat {
	return p9.Stat{
		Mode:   ^p9.Mode(0),
		Atime:  ^uint32(0),
		Mtime:  ^uint32(0),
		Length: ^uint64(0),
	}
}

// biMv implements `mv(src, dst)`. When src and dst share the same parent
// directory, this is a real rename: one WStat with a new Name, the same
// primitive server.File.WStat already exposes on every backend a
// namespace can bind (see dontTouchStat's doc comment) — no content ever
// moves. When they don't share a parent (moving across namespace
// directories, possibly across a bind boundary onto a different real
// backend entirely), WStat's Name field can't reach across directories,
// so this falls back to a copy-then-remove, the same read/write shape
// biCp already uses, finished by removing src the way biRm does.
//
// v1 scope, deliberately narrow like cp/rm's own: src must be a regular
// file, not a directory. Any failure is an ordinary ErrorVal, not a hard
// error, matching cp/rm/stat/glob's convention.
func biMv(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("mv: expected 2 arguments (source path, destination path), got %d", len(args))
	}
	src, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("mv: first argument must be a path, got %s", args[0].Kind())
	}
	dst, ok := args[1].(value.Path)
	if !ok {
		return nil, fmt.Errorf("mv: second argument must be a path, got %s", args[1].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("mv: no namespace attached to this environment")
	}
	srcParts := splitPath(string(src))
	dstParts := splitPath(string(dst))
	if len(srcParts) == 0 {
		return value.ErrorVal{Msg: "mv: source cannot be the namespace root"}, nil
	}
	if len(dstParts) == 0 {
		return value.ErrorVal{Msg: "mv: destination cannot be the namespace root"}, nil
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	srcFile, err := walkAll(ctx, root, srcParts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", src, err)}, nil
	}
	srcSt, err := srcFile.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", src, err)}, nil
	}
	if srcSt.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: is a directory (directory move not yet supported)", src)}, nil
	}

	if samePath(srcParts[:len(srcParts)-1], dstParts[:len(dstParts)-1]) {
		st := dontTouchStat()
		st.Name = dstParts[len(dstParts)-1]
		if err := srcFile.WStat(ctx, st); err != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", dst, err)}, nil
		}
		return value.Null{}, nil
	}

	if err := srcFile.Open(ctx, p9.OREAD); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", src, err)}, nil
	}
	content, err := readAllFile(ctx, srcFile)
	srcFile.Close()
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", src, err)}, nil
	}

	dstFile, err := resolveOrCreate(ctx, root, dstParts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", dst, err)}, nil
	}
	if dstSt, err := dstFile.Stat(ctx); err == nil && dstSt.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: is a directory (moving into a directory not yet supported -- name the destination file explicitly)", dst)}, nil
	}
	if err := dstFile.Open(ctx, p9.OWRITE|p9.OTRUNC); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", dst, err)}, nil
	}
	if _, err := dstFile.Write(ctx, 0, content); err != nil {
		dstFile.Close()
		return value.ErrorVal{Msg: fmt.Sprintf("mv: %s: %v", dst, err)}, nil
	}
	dstFile.Close()

	// Re-walk src rather than reusing srcFile: it was already Open+Close'd
	// above for the read half, and Remove is documented per-backend as
	// operating on a fresh handle (see server.File.Remove's callers
	// elsewhere in this package), not one already cycled through a
	// read/close.
	srcFile2, err := walkAll(ctx, root, srcParts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: copied to %s but couldn't re-open %s to remove it: %v", dst, src, err)}, nil
	}
	if err := srcFile2.Remove(ctx); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("mv: copied to %s but couldn't remove %s: %v", dst, src, err)}, nil
	}
	return value.Null{}, nil
}

// samePath reports whether a and b name the same namespace directory
// (equal length, equal elements in order) -- used to decide whether mv
// can do a real in-place rename (same parent, WStat's Name field) or
// needs the copy-then-remove fallback.
func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
