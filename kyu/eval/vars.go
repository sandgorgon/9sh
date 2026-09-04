package eval

import (
	"fmt"
	"sort"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biVars implements `vars()` — kyu's own equivalent of /env's "the
// environment is just files you can ls": there's no namespace-backed
// equivalent for kyu variables (h := ...), since they're plain lexical
// scope, not namespace state, so this is the only way to list them.
//
// Built on Env.Names() (already existed, for pane/kyurepl.go's tab
// completion) but filtered down to *user* bindings: Names() reports
// every visible name including builtins, since builtins are just
// ordinary env.Define entries on the root Env with no separate
// registry — vars() drops anything whose current (innermost/shadowing)
// value is a *Builtin so "the variables you've set" doesn't come back
// full of where/select/dial/checkout/etc. A user variable that happens
// to shadow a builtin name still shows up correctly, since the check is
// against the resolved value, not the name.
//
// Returns a Table (List of *Record, kyu's own convention) — name, kind,
// and the live value itself — rather than bare names, so the result is
// pipeable (`vars() | where kind == "path"`) like everything else
// data-shaped in kyu.
func biVars(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("vars: expected no arguments, got %d", len(args))
	}
	names := env.Names()
	sort.Strings(names)
	elems := make([]value.Value, 0, len(names))
	for _, name := range names {
		v, ok := env.Get(name)
		if !ok {
			continue // Names() and Get() walk the same scope chain
		}
		if _, isBuiltin := v.(*Builtin); isBuiltin {
			continue
		}
		rec := value.NewRecord()
		rec.Set("name", value.String(name))
		rec.Set("kind", value.String(v.Kind()))
		rec.Set("value", v)
		elems = append(elems, rec)
	}
	return value.NewList(elems), nil
}

// biUnset implements `unset(name)` — vars()'s natural companion, and
// kyu variables' equivalent of unsetenv/unbind (every "set" verb in kyu
// gets a matching removal verb). Searches outward the same way `=`
// (Env.Set) does, so it reaches whichever scope actually holds the
// binding, and reports whether one was found — matching unsetenv's
// forgiving "already absent" convention rather than unbind's hard
// error, since a lexical variable is closer in spirit to an env var
// slot than to a structural namespace mutation.
//
// Refuses to remove a builtin: unset("dial") searching outward would
// otherwise happily delete the root Env's own builtin binding and take
// it out for the rest of the session — an explicit error here, not
// silent breakage, matching how dial/dir/checkout all refuse a
// wrong-shaped argument outright rather than guessing.
func biUnset(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("unset: expected 1 argument (a name), got %d", len(args))
	}
	name, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("unset: expected a string name, got %s", args[0].Kind())
	}
	if v, found := env.Get(string(name)); found {
		if _, isBuiltin := v.(*Builtin); isBuiltin {
			return nil, fmt.Errorf("unset: %q is a builtin, not a variable", string(name))
		}
	}
	return value.Bool(env.Delete(string(name))), nil
}
