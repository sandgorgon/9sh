package eval

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
)

// evalAtHost implements `@host { ... }`, `@/path { ... }` and
// `@(expr) { ... }`. Per the design doc, this is the entirety of "proxy
// jobs": no separate remote-job protocol exists — <mount>/jobs/<id>/*
// already are the remote job's real files once the peer is bound (bind
// grafted the whole remote tree, /jobs included), so all this needs to do
// is run the block in a child scope whose JobRoot points there instead of
// at the local /jobs. evalBackground and runExternalViaJob are unaware @
// exists at all; they just consult Env.JobRoot (and, to decide whether a
// local-side session-history linking record is even applicable,
// Env.ProxyRecorder — see namespace.go's isProxyJobRoot) — neither has any
// @-specific code.
//
// The operand is a mount point, a Path like everything else kyu takes a
// namespace location for (see atMount): the bare `@host` form is shorthand
// for `@/n/host`, and the other two forms take the mount as a Path value,
// so it can be a variable or built at run time (`@(/n + name)`).
//
// The <mount>/jobs walk before running the block is a deliberate
// eagerness: it turns a typo'd or never-dialed host into one clear error
// up front, instead of the block's first %cmd failing with a less
// specific "no such file" deep inside job creation.
func evalAtHost(x *ast.AtHost, env *Env) (value.Value, error) {
	mount, label, err := atMount(x, env)
	if err != nil {
		return nil, err
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("@%s: no namespace attached to this environment", label)
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	jobRoot := append(append([]string(nil), mount...), "jobs")
	if _, err := walkAll(ctx, root, jobRoot); err != nil {
		mountPath := "/" + strings.Join(mount, "/")
		return nil, fmt.Errorf("@%s: %w (is %s bound? try `bind dial(\"addr\"), %s` first)", label, err, mountPath, mountPath)
	}

	child := NewEnv(env)
	child.jobRoot = jobRoot
	result, err := evalBlock(x.Body, child)
	if err != nil {
		return nil, fmt.Errorf("@%s: %w", label, err)
	}
	return result, nil
}

// atMount resolves an @ operand to the mount point's path segments, plus
// the label error messages use for it: the bare host name for the
// `@host` shorthand (so its messages read as they always did), the path
// itself otherwise.
func atMount(x *ast.AtHost, env *Env) (segments []string, label string, err error) {
	if x.Target == nil {
		return []string{"n", x.Host}, x.Host, nil
	}
	v, err := evalExpr(x.Target, env)
	if err != nil {
		return nil, "", err
	}
	p, ok := v.(value.Path)
	if !ok {
		return nil, "", fmt.Errorf("@: expected a path naming a mount (e.g. /n/host, or /n + name), got %s — use path(str) to convert a string", v.Kind())
	}
	clean := path.Clean(string(p))
	if clean == "/" {
		return nil, "", fmt.Errorf("@/: the namespace root is not a remote mount (name one, e.g. /n/host)")
	}
	return strings.Split(strings.Trim(clean, "/"), "/"), string(p), nil
}
