package eval

import (
	"context"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// protectedNamespaceRoots is the set of top-level namespace entries
// reset_config() leaves untouched: 9sh's own process bootstrap (/jobs,
// /local, /env, /config, /session, /ns — see README's "Startup sequence"),
// not something "the startup configs" (config.ky, common.ky,
// hosts/<hostname>.ky) are considered to own. Rebuilding these from
// scratch would need state (the job manager, the real launch directory,
// config/session directory paths) this package has no access to, and a
// running session can't safely discard some of them anyway — /jobs
// holds live job records. See biResetConfig's doc comment for the
// residual limitation this leaves.
var protectedNamespaceRoots = map[string]bool{
	"jobs": true, "local": true, "env": true, "config": true, "session": true, "ns": true,
}

// protectedVarNames is process-provided state that happens to live in
// the same Env.vars map a := variable would, but isn't something a
// dotfile defines or reset_config() should ever clear: cmd/9sh's
// script-mode entry point does env.Define("args", ...) directly (see
// main.go), the same primitive `:=` compiles to.
var protectedVarNames = map[string]bool{
	"args": true,
}

// biSourceConfig implements `source_config()`: re-runs config.ky, then
// common.ky/hosts/<hostname>.ky, against the current session — exactly
// cmd/9sh's own bootstrap sequence (see README's "Startup sequence"),
// just callable at runtime instead of only once at process start.
//
// Additive by construction, not a special mode of this builtin: a
// plain `bind SRC, DST` already defaults to "replace" (see
// ast.BindStmt's own doc comment), so re-running a dotfile that only
// uses plain bind naturally refreshes DST's target rather than
// stacking a duplicate layer, and a `:=` variable redefinition is an
// ordinary map overwrite (Env.Define). The only state that genuinely
// accumulates across repeated calls is a dotfile's own explicit
// before/after union bind — exactly the layering that syntax exists to
// ask for, so accumulating another layer there is the correct result of
// asking for it again, not a bug. reset_config() is the alternative for
// when that accumulation isn't what's wanted.
func biSourceConfig(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("source_config: expected no arguments, got %d", len(args))
	}
	fn := env.SourceConfig()
	if fn == nil {
		return nil, fmt.Errorf("source_config: not available in this environment")
	}
	fn(env)
	return value.Null{}, nil
}

// biResetConfig implements `reset_config()`: makes the running session
// look like a freshly started one would, then does exactly what
// source_config() does.
//
// "Fresh" is scoped to what the startup configs themselves are
// responsible for, not 9sh's own process bootstrap (see
// protectedNamespaceRoots' doc comment): every := -defined kyu variable
// is removed (the same builtin-vs-user-variable filter vars()/unset()
// already use, plus protectedVarNames for process-provided state that
// isn't a dotfile's), and every top-level namespace entry outside
// jobs/local/env/config/session/ns is unbound — a dotfile-created
// /n/<host> mount from dial()+bind, or any other bespoke bind a
// previous source_config()/reset_config() left behind, goes away before
// config.ky/common.ky/hosts/<hostname>.ky run again from a clean slate.
//
// Residual limitation (documented for users in docs.go and the README,
// and pinned by TestResetConfigLeavesLayersBoundOntoProtectedRoots): a
// dotfile that binds onto one of the six protected roots themselves —
// with an explicit before/after, or a plain replacing bind, whose
// replaced bootstrap layer isn't restored either — leaves that layer in
// place across a reset. Removing it would mean unbinding the root
// entirely, which this deliberately never does (see
// protectedNamespaceRoots).
func biResetConfig(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("reset_config: expected no arguments, got %d", len(args))
	}
	fn := env.SourceConfig()
	if fn == nil {
		return nil, fmt.Errorf("reset_config: not available in this environment")
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("reset_config: no namespace attached to this environment")
	}

	for _, name := range env.Names() {
		if protectedVarNames[name] {
			continue
		}
		v, ok := env.Get(name)
		if !ok {
			continue
		}
		if _, isBuiltin := v.(*Builtin); isBuiltin {
			continue
		}
		env.Delete(name)
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	entries, err := ns.ReadDirEntries(ctx, root)
	if err != nil {
		return nil, err
	}
	for _, ent := range entries {
		if protectedNamespaceRoots[ent.Name] {
			continue
		}
		if err := namespace.Unbind("/" + ent.Name); err != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("reset_config: unbind /%s: %v", ent.Name, err)}, nil
		}
	}

	fn(env)
	return value.Null{}, nil
}
