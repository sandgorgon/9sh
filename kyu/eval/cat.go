package eval

import (
	"context"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biCat implements `cat(path)`: reads one namespace file's whole
// content back as a String. The namespace-native content-read
// counterpart to stat/ls's metadata: those already expose a file's
// Stat for free (ns.ReadDirEntries fetches it anyway), but reading a
// namespace-only file's actual bytes (an /env var, a job's status/ctl
// file, anything under a remote /n/host mount) had no path shorter
// than checkout()'s full materialize-to-scratch-dir round trip —
// overkill for "just show me what's in this file." Like getenv/glob/
// stat, needs the calling Env's namespace, so it's registered specially
// in NewGlobalEnv rather than the plain builtins map.
//
// Returns String, not Bytes: kyu's own convention reserves Bytes for
// the external/legacy-binary boundary (the % sigil), and a native
// builtin's whole point is to hand back something immediately pipeable
// into split/trim/contains/etc, which only accept String. Go's string
// is an arbitrary byte sequence, not enforced-UTF-8, so converting here
// loses nothing even for a genuinely binary file — it just won't be
// pleasant to read or safe to pass through case-mapping ops (upper/
// lower), same as cat-ing a binary file to a terminal in any other
// shell.
func biCat(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("cat: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("cat: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("cat: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, splitPath(string(p)))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cat: %v", err)}, nil
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cat: %v", err)}, nil
	}
	if st.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("cat: %s: is a directory", p)}, nil
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cat: %v", err)}, nil
	}
	defer f.Close()
	content, err := readAllFile(ctx, f)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cat: %v", err)}, nil
	}
	return value.String(content), nil
}
