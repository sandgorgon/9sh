package eval

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biCheckout implements `checkout(nsPath, closure)` — the explicit
// materialize tier for legacy tools that need a real, seekable OS path
// (vim, a compiler), not just bytes on a pipe: copy the namespace
// subtree at nsPath out to a real scratch directory, run closure with
// that real path, then write back whatever changed.
//
// It's registered outside the ordinary `builtins` map (see
// NewGlobalEnv) because — unlike where/select/each/etc. — it needs the
// namespace itself, which a plain BuiltinFn (args-only) has no access
// to; the wrapping closure built in NewGlobalEnv captures it instead of
// widening every builtin's signature for this one case.
//
// v1 scope, deliberate: a file deleted inside the scratch dir does not
// propagate as a namespace removal (only modifications and brand-new
// files write back), and a brand-new *subdirectory* isn't created in
// the namespace (only new files at an existing directory level) —
// both are real gaps, not silent data loss, since nothing is removed
// or corrupted by skipping them; they're narrower use cases than the
// primary one (edit an existing file or tree, write the changes back).
func biCheckout(namespace *ns.Namespace, args []value.Value) (value.Value, error) {
	if namespace == nil {
		return nil, fmt.Errorf("checkout: no namespace attached to this environment")
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("checkout: expected 2 arguments (a namespace path, a closure), got %d", len(args))
	}
	nsPath, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("checkout: first argument must be a path, got %s", args[0].Kind())
	}
	switch args[1].(type) {
	case *ClosureVal, *Builtin:
	default:
		return nil, fmt.Errorf("checkout: second argument must be a closure, got %s", args[1].Kind())
	}
	fn := args[1]

	ctx := context.Background()
	mat, err := materializeNamespacePath(ctx, namespace, nsPath)
	if err != nil {
		return nil, fmt.Errorf("checkout: %w", err)
	}
	defer os.RemoveAll(mat.scratchRoot)

	result, err := call(fn, []value.Value{value.Path(mat.scratchPath)})
	if err != nil {
		return nil, err
	}

	if err := writeBackNamespacePath(ctx, mat); err != nil {
		return nil, fmt.Errorf("checkout: writing back %s: %w", nsPath, err)
	}
	return result, nil
}

// materialized is what materializeNamespacePath produces and
// writeBackNamespacePath later consumes — the namespace handle plus
// enough real-filesystem state to write changes back once a caller is
// done with scratchPath. The caller owns cleanup of scratchRoot
// (os.RemoveAll) once it's no longer needed; src stays open across that
// whole window (it's an in-process 9P handle into namespace, not a
// resource that expires on its own).
type materialized struct {
	src         server.File
	scratchRoot string
	scratchPath string
	isDir       bool
	snapshot    map[string][]byte // absolute scratch path -> original content
}

// materializeNamespacePath copies the namespace subtree at nsPath (a
// single file or a whole directory) out to a fresh real scratch
// directory. Shared by biCheckout and runExternalFullscreen (see
// fullscreen.go) — the same materialize-then-run-then-write-back shape,
// just with a kyu closure or a real external process standing in for
// "run". Errors carry no "checkout:"/"%cmd:" prefix of their own — each
// caller wraps with whatever prefix fits its own error convention.
func materializeNamespacePath(ctx context.Context, namespace *ns.Namespace, nsPath value.Path) (*materialized, error) {
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	src, err := walkAll(ctx, root, splitPath(string(nsPath)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nsPath, err)
	}
	st, err := src.Stat(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: stat: %w", nsPath, err)
	}

	scratchRoot, err := os.MkdirTemp("", "9sh-checkout-")
	if err != nil {
		return nil, err
	}

	mat := &materialized{src: src, scratchRoot: scratchRoot, isDir: st.Qid.IsDir(), snapshot: map[string][]byte{}}
	if mat.isDir {
		mat.scratchPath = scratchRoot
		if err := materializeDir(ctx, src, scratchRoot, mat.snapshot); err != nil {
			os.RemoveAll(scratchRoot)
			return nil, err
		}
	} else {
		if err := src.Open(ctx, p9.OREAD); err != nil {
			os.RemoveAll(scratchRoot)
			return nil, fmt.Errorf("opening %s: %w", nsPath, err)
		}
		content, err := readAllFile(ctx, src)
		src.Close()
		if err != nil {
			os.RemoveAll(scratchRoot)
			return nil, fmt.Errorf("reading %s: %w", nsPath, err)
		}
		mat.scratchPath = filepath.Join(scratchRoot, filepath.Base(string(nsPath)))
		if err := os.WriteFile(mat.scratchPath, content, 0644); err != nil {
			os.RemoveAll(scratchRoot)
			return nil, err
		}
		mat.snapshot[mat.scratchPath] = content
	}
	return mat, nil
}

// writeBackNamespacePath pushes whatever changed under mat's scratch
// path(s) back into the namespace, dispatching to writeBackDir or
// writeBackFile depending on mat.isDir — the inverse of
// materializeNamespacePath.
func writeBackNamespacePath(ctx context.Context, mat *materialized) error {
	if mat.isDir {
		return writeBackDir(ctx, mat.src, mat.scratchRoot, mat.snapshot)
	}
	return writeBackFile(ctx, mat.src, mat.scratchPath, mat.snapshot[mat.scratchPath])
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// materializeDir recursively copies a namespace directory to a real one,
// recording each file's original content in snapshot (keyed by its
// resulting real path) for later dirty-detection in writeBackDir.
func materializeDir(ctx context.Context, nsDir server.File, realDir string, snapshot map[string][]byte) error {
	if err := os.MkdirAll(realDir, 0755); err != nil {
		return err
	}
	entries, err := ns.ReadDirEntries(ctx, nsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		child, err := nsDir.Walk(ctx, e.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
		realPath := filepath.Join(realDir, e.Name)
		if e.Qid.IsDir() {
			if err := materializeDir(ctx, child, realPath, snapshot); err != nil {
				return err
			}
			continue
		}
		if err := child.Open(ctx, p9.OREAD); err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
		content, err := readAllFile(ctx, child)
		child.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name, err)
		}
		if err := os.WriteFile(realPath, content, 0644); err != nil {
			return err
		}
		snapshot[realPath] = content
	}
	return nil
}

// writeBackFile pushes a single materialized file's content back into
// the namespace if it changed.
func writeBackFile(ctx context.Context, nsFile server.File, scratchPath string, original []byte) error {
	content, err := os.ReadFile(scratchPath)
	if err != nil {
		return err
	}
	if bytes.Equal(content, original) {
		return nil
	}
	if err := nsFile.Open(ctx, p9.OWRITE|p9.OTRUNC); err != nil {
		return err
	}
	defer nsFile.Close()
	_, err = nsFile.Write(ctx, 0, content)
	return err
}

// writeBackDir walks the real scratch tree and, for every regular file
// that's new or changed relative to snapshot, writes it back to the
// corresponding namespace path (creating it there if it's new).
func writeBackDir(ctx context.Context, nsRoot server.File, scratchRoot string, snapshot map[string][]byte) error {
	return filepath.WalkDir(scratchRoot, func(realPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.ReadFile(realPath)
		if err != nil {
			return err
		}
		if orig, existed := snapshot[realPath]; existed && bytes.Equal(orig, content) {
			return nil
		}
		rel, err := filepath.Rel(scratchRoot, realPath)
		if err != nil {
			return err
		}
		target, err := resolveOrCreate(ctx, nsRoot, strings.Split(rel, string(filepath.Separator)))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if err := target.Open(ctx, p9.OWRITE|p9.OTRUNC); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		defer target.Close()
		if _, err := target.Write(ctx, 0, content); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		return nil
	})
}

// resolveOrCreate walks parts from root, Create-ing only the final
// element if it's missing (a new file at an existing directory level) —
// a missing intermediate directory is an error, not something checkout
// invents on write-back; see the v1-scope note on biCheckout.
func resolveOrCreate(ctx context.Context, root server.File, parts []string) (server.File, error) {
	f := root
	for i, part := range parts {
		child, err := f.Walk(ctx, part)
		if err == nil {
			f = child
			continue
		}
		if i != len(parts)-1 {
			return nil, fmt.Errorf("%s: %w (checkout doesn't create new subdirectories)", part, err)
		}
		created, cerr := f.Create(ctx, part, 0644, p9.OWRITE)
		if cerr != nil {
			return nil, cerr
		}
		return created, nil
	}
	return f, nil
}
