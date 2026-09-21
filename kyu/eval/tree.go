package eval

import (
	"context"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"

	"github.com/sandgorgon/9sh/ns"
)

// removeTree recursively removes every entry inside dir, then dir
// itself -- the shared primitive behind rmdir's recursive mode and mv's
// cross-directory directory move (which removes src once its content
// has been copied to dst). Each child is Walk'd fresh rather than
// reusing an entry's Qid, since server.File has no "open by Qid"
// operation; a directory child is fully emptied before removal so
// Remove(ctx) -- a thin os.Remove on every backend that implements it,
// see dirfs.go -- never sees a non-empty directory.
func removeTree(ctx context.Context, dir server.File) error {
	entries, err := ns.ReadDirEntries(ctx, dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		child, err := dir.Walk(ctx, ent.Name)
		if err != nil {
			return err
		}
		if ent.Qid.IsDir() {
			if err := removeTree(ctx, child); err != nil {
				return err
			}
			continue
		}
		if err := child.Remove(ctx); err != nil {
			return err
		}
	}
	return dir.Remove(ctx)
}

// copyDirTree recursively copies srcDir's whole content into a newly
// created directory named name under dstParent -- the shared primitive
// behind cp's directory mode and mv's cross-directory directory move.
// dstParent must not already have an entry named name (mirrors mkdir's
// "already exists and isn't a directory" refusal -- no merge-into-an-
// existing-directory semantics yet, name a fresh destination), enforced
// by the caller before this is invoked, not here.
func copyDirTree(ctx context.Context, srcDir server.File, dstParent server.File, name string) error {
	entries, err := ns.ReadDirEntries(ctx, srcDir)
	if err != nil {
		return err
	}
	dstDir, err := dstParent.Create(ctx, name, p9.DMDIR|0755, 0)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		child, err := srcDir.Walk(ctx, ent.Name)
		if err != nil {
			return err
		}
		if ent.Qid.IsDir() {
			if err := copyDirTree(ctx, child, dstDir, ent.Name); err != nil {
				return err
			}
			continue
		}
		if err := copyFileInto(ctx, child, dstDir, ent.Name); err != nil {
			return err
		}
	}
	return nil
}

// copyFileInto reads src's whole content and writes it to a new file
// named name under dstDir -- the same Open(OREAD)/readAllFile, then
// Create/Open(OWRITE|OTRUNC)/Write shape biCp already uses for a single
// file, factored out so copyDirTree can call it per leaf.
func copyFileInto(ctx context.Context, src server.File, dstDir server.File, name string) error {
	if err := src.Open(ctx, p9.OREAD); err != nil {
		return err
	}
	content, err := readAllFile(ctx, src)
	src.Close()
	if err != nil {
		return err
	}
	dst, err := dstDir.Create(ctx, name, 0644, p9.OWRITE)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = dst.Write(ctx, 0, content)
	return err
}

// resolveDstIntoDir applies the rule cp and mv share with their Unix
// namesakes: when dst names an existing directory, the destination is
// that directory's entry named after src's base name — cp(/f, /d) writes
// /d/f, mv(/a/dir, /b) moves it to /b/dir. Anything else (a new name, an
// existing file, an unreachable path) is returned unchanged for the
// caller's usual handling. intoDir reports whether the rewrite happened.
// The namespace root as src has no base name, so is left alone.
func resolveDstIntoDir(ctx context.Context, root server.File, srcParts, dstParts []string) (parts []string, intoDir bool) {
	if len(srcParts) == 0 || len(dstParts) == 0 {
		return dstParts, false
	}
	f, err := walkAll(ctx, root, dstParts)
	if err != nil {
		return dstParts, false
	}
	st, err := f.Stat(ctx)
	if err != nil || !st.Qid.IsDir() {
		return dstParts, false
	}
	parts = append(append([]string(nil), dstParts...), srcParts[len(srcParts)-1])
	return parts, true
}

// isWithin reports whether p is dir itself or lies beneath it.
func isWithin(p, dir []string) bool {
	return len(p) >= len(dir) && samePath(p[:len(dir)], dir)
}

// selfTransferMsg explains why src can't be copied/moved to dst, or
// returns "" if it can. Two cases, both reachable now that dst may be
// resolved into a directory:
//
//   - the resolved dst is src itself (mv(/d/f, /d) → /d/f onto /d/f).
//     For mv that would copy the file onto itself and then remove it —
//     the only copy — so it must be refused, not treated as a no-op.
//   - a directory src whose dst is inside it: copying a tree into its own
//     subtree never terminates, since the copy keeps finding what it just
//     wrote.
//
// The check is lexical, on namespace paths: a bind that aliases src
// somewhere else isn't seen through.
func selfTransferMsg(srcIsDir bool, srcParts, dstParts []string, intoDir bool) string {
	if srcIsDir && isWithin(dstParts, srcParts) {
		return "can't copy or move a directory into itself"
	}
	if intoDir && samePath(srcParts, dstParts) {
		return "source and destination are the same file"
	}
	return ""
}
