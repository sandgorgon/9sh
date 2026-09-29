package eval

import (
	"context"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biHostPath implements `host_path(path)`: the real OS path behind a
// namespace Path, as a String — the Path -> String crossing that path(str)
// is the reverse of, for handing a namespace path to something that needs
// real path text (a %cmd argument, format(...), a comparison).
//
// Only a path served by a real host directory has one: a dir(...) bind,
// the /local bootstrap, or a path-bind of either (bind /local, /work is
// followed to its source). Anything else — a dial() remote, /jobs, /env,
// a synthetic directory, a union-expression bind — is an ordinary
// in-stream ErrorVal, never a guess. Local namespace only, like which_bind.
func biHostPath(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("host_path: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("host_path: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("host_path: no namespace attached to this environment")
	}
	hp, err := namespace.HostPath(context.Background(), string(p))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("host_path: %v", err)}, nil
	}
	return value.String(hp), nil
}
