package eval

import (
	"context"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biMkdir implements `mkdir(path)`: creates path, creating any missing
// intermediate directory along the way (Unix `mkdir -p` semantics, not
// plain `mkdir` -- there's no flag syntax in kyu to ask for the other
// behavior, and "just make this path exist as a directory" is the
// nearly-always-wanted case). A component that already exists is fine
// as long as it's a directory (so mkdir is idempotent: calling it twice
// on the same path is a no-op the second time, not an error); a
// component that exists as something other than a directory is an
// ordinary ErrorVal.
//
// Every backend a namespace can bind already implements the one
// primitive this needs generically (dirfs's own Create branches on
// perm.IsDir() before ever reaching its file-open logic; ns/file.go's
// Create passes perm through untouched) -- this just exposes it to kyu
// directly, mirroring rm/mv's "already-there primitive, never wired up"
// story.
func biMkdir(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("mkdir: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("mkdir: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("mkdir: no namespace attached to this environment")
	}
	parts := splitPath(string(p))
	if len(parts) == 0 {
		return value.ErrorVal{Msg: "mkdir: cannot create the namespace root"}, nil
	}

	ctx := context.Background()
	f, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		child, walkErr := f.Walk(ctx, part)
		if walkErr == nil {
			st, statErr := child.Stat(ctx)
			if statErr != nil {
				return value.ErrorVal{Msg: fmt.Sprintf("mkdir: %s: %v", p, statErr)}, nil
			}
			if !st.Qid.IsDir() {
				return value.ErrorVal{Msg: fmt.Sprintf("mkdir: %s: %q exists and is not a directory", p, part)}, nil
			}
			f = child
			continue
		}
		created, createErr := f.Create(ctx, part, p9.DMDIR|0755, 0)
		if createErr != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("mkdir: %s: %v", p, createErr)}, nil
		}
		f = created
	}
	return value.Null{}, nil
}
