package eval

import (
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// Env is a lexical scope: a variable map with a parent link for closures.
// The namespace is process-wide, not lexical — ns is only ever set on the
// root Env (by NewGlobalEnv); Namespace() walks up to find it, the same
// way every other language keeps one thing (here, "what /jobs resolves
// to") outside the scope-per-block model that vars/Define/Set exist for.
type Env struct {
	vars    map[string]value.Value
	parent  *Env
	ns      *ns.Namespace
	jobRoot []string // nil = inherit from parent; see JobRoot
}

func NewEnv(parent *Env) *Env {
	return &Env{vars: map[string]value.Value{}, parent: parent}
}

// Namespace returns the process's namespace (nil if none was configured —
// see NewGlobalEnv), regardless of how deep in nested scopes e is.
func (e *Env) Namespace() *ns.Namespace {
	n := e
	for n.parent != nil {
		n = n.parent
	}
	return n.ns
}

// JobRoot returns the namespace path prefix job creation should use —
// ["jobs"] normally, or ["n", host, "jobs"] inside an `@host { ... }`
// block (see evalAtHost), searching outward through parents the same way
// Get does. This is how `@host{}` desugars to "no separate remote-job
// protocol" per the design doc: evalBackground and runExternalViaJob
// don't know they're running inside an @host block at all, they just ask
// for the current job root.
func (e *Env) JobRoot() []string {
	n := e
	for n != nil {
		if n.jobRoot != nil {
			return n.jobRoot
		}
		n = n.parent
	}
	return []string{"jobs"}
}

// Get looks up name in this scope, then outward through parents.
func (e *Env) Get(name string) (value.Value, bool) {
	if v, ok := e.vars[name]; ok {
		return v, true
	}
	if e.parent != nil {
		return e.parent.Get(name)
	}
	return nil, false
}

// Define binds name in this scope (kyu's `:=`), shadowing any outer binding.
func (e *Env) Define(name string, v value.Value) {
	e.vars[name] = v
}

// Set assigns to an already-defined binding, searching outward (kyu's `=`).
// It reports whether an existing binding was found.
func (e *Env) Set(name string, v value.Value) bool {
	if _, ok := e.vars[name]; ok {
		e.vars[name] = v
		return true
	}
	if e.parent != nil {
		return e.parent.Set(name, v)
	}
	return false
}
