package eval

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// Env is a lexical scope: a variable map with a parent link for closures.
// The namespace is process-wide, not lexical — ns is only ever set on the
// root Env (by NewGlobalEnv); Namespace() walks up to find it, the same
// way every other language keeps one thing (here, "what /jobs resolves
// to") outside the scope-per-block model that vars/Define/Set exist for.
//
// mu guards two different things depending on which Env it's locked on:
// on ANY node, it protects that node's own vars (see Get/Define/Set/
// Delete/Names, each of which locks e.mu, or n.mu while walking parents,
// never a different node's); on the root specifically, it additionally
// protects every field below marked "process-wide" (locked via
// e.root().mu — see e.g. SetCwd/Cwd). Both uses became necessary once
// evalBackgroundInproc existed: a closure snapshotted onto a background
// job's own fresh Env still carries a live reference to whatever Env it
// was originally *defined* in (ClosureVal.Env, kyu/eval/callable.go) --
// calling it, or any inner closure it in turn calls, walks right back
// into that original Env's vars and, via root(), the process-wide
// fields too. Before background jobs existed, "no two evaluate() calls
// ever overlap" made all of this safe without locking; a background
// job's own goroutine calling back into the foreground Env it was
// snapshotted from is exactly the second, genuinely concurrent access
// that invariant no longer rules out. ns and interruptHandler already
// used atomics for a narrower version of this same reason (see
// SetInterruptHandler's own doc comment) -- this generalizes that.
// cancelCtx/cancelFn joined this group later than the rest, once a real
// Ctrl-C (not just a background job's own ctl kill) needed to reach them
// too — see SetCancelContext's own doc comment.
type Env struct {
	mu                 sync.RWMutex
	vars               map[string]value.Value
	parent             *Env
	ns                 atomic.Pointer[ns.Namespace] // swapped for the duration of an in_ns block; see SwapNamespace
	jobRoot            []string                     // nil = inherit from parent; write-once at construction (athost.go), never reassigned on a shared Env afterward -- safe unlocked
	proxyRecorder      ProxyRecorderFunc            // process-wide, guarded by root().mu; see ProxyRecorder
	passthroughBlocked string                       // process-wide, guarded by root().mu; see SetPassthroughBlocked
	cwd                string                       // process-wide, guarded by root().mu; see SetCwd
	interruptHandler   atomic.Pointer[func()]       // process-wide; see SetInterruptHandler for why this one predates the general fix above and stays a dedicated atomic
	lastExitCode       *int                         // process-wide, guarded by root().mu; see SetLastExitCode
	fullscreenHandler  FullscreenHandlerFunc        // process-wide, guarded by root().mu; see SetFullscreenHandler
	attachHandler      AttachHandlerFunc            // process-wide, guarded by root().mu; see SetAttachHandler
	externalOutputSink ExternalOutputSinkFunc       // process-wide, guarded by root().mu; see SetExternalOutputSink
	sourceConfig       SourceConfigFunc             // process-wide, guarded by root().mu; see SetSourceConfig
	historyAccess      *HistoryAccess               // process-wide, guarded by root().mu; see SetHistoryAccess
	sourceDepth        int                          // process-wide, guarded by root().mu; see biSource's maxSourceDepth
	callDepth          int                          // process-wide, guarded by root().mu; see callClosure's maxCallDepth
	localJobManager    *job.Manager                 // process-wide; write-once at cmd/9sh's bootstrap, before env is ever shared with another goroutine -- safe unlocked, see SetLocalJobManager
	cancelCtx          context.Context              // process-wide, guarded by root().mu; see SetCancelContext
	cancelFn           func()                       // process-wide, guarded by root().mu; see SetCancelContext/CancelFunc
}

// ExternalOutputSinkFunc receives a foreground %cmd's captured stderr
// instead of it being written to the real os.Stderr — see
// SetExternalOutputSink's doc comment for why this exists. Stdout isn't
// routed through this: it's already returned as the call's own
// value.Bytes result (runExternalViaJob/runExternalDirect), displayed by
// whichever caller printed that result at a controlled point, never
// written to the real fd mid-evaluation — stderr is the only one of the
// two with nowhere else to go today.
type ExternalOutputSinkFunc func(stderr []byte)

// FullscreenHandlerFunc is how a fullscreen %cmd (see
// runExternalFullscreen) gets its real screen inside the TUI, where
// blocking synchronously for the child's whole lifetime would freeze
// the TUI's own render loop too (kyu evaluation and rendering share one
// goroutine). cmd is built (argv, Dir, Env, resolved Path) but not yet
// started — no Stdin/Stdout/Stderr set — since starting it and
// attaching a pty is the registrant's job (package replui, via
// widget.Terminal). The handler must return immediately without
// blocking; onDone must be called exactly once, whenever the child
// actually exits, so the caller can write any checked-out namespace
// paths back and clean up their scratch directories.
type FullscreenHandlerFunc func(cmd *exec.Cmd, onDone func(err error))

// AttachStream is the minimal shape attach() (see biAttach, attach.go)
// needs to hand off to a real terminal-emulator widget instead of this
// package's own plain-terminal raw passthrough — deliberately not
// tui/pty.Stream itself, to keep this package free of any UI-toolkit
// dependency, the same posture FullscreenHandlerFunc's plain *exec.Cmd
// signature already takes for the fullscreen-%cmd case. jobPtyFiles
// (attach.go) already has exactly this shape, so biAttach can hand one
// straight to whatever AttachHandlerFunc is registered without any
// adapting of its own; the registrant (package replui) is the one that
// adapts it to whatever its own Terminal widget actually wants.
type AttachStream interface {
	io.Reader
	io.Writer
	io.Closer
	Resize(rows, cols int) error
}

// AttachHandlerFunc is how attach() gets a real terminal-emulator
// widget inside the TUI, instead of erroring with "not supported inside
// the TUI yet" — the same "blocking synchronously would freeze the
// TUI's own render loop" reason FullscreenHandlerFunc exists for a
// fullscreen %cmd (see its own doc comment). The handler must return
// immediately without blocking; onDone must be called exactly once,
// whenever the attachment ends (the job exited, or the user detached).
type AttachHandlerFunc func(stream AttachStream, onDone func(err error))

// ProxyRecorderFunc is called once a job created via `@host{}` (a "proxy"
// job — see evalAtHost's doc comment) reaches a terminal state: the
// local-side "I ran X on host Y" linking record the design doc calls for
// (session.Recorder.RecordProxy has the full rationale). A plain
// callback, not a direct reference to package session, so eval doesn't
// need to import it — the same shape job.Manager.OnFinish's own callback
// already uses for the same reason.
type ProxyRecorderFunc func(host string, remoteID int, argv []string, tsStart, tsEnd time.Time, exitCode *int, signal string)

// SourceConfigFunc re-runs config.ky, then common.ky/hosts/<hostname>.ky,
// against env -- exactly cmd/9sh's own bootstrap sequence (README's
// "Startup sequence", steps 8-9). A hook rather than a direct call
// because kyu/eval can't import package config or package dotfiles:
// both of those already import kyu/eval to run kyu code against an Env,
// so the reverse import would cycle. cmd/9sh's bootstrap sets this to
// the same two calls (config.Load, dotfiles.Load) it already makes once
// at process start, so there's exactly one place that knows what "the
// startup configs" means. See source_config()/reset_config()
// (kyu/eval/source.go) for the two ways kyu code can invoke it.
type SourceConfigFunc func(env *Env)

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
	return e.root().ns.Load()
}

// SwapNamespace makes n the process's namespace and returns the one it
// replaced — process-wide like Namespace(), so every scope (and every
// builtin closure registered by NewGlobalEnv) sees the change at once.
// evalInNS pairs it with a deferred swap back; the atomic is only for
// readers on other goroutines (a job's completion callback, the TUI),
// since evaluation itself is single-threaded.
func (e *Env) SwapNamespace(n *ns.Namespace) *ns.Namespace {
	return e.root().ns.Swap(n)
}

// SetSourceConfig registers the hook source_config()/reset_config() call
// — process-wide like the namespace. nil (the default, e.g. bare
// eval-package tests that never call cmd/9sh's bootstrap) means neither
// builtin is available.
func (e *Env) SetSourceConfig(fn SourceConfigFunc) {
	root := e.root()
	root.mu.Lock()
	root.sourceConfig = fn
	root.mu.Unlock()
}

// SourceConfig returns the hook set by SetSourceConfig, or nil.
func (e *Env) SourceConfig() SourceConfigFunc {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.sourceConfig
}

// HistoryAccess bundles the three operations history()/
// history_delete(index)/history_clear() (kyu/eval/history.go) need
// against whatever's keeping REPL recall history — only replui's
// kyuReplWidget today. A hook for the same import-direction reason as
// SourceConfigFunc: kyu/eval can't import replui, since replui already
// imports kyu/eval. Registered set-before/clear-after each evaluate()
// call, the same pattern SetFullscreenHandler/SetExternalOutputSink
// already use (see kyurepl.go's evaluate()) — safe because evaluate()
// calls never overlap.
//
// List returns every entry, oldest first, matching how Up/Down/Ctrl-R
// walk it. Delete removes the entry at a List()-reported index,
// reporting whether one existed there (unset()'s own convention).
// Clear removes every entry.
type HistoryAccess struct {
	List   func() []string
	Delete func(index int) bool
	Clear  func()
}

// SetHistoryAccess registers the hook history()/history_delete/
// history_clear call — process-wide like the namespace. nil (the
// default, and the state outside any evaluate() call, or in any entry
// point that isn't the TUI at all — the plain -repl/script modes have
// no recall history to begin with) means none of the three are
// available.
func (e *Env) SetHistoryAccess(a *HistoryAccess) {
	root := e.root()
	root.mu.Lock()
	root.historyAccess = a
	root.mu.Unlock()
}

// HistoryAccess returns the hook set by SetHistoryAccess, or nil.
func (e *Env) HistoryAccess() *HistoryAccess {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.historyAccess
}

// SetProxyRecorder configures the hook evalBackground/runExternalViaJob
// call when a job runs against a remote (`@host{}`-scoped) JobRoot — see
// cmd/9sh's bootstrap. Only meaningful set once, on the root Env; nil
// (the default) means proxy jobs simply aren't recorded, matching how
// session history degrades gracefully everywhere else in this codebase.
func (e *Env) SetProxyRecorder(fn ProxyRecorderFunc) {
	root := e.root()
	root.mu.Lock()
	root.proxyRecorder = fn
	root.mu.Unlock()
}

// ProxyRecorder returns the hook set by SetProxyRecorder, or nil.
func (e *Env) ProxyRecorder() ProxyRecorderFunc {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.proxyRecorder
}

// SetPassthroughBlocked marks whether a fullscreen %cmd (see
// runExternalFullscreen) can connect its child directly to this
// process's own stdin/stdout/stderr, process-wide like the namespace
// and proxy recorder. cmd/9sh's runTUI calls this before starting the
// TUI: direct-stdio inheritance is only safe outside it — tui.App.Run
// puts the terminal in raw mode and the alt screen for its entire
// session and runs a background goroutine that keeps reading os.Stdin
// for its own input decoding the whole time, so a subprocess sharing
// that fd would race it for every keystroke rather than receiving them
// reliably, on top of writing into a screen buffer the TUI still thinks
// it owns. Inside the TUI, a fullscreen command instead takes the
// checkout-and-pty-handoff path (see runExternalFullscreen and package
// replui's fullscreen handling), never direct stdio inheritance. The
// plain line REPL (cmd/9sh's repl(), reached via -repl or non-terminal
// stdin) has neither hazard — a bare bufio.Scanner loop, no raw mode,
// nothing else ever reads stdin — so it never calls this, and a
// fullscreen command there inherits stdio directly. "" (the default)
// means direct inheritance is allowed.
func (e *Env) SetPassthroughBlocked(reason string) {
	root := e.root()
	root.mu.Lock()
	root.passthroughBlocked = reason
	root.mu.Unlock()
}

// PassthroughBlocked returns the reason set by SetPassthroughBlocked, or
// "" if direct stdio inheritance is allowed here.
func (e *Env) PassthroughBlocked() string {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.passthroughBlocked
}

// SetFullscreenHandler registers the hook a fullscreen %cmd (see
// runExternalFullscreen) calls when PassthroughBlocked() is non-empty —
// process-wide like the namespace. Registered by package replui's
// kyu-repl widget around each single evaluation, the same set-before/
// clear-after-one-call pattern SetInterruptHandler already uses; safe
// because kyu evaluation is already inherently single-threaded — no two
// evaluate() calls ever overlap, so there's never ambiguity about which
// call this handler is for. nil (the default, and the state outside any
// evaluate() call) means no handler is registered; runExternalFullscreen
// falls back to an ErrorVal mentioning PassthroughBlocked's reason in
// that case rather than blocking.
func (e *Env) SetFullscreenHandler(fn FullscreenHandlerFunc) {
	root := e.root()
	root.mu.Lock()
	root.fullscreenHandler = fn
	root.mu.Unlock()
}

// FullscreenHandler returns the hook set by SetFullscreenHandler, or nil
// if none is currently registered.
func (e *Env) FullscreenHandler() FullscreenHandlerFunc {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.fullscreenHandler
}

// SetAttachHandler registers the hook attach() (biAttach, attach.go)
// calls when PassthroughBlocked() is non-empty — same set-before/
// clear-after-one-call pattern as SetFullscreenHandler, registered by
// package replui's kyu-repl widget around each single evaluation. nil
// (the default) means no handler is registered; biAttach falls back to
// a plain error mentioning the TUI in that case rather than blocking or
// corrupting the TUI's own raw-mode terminal state.
func (e *Env) SetAttachHandler(fn AttachHandlerFunc) {
	root := e.root()
	root.mu.Lock()
	root.attachHandler = fn
	root.mu.Unlock()
}

// AttachHandler returns the hook set by SetAttachHandler, or nil if
// none is currently registered.
func (e *Env) AttachHandler() AttachHandlerFunc {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.attachHandler
}

// SetExternalOutputSink registers where a foreground %cmd's captured
// stderr goes, process-wide like the namespace. runExternalViaJob/
// runExternalDirect (external.go) write it directly to os.Stderr by
// default (nil sink, the zero value) — correct outside the TUI (the
// plain line REPL, script mode: nothing else owns the terminal, so
// direct inheritance is the simplest correct thing, same reasoning as
// PassthroughBlocked's doc comment above). Inside the TUI, replui's
// kyu-repl widget registers a sink for the duration of each evaluate()
// call (the same set-before/clear-after pattern SetFullscreenHandler
// already uses, and for the same reason: evaluate() calls never
// overlap, so there's no ambiguity about which call a callback is for)
// that appends into the same transcript evaluate() already writes
// results into: replui owns the screen via a diffed cell renderer, and
// a raw write to the real fd desyncs that renderer's own "what's on
// screen" bookkeeping from reality — a real, previously-shipped bug
// (os.Stderr.Write(errOut) in runExternalViaJob), not a hypothetical
// one.
func (e *Env) SetExternalOutputSink(fn ExternalOutputSinkFunc) {
	root := e.root()
	root.mu.Lock()
	root.externalOutputSink = fn
	root.mu.Unlock()
}

// ExternalOutputSink returns the hook set by SetExternalOutputSink, or
// nil if none is registered (direct os.Stdout/os.Stderr inheritance).
func (e *Env) ExternalOutputSink() ExternalOutputSinkFunc {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.externalOutputSink
}

// SetCwd sets the working directory `%cmd` subprocesses run in —
// process-wide like the namespace, not lexical, and deliberately not a
// real os.Chdir(): every 9sh entry point (script, -repl, the TUI's
// kyu-repl widget) shares this same root Env, so a real chdir would
// affect every consumer of it at once rather than being scoped to the
// one that called cd. "" (the default) means subprocesses inherit 9sh's
// own process cwd, unchanged from today's behavior.
func (e *Env) SetCwd(path string) {
	root := e.root()
	root.mu.Lock()
	root.cwd = path
	root.mu.Unlock()
}

// Cwd returns the path set by SetCwd, or "" if cd has never been called
// (subprocesses should then inherit 9sh's own process cwd as before).
func (e *Env) Cwd() string {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.cwd
}

// SetInterruptHandler registers the function a Ctrl-C should call to
// interrupt whatever foreground %cmd is currently running — process-wide
// like the namespace. Callers (runExternalViaJob, runExternalDirect,
// runExternalFullscreen's direct-stdio path) set this once their
// subprocess has actually started and clear it (nil) once it returns,
// via defer, so a signal arriving before start or after completion is
// simply ignored — matching a normal shell's "Ctrl-C at an idle prompt
// does nothing." Two callers reach this: cmd/9sh's repl() forwards a
// real SIGINT from signal.Notify; the TUI (replui/kyurepl.go's handleKey)
// calls it directly from a decoded Ctrl+C keystroke instead, since raw
// mode means the kernel never raises a real SIGINT there (see
// SetPassthroughBlocked's doc comment on that same raw-mode hazard).
// That second caller is exactly why this one field, alone among Env's
// other process-wide fields, needs real synchronization rather than
// relying on evaluate() calls never overlapping: it can now be read from
// the UI goroutine while a still-running evaluate(), on its own
// goroutine, concurrently sets or clears it via defer.
func (e *Env) SetInterruptHandler(fn func()) {
	root := e.root()
	if fn == nil {
		root.interruptHandler.Store(nil)
		return
	}
	root.interruptHandler.Store(&fn)
}

// InterruptHandler returns the function set by SetInterruptHandler, or
// nil if nothing interruptible is currently running.
func (e *Env) InterruptHandler() func() {
	if fn := e.root().interruptHandler.Load(); fn != nil {
		return *fn
	}
	return nil
}

// SetLastExitCode records a foreground %cmd's exit code — bash's $?
// equivalent, exposed as the exit_code() builtin rather than literal
// `$?` syntax (kyu has no `$`-prefixed syntax at all). Process-wide
// like the namespace, updated only by a foreground external-command
// call that actually ran to completion (runExternalDirect,
// runExternalViaJob, runExternalFullscreen) — never by an ordinary kyu
// expression, a backgrounded %cmd&, or a failed-to-start process (that
// case is already visible as an ErrorVal at the call site, and has no
// real exit code to report). A background job's exit code is already
// reachable via its own record (j.status.exit_code, j | wait) — this
// is specifically the "last foreground command" convenience, mirroring
// what bash's $? tracks.
func (e *Env) SetLastExitCode(code *int) {
	root := e.root()
	root.mu.Lock()
	root.lastExitCode = code
	root.mu.Unlock()
}

// LastExitCode returns the value set by SetLastExitCode, or nil if no
// foreground external command has completed yet this session.
func (e *Env) LastExitCode() *int {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.lastExitCode
}

// JobRoot returns the namespace path prefix job creation should use —
// ["jobs"] normally, or <mount>/jobs (["n", host, "jobs"] for `@host`)
// inside an `@ ... { ... }` block (see evalAtHost), searching outward through parents the same way
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

// SetLocalJobManager registers the *job.Manager backing this process's
// own local /jobs — process-wide like the namespace, set once by
// cmd/9sh's bootstrap right where it constructs that Manager and binds
// it into the namespace. evalBackgroundInproc is the one caller: an
// in-process background job (arbitrary kyu code, not %cmd) needs the
// concrete Go value to call Manager.AllocInproc on, since a live Go
// closure can't be carried across the abstract server.File interface
// the way argv bytes can for an ordinary %cmd & — see
// evalBackgroundInproc's own doc comment. Nil until set (e.g. in tests
// that construct an Env without cmd/9sh's bootstrap), in which case
// backgrounding non-%cmd kyu code fails with a clear error rather than
// a nil-pointer panic.
func (e *Env) SetLocalJobManager(mgr *job.Manager) {
	e.root().localJobManager = mgr
}

// LocalJobManager returns what SetLocalJobManager registered, or nil.
func (e *Env) LocalJobManager() *job.Manager {
	return e.root().localJobManager
}

// SetCancelContext registers ctx, and the function that cancels it, as
// the cancellation signal evalWhile and callClosure check on every loop
// iteration/call (see their own doc comments) — how a killed in-process
// background job (evalBackgroundInproc), or a real Ctrl-C over a
// foreground evaluation (replui/kyurepl.go's handleKey, cmd/9sh's
// repl()), actually stops a runaway `while true {}` or unbounded
// recursion, since Go can't force-kill a goroutine the way a real
// process can be signaled.
//
// cancel is nil for evalBackgroundInproc's own registration: a
// background job never needs to trigger its own cancellation through
// Env, since job.go's Ctl("kill") already calls the same context's
// cancel function directly. It's only ever non-nil for the foreground
// case, where there's no job object to call Ctl on — CancelFunc is how
// a real Ctrl-C reaches it instead, called the same way
// InterruptHandler already is: `if fn := env.CancelFunc(); fn != nil {
// fn() }`.
//
// Guarded by root().mu like every other process-wide field above
// (unlike this field's own original design — see git history): once a
// real Ctrl-C needs to trigger cancellation for a still-running
// *foreground* evaluation, this is read from the evaluating goroutine
// and written/triggered from a different one (the TUI's UI goroutine,
// or repl()'s own SIGINT-forwarding goroutine) at the same time,
// exactly the hazard SetInterruptHandler's own doc comment already
// describes for that field.
func (e *Env) SetCancelContext(ctx context.Context, cancel func()) {
	root := e.root()
	root.mu.Lock()
	root.cancelCtx, root.cancelFn = ctx, cancel
	root.mu.Unlock()
}

// CancelContext returns what SetCancelContext registered, or nil when
// nothing is currently running that this can cancel — evalWhile/
// callClosure's cancellation check is a no-op whenever this is nil.
func (e *Env) CancelContext() context.Context {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.cancelCtx
}

// CancelFunc returns the function SetCancelContext registered alongside
// its context, or nil if none is currently registered (idle, or a
// background job, which is never cancelled through Env — see
// SetCancelContext's own doc comment).
func (e *Env) CancelFunc() func() {
	root := e.root()
	root.mu.RLock()
	defer root.mu.RUnlock()
	return root.cancelFn
}

// Get looks up name in this scope, then outward through parents. Each
// level's own vars lookup is locked independently (see Env.mu's own doc
// comment) and released before recursing to the parent's own, separate
// lock — never held across the recursive call.
func (e *Env) Get(name string) (value.Value, bool) {
	e.mu.RLock()
	v, ok := e.vars[name]
	e.mu.RUnlock()
	if ok {
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
		n.mu.RLock()
		for name := range n.vars {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		n.mu.RUnlock()
	}
	return out
}

// Define binds name in this scope (kyu's `:=`), shadowing any outer binding.
func (e *Env) Define(name string, v value.Value) {
	e.mu.Lock()
	e.vars[name] = v
	e.mu.Unlock()
}

// Set assigns to an already-defined binding, searching outward (kyu's `=`).
// It reports whether an existing binding was found. Each level's own
// vars is locked independently, same as Get — never held across the
// recursive call to the parent's own, separate lock.
func (e *Env) Set(name string, v value.Value) bool {
	e.mu.Lock()
	if _, ok := e.vars[name]; ok {
		e.vars[name] = v
		e.mu.Unlock()
		return true
	}
	e.mu.Unlock()
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
	e.mu.Lock()
	if _, ok := e.vars[name]; ok {
		delete(e.vars, name)
		e.mu.Unlock()
		return true
	}
	e.mu.Unlock()
	if e.parent != nil {
		return e.parent.Delete(name)
	}
	return false
}
