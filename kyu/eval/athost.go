package eval

import (
	"context"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
)

// evalAtHost implements `@host { ... }`. Per the design doc, this is the
// entirety of "proxy jobs": no separate remote-job protocol exists —
// /n/<host>/jobs/<id>/* already are the remote job's real files once host
// is bound (bind grafted the whole remote tree, /jobs included), so all
// this needs to do is run the block in a child scope whose JobRoot points
// there instead of at the local /jobs. evalBackground and
// runExternalViaJob are unaware @host exists at all; they just consult
// Env.JobRoot (and, to decide whether a local-side session-history
// linking record is even applicable, Env.ProxyRecorder — see
// namespace.go's isProxyJobRoot) — neither has any @host-specific code.
//
// The /n/<host>/jobs walk before running the block is a deliberate
// eagerness: it turns a typo'd or never-dialed host into one clear error
// up front, instead of the block's first %cmd failing with a less
// specific "no such file" deep inside job creation.
func evalAtHost(x *ast.AtHost, env *Env) (value.Value, error) {
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("@%s: no namespace attached to this environment", x.Host)
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	jobRoot := []string{"n", x.Host, "jobs"}
	if _, err := walkAll(ctx, root, jobRoot); err != nil {
		return nil, fmt.Errorf("@%s: %w (is /n/%s bound? try `bind dial(\"addr\"), \"/n/%s\"` first)", x.Host, err, x.Host, x.Host)
	}

	child := NewEnv(env)
	child.jobRoot = jobRoot
	result, err := evalBlock(x.Body, child)
	if err != nil {
		return nil, fmt.Errorf("@%s: %w", x.Host, err)
	}
	return result, nil
}
