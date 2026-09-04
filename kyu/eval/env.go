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
	vars               map[string]value.Value
	parent             *Env
	ns                 *ns.Namespace
	jobRoot            []string          // nil = inherit from parent; see JobRoot
	proxyRecorder      ProxyRecorderFunc // process-wide, like ns; see ProxyRecorder
	passthroughBlocked string            // process-wide, like ns; see SetPassthroughBlocked
	cwd                string            // process-wide, like ns; see SetCwd
	interruptHandler   func()            // process-wide, like ns; see SetInterruptHandler
	lastExitCode       *int              // process-wide, like ns; see SetLastExitCode
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

// SetPassthroughBlocked makes every $cmd (ast.PassthroughStmt) evaluation
// fail with reason instead of running, process-wide like the namespace
// and proxy recorder. cmd/9sh's runTUI calls this before starting the
// pane multiplexer: $cmd connects a subprocess directly to this
// process's own stdin/stdout/stderr (see evalPassthroughStmt), which the
// TUI can't support safely — tui.App.Run puts the terminal in raw mode
// and the alt screen for its entire session and runs a background
// goroutine that keeps reading os.Stdin for its own input decoding the
// whole time, so a subprocess sharing that fd would race it for every
// keystroke rather than receiving them reliably, on top of writing into
// a screen buffer the TUI still thinks it owns. The plain line REPL
// (cmd/9sh's repl(), reached via -repl or non-terminal stdin) has
// neither hazard — a bare bufio.Scanner loop, no raw mode, nothing else
// ever reads stdin — so it never calls this and $cmd runs there
// unmodified. "" (the default) means $cmd is allowed.
func (e *Env) SetPassthroughBlocked(reason string) {
	e.root().passthroughBlocked = reason
}

// PassthroughBlocked returns the reason set by SetPassthroughBlocked, or
// "" if $cmd is allowed to run normally.
func (e *Env) PassthroughBlocked() string {
	return e.root().passthroughBlocked
}

// SetCwd sets the working directory `%cmd`/`$cmd` subprocesses run in —
// process-wide like the namespace, not lexical, and deliberately not a
// real os.Chdir(): every kyu-repl pane in a TUI session shares this same
// root Env (no eval.NewEnv call anywhere in package pane — every pane's
// Spec carries the same *Env), so a real chdir would silently redirect
// every pane's subsequent commands at once, not just the one that called
// cd. "" (the default) means subprocesses inherit 9sh's own process cwd,
// unchanged from today's behavior.
func (e *Env) SetCwd(path string) {
	e.root().cwd = path
}

// Cwd returns the path set by SetCwd, or "" if cd has never been called
// (subprocesses should then inherit 9sh's own process cwd as before).
func (e *Env) Cwd() string {
	return e.root().cwd
}

// SetInterruptHandler registers the function a `-repl` SIGINT (Ctrl-C)
// should call to interrupt whatever foreground %cmd/$cmd is currently
// running — process-wide like the namespace. Callers (runExternalViaJob,
// evalPassthroughStmt, runExternalDirect) set this once their subprocess
// has actually started and clear it (nil) once it returns, via defer, so
// a signal arriving before start or after completion is simply ignored —
// matching a normal shell's "Ctrl-C at an idle prompt does nothing."
// Only meaningful in cmd/9sh's repl(): the TUI can't safely deliver
// SIGINT-driven interrupts at all yet (see SetPassthroughBlocked's doc
// comment on the same underlying single-goroutine/raw-mode hazard), so
// runTUI never calls InterruptHandler.
func (e *Env) SetInterruptHandler(fn func()) {
	e.root().interruptHandler = fn
}

// InterruptHandler returns the function set by SetInterruptHandler, or
// nil if nothing interruptible is currently running.
func (e *Env) InterruptHandler() func() {
	return e.root().interruptHandler
}

// SetLastExitCode records a foreground %cmd/$cmd's exit code — bash's
// $? equivalent, exposed as the exit_code() builtin rather than literal
// `$?` syntax, which would collide with $cmd's own sigil. Process-wide
// like the namespace, updated only by a foreground external-command
// call that actually ran to completion (runExternalDirect,
// runExternalViaJob, evalPassthroughStmt) — never by an ordinary kyu
// expression, a backgrounded %cmd&, or a failed-to-start process (that
// case is already visible as an ErrorVal at the call site, and has no
// real exit code to report). A background job's exit code is already
// reachable via its own record (j.status.exit_code, j | wait) — this
// is specifically the "last foreground command" convenience, mirroring
// what bash's $? tracks.
func (e *Env) SetLastExitCode(code *int) {
	e.root().lastExitCode = code
}

// LastExitCode returns the value set by SetLastExitCode, or nil if no
// foreground external command has completed yet this session.
func (e *Env) LastExitCode() *int {
	return e.root().lastExitCode
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

// Names returns every name visible from e — this scope's own vars, then
// every enclosing scope's, deduplicated. Used for tab completion (see
// pane/kyurepl.go's completeTab), not by eval itself. This already
// covers every builtin for free: where/select/... (the plain builtins
// map, kyu/eval/builtins.go) and checkout/cd/getenv/setenv/unsetenv/
// glob/exit_code (the specially-registered ones, NewGlobalEnv) are all
// just ordinary env.Define-populated entries on the root Env — there's
// no separate "list of builtins" concept to expose here.
func (e *Env) Names() []string {
	seen := map[string]bool{}
	var out []string
	for n := e; n != nil; n = n.parent {
		for name := range n.vars {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
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

// Delete removes name's binding, searching outward the same way Set
// does (kyu's `unset`) — the mutation lands in whichever scope actually
// holds the name, mirroring Set's shadowing rules. Reports whether a
// binding was found and removed.
func (e *Env) Delete(name string) bool {
	if _, ok := e.vars[name]; ok {
		delete(e.vars, name)
		return true
	}
	if e.parent != nil {
		return e.parent.Delete(name)
	}
	return false
}
