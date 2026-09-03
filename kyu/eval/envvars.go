package eval

import (
	"context"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// envSlice walks /env (cmd/9sh's bootstrap binds it — a dirfs-backed
// scratch directory seeded from os.Environ(), see bootstrap's doc
// comment) and builds a "NAME=VALUE" slice suitable for exec.Cmd.Env.
// namespace may be nil, and /env may simply not be bound (mainly tests
// that build an Env without cmd/9sh's full bootstrap, e.g. jobsEnv) —
// both return (nil, nil), the same "no overlay" shape os/exec's own nil
// Cmd.Env already means ("inherit the current process's environment"),
// so callers that don't have /env keep today's inherit-everything
// behavior unchanged rather than erroring or running with an empty
// environment.
// EnvSlice is envSlice's exported form — for callers outside package
// eval that need the same "NAME=VALUE" view of /env (pane's tab
// completion, resolving PATH the same way a %cmd/$cmd would).
func (e *Env) EnvSlice(ctx context.Context) ([]string, error) {
	return envSlice(ctx, e.Namespace())
}

func envSlice(ctx context.Context, namespace *ns.Namespace) ([]string, error) {
	if namespace == nil {
		return nil, nil
	}
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, nil
	}
	envDir, err := walkAll(ctx, root, []string{"env"})
	if err != nil {
		return nil, nil
	}
	entries, err := ns.ReadDirEntries(ctx, envDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, ent := range entries {
		f, err := openFile(ctx, root, p9.OREAD, "env", ent.Name)
		if err != nil {
			return nil, fmt.Errorf("envSlice: opening %s: %w", ent.Name, err)
		}
		b, err := readAllFile(ctx, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("envSlice: reading %s: %w", ent.Name, err)
		}
		out = append(out, ent.Name+"="+string(b))
	}
	return out, nil
}

// biGetenv implements `getenv(name)`. Like cd/checkout, it needs the
// calling Env (for its namespace) — see NewGlobalEnv's registration. An
// unset variable, or no /env at all, returns an empty string rather than
// an error — matching a shell's own $UNSET habit, the least-surprising
// default for something scripts will check casually.
func biGetenv(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("getenv: expected 1 argument (a name), got %d", len(args))
	}
	name, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("getenv: expected a string name, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return value.String(""), nil
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return value.String(""), nil
	}
	f, err := openFile(ctx, root, p9.OREAD, "env", string(name))
	if err != nil {
		return value.String(""), nil
	}
	defer f.Close()
	b, err := readAllFile(ctx, f)
	if err != nil {
		return value.String(""), nil
	}
	return value.String(b), nil
}

// biSetenv implements `setenv(name, value)`: writes (creating if absent,
// via resolveOrCreate — the same helper checkout's write-back already
// uses) /env/<name>, so a later %cmd/$cmd's envSlice call picks it up.
// Unlike getenv, no namespace at all is a hard error (fmt.Errorf, not an
// ErrorVal) — matching evalBindStmt's own "no namespace attached"
// convention for a namespace-mutating verb, not an ordinary expected
// failure like a bad path.
func biSetenv(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("setenv: expected 2 arguments (name, value), got %d", len(args))
	}
	name, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("setenv: expected a string name, got %s", args[0].Kind())
	}
	val, ok := args[1].(value.String)
	if !ok {
		return nil, fmt.Errorf("setenv: expected a string value, got %s", args[1].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("setenv: no namespace attached to this environment (is /env bound?)")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := resolveOrCreate(ctx, root, []string{"env", string(name)})
	if err != nil {
		return nil, fmt.Errorf("setenv: %w", err)
	}
	defer f.Close()
	// resolveOrCreate returns the file unopened, whether freshly Created
	// or found by Walk — same as checkout.go's own writeBackFile caller,
	// which explicitly Opens before Write for the same reason.
	if err := f.Open(ctx, p9.OWRITE|p9.OTRUNC); err != nil {
		return nil, fmt.Errorf("setenv: %w", err)
	}
	if _, err := f.Write(ctx, 0, []byte(val)); err != nil {
		return nil, fmt.Errorf("setenv: %w", err)
	}
	return value.Null{}, nil
}

// biUnsetenv implements `unsetenv(name)`: removes /env/<name> outright
// (dirfs.file.Remove is a real os.Remove under the hood, so this isn't
// "set to empty" — a subsequent getenv/envSlice sees no entry at all,
// same as a variable that was never set). Removing an already-absent
// name is not an error — matching a shell's own forgiving `unset`.
func biUnsetenv(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("unsetenv: expected 1 argument (a name), got %d", len(args))
	}
	name, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("unsetenv: expected a string name, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("unsetenv: no namespace attached to this environment (is /env bound?)")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, []string{"env", string(name)})
	if err != nil {
		return value.Null{}, nil // already absent — nothing to do
	}
	if err := f.Remove(ctx); err != nil {
		return nil, fmt.Errorf("unsetenv: %w", err)
	}
	return value.Null{}, nil
}
