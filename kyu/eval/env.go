package eval

import (
	"time"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// Env is a lexical scope: a variable map with a parent link for closures.
// The namespace is process-wide, not lexical — ns is only ever set on the
// root Env (by NewGlobalEnv); Namespace() walks up to find it, the same
// way every other language keeps one thing (here, "what /jobs resolves
// to") outside the scope-per-block model that vars/Define/Set exist for.
type Env struct {
	vars          map[string]value.Value
	parent        *Env
	ns            *ns.Namespace
	jobRoot       []string          // nil = inherit from parent; see JobRoot
	proxyRecorder ProxyRecorderFunc // process-wide, like ns; see ProxyRecorder
}

// ProxyRecorderFunc is called once a job created via `@host{}` (a "proxy"
// job — see evalAtHost's doc comment) reaches a terminal state: the
// local-side "I ran X on host Y" linking record the design doc calls for
// (session.Recorder.RecordProxy has the full rationale). A plain
// callback, not a direct reference to package session, so eval doesn't
// need to import it — the same shape job.Manager.OnFinish's own callback
// already uses for the same reason.
type ProxyRecorderFunc func(host string, remoteID int, argv []string, tsStart, tsEnd time.Time, exitCode *int, signal string)

func NewEnv(parent *Env) *Env {
	return &Env{vars: map[string]value.Value{}, parent: parent}
}

// root walks up to the outermost Env — where process-wide state (the
// namespace, the proxy recorder) actually lives, regardless of how deep
// in nested scopes the caller is.
func (e *Env) root() *Env {
	n := e
	for n.parent != nil {
		n = n.parent
	}
	return n
}

// Namespace returns the process's namespace (nil if none was configured —
// see NewGlobalEnv), regardless of how deep in nested scopes e is.
func (e *Env) Namespace() *ns.Namespace {
	return e.root().ns
}

// SetProxyRecorder configures the hook evalBackground/runExternalViaJob
// call when a job runs against a remote (`@host{}`-scoped) JobRoot — see
// cmd/9sh's bootstrap. Only meaningful set once, on the root Env; nil
// (the default) means proxy jobs simply aren't recorded, matching how
// session history degrades gracefully everywhere else in this codebase.
func (e *Env) SetProxyRecorder(fn ProxyRecorderFunc) {
	e.root().proxyRecorder = fn
}

// ProxyRecorder returns the hook set by SetProxyRecorder, or nil.
func (e *Env) ProxyRecorder() ProxyRecorderFunc {
	return e.root().proxyRecorder
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
