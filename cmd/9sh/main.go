// Command 9sh is the shell's entry point: a real terminal launches
// package replui's single-screen interactive TUI (a native kyu REPL
// with live syntax highlighting, history search, and Tab completion);
// piped/non-terminal stdin falls back to a line-based REPL; a script
// argument runs headlessly either way. All three share one bootstrap
// and one kyu evaluation environment, so kyu code behaves identically
// regardless of how it's reached — see bootstrap. For a real multi-pane
// terminal (several 9sh sessions, or 9sh alongside a shell, side by
// side), see github.com/sandgorgon/9mux — that used to be part of this
// binary (package pane, removed in the same change that added
// package replui); see replui's own doc comment and 9mux's README for
// the full split rationale.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/term"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/config"
	"github.com/sandgorgon/9sh/dotfiles"
	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
	"github.com/sandgorgon/9sh/remote"
	"github.com/sandgorgon/9sh/replui"
	"github.com/sandgorgon/9sh/session"
)

// version is overridden at build time via
// -ldflags "-X main.version=vX.Y.Z" (see .github/workflows/release.yml),
// matching 9vcs's own convention — "dev" for a plain `go build`/`go run`.
var version = "dev"

func main() {
	os.Exit(run())
}

// run is main's actual body, returning an exit code instead of calling
// os.Exit directly — os.Exit skips deferred functions, and the session
// recorder's Close (its final checkpoint flush, "shell exit" being one
// of its three checkpoint triggers) must run on every exit path, not
// just a clean one.
func run() int {
	forceRepl := flag.Bool("repl", false,
		"use the line-based REPL even when a real terminal is available (default: launch the pane multiplexer)")
	listenAddr := flag.String("listen", "",
		"serve this shell's own namespace over mutual TLS on addr (host:port), so another 9sh can bind it at /n/<host> and run @host{} blocks against it — see package remote")
	listenUnixPath := flag.String("listen-unix", "",
		"serve this shell's own namespace over a Unix socket at path, restricted to this process's own UID — no TLS/9auth involved, for same-machine consumers (another local 9sh, or any 9P-aware app) — see remote.ListenUnix")
	showVersion := flag.Bool("version", false, "print the 9sh version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("9sh " + version)
		return 0
	}

	// sessionDir (bootstrap's third return) fed the old pane
	// multiplexer's "+ history" button — package replui has no
	// equivalent (see 9mux's own 9P-browsing pane for where that
	// capability's heading), so it's unused here now; bootstrap's own
	// signature is otherwise untouched (session history recording via
	// recorder is unrelated to reading it back for display).
	env, recorder, _, envScratchDir := bootstrap(*listenAddr, *listenUnixPath)
	if recorder != nil {
		defer recorder.Close()
	}
	if envScratchDir != "" {
		defer os.RemoveAll(envScratchDir)
	}

	if args := flag.Args(); len(args) > 0 {
		src, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// Everything after the script path itself is the script's own
		// argument list, kyu-visible as `args` (a List of String) --
		// `9sh script.kyu foo bar` sees args = ["foo", "bar"].
		env.Define("args", scriptArgsList(args[1:]))
		if !runSource(string(src), env) {
			return 1
		}
		return 0
	}

	if !*forceRepl && term.IsTerminal(os.Stdin) {
		if err := runTUI(env); err != nil {
			fmt.Fprintln(os.Stderr, "9sh:", err)
			return 1
		}
		return 0
	}

	repl(env)
	return 0
}

// bootstrap wires up 9sh's namespace, its job manager, and (best-effort)
// session history, returning the shared kyu evaluation environment used
// by every mode (script, line REPL, and replui's own single-screen TUI)
// so kyu code behaves identically no matter how it's reached. The
// returned *session.Recorder is nil if session history isn't available
// this run (no 9vcs on PATH, no writable home directory, ...) — that's
// never fatal to starting the shell at all, only to the history feature
// itself; the returned dir is still worth returning even then, since it
// may hold real history from an earlier run when 9vcs *was* available —
// reading it back is pure disk I/O, no 9vcs needed (no caller currently
// reads it back — package replui has no session-history view, unlike
// the old pane package's session-viewer pane; see 9mux's own README for
// where that capability is headed).
func bootstrap(listenAddr, listenUnixPath string) (*eval.Env, *session.Recorder, string, string) {
	namespace := ns.New()
	mgr := job.NewManager()
	// Bootstrap binds: 9sh's own Go-level setup, not something kyu's
	// `bind` (which only reshapes what's already in the namespace) can
	// do — see ns.Namespace.BindFS's doc.
	if err := namespace.BindFS(job.New(mgr), "", "/jobs", ns.Replace); err != nil {
		fmt.Fprintln(os.Stderr, "9sh: bootstrapping /jobs:", err)
		os.Exit(1)
	}
	// /local exposes the real launch directory — a placeholder default,
	// not a settled namespace-layout convention — so there's something
	// checkout-able (and browsable) out of the box.
	if cwd, err := os.Getwd(); err == nil {
		if fs, err := dirfs.New(cwd); err == nil {
			namespace.BindFS(fs, "", "/local", ns.Replace)
		}
	}

	if listenAddr != "" {
		// Serving is fire-and-forget for the shell's own lifetime — no
		// separate shutdown hook needed, since the process exiting tears
		// down the listener along with everything else.
		if _, err := remote.Listen(context.Background(), listenAddr, namespace); err != nil {
			fmt.Fprintln(os.Stderr, "9sh: -listen:", err)
			os.Exit(1)
		}
	}
	if listenUnixPath != "" {
		if _, err := remote.ListenUnix(context.Background(), listenUnixPath, namespace); err != nil {
			fmt.Fprintln(os.Stderr, "9sh: -listen-unix:", err)
			os.Exit(1)
		}
		// Exported into this process's own environment (not set per-job)
		// so every subprocess job inherits it via os/exec's ordinary
		// "nil Cmd.Env means inherit the current environment" default —
		// mirrors SSH_AUTH_SOCK: a job 9sh itself spawns (e.g. 9ed) can
		// dial straight back into its own parent's namespace with zero
		// configuration, no well-known path needed. A job whose kyu
		// script explicitly sets its own env opts out of this the same
		// way it opts out of the rest of the inherited environment.
		//
		// Named with a leading underscore, not "9SH_...": a POSIX shell
		// variable name can't start with a digit, so "9SH_UNIX_SOCK"
		// would be unreferenceable as $9SH_UNIX_SOCK from any spawned
		// sh/bash job (bash parses "$9" as a positional parameter,
		// leaving "SH_UNIX_SOCK" as trailing literal text) — caught
		// before shipping by checking the name actually expands in sh.
		os.Setenv("_9SH_UNIX_SOCK", listenUnixPath)
	}

	// /env exposes this process's environment as real namespace files —
	// Plan 9's own convention (env vars are files under /env), not a
	// hidden getenv/setenv side-table — via the same dirfs-over-a-real-
	// directory trick /local above already uses. Seeded from os.Environ()
	// (after _9SH_UNIX_SOCK above, so a job reading /env still inherits
	// it) so %cmd subprocesses keep inheriting PATH/HOME/etc by
	// default exactly as before this existed — see kyu/eval's envSlice,
	// which %cmd now builds its environment from instead of the
	// os/exec "nil Cmd.Env" implicit-inherit default. Ephemeral by
	// design, like Plan 9's own tmpfs-backed /env: cleaned up via
	// envScratchDir's caller-side defer, not meant to persist across runs.
	envScratchDir, err := os.MkdirTemp("", "9sh-env-*")
	if err == nil {
		for _, kv := range os.Environ() {
			if name, value, ok := strings.Cut(kv, "="); ok {
				os.WriteFile(filepath.Join(envScratchDir, name), []byte(value), 0600)
			}
		}
		if fs, err := dirfs.New(envScratchDir); err == nil {
			namespace.BindFS(fs, "", "/env", ns.Replace)
		}
	} else {
		envScratchDir = ""
	}

	// /config exposes 9sh's own settings (starting with
	// fullscreen_programs — see kyu/eval/fullscreen.go) as a real,
	// checkout-able namespace path, same dirfs-over-a-real-directory
	// trick as /local and /env above. EnsureDefault seeds config.ky with
	// sensible defaults on a fresh install without ever overwriting an
	// existing one.
	if err := config.EnsureDefault(); err == nil {
		if dir, err := config.Dir(); err == nil {
			if fs, err := dirfs.New(dir); err == nil {
				namespace.BindFS(fs, "", "/config", ns.Replace)
			}
		}
	}

	recorder, sessionDir := bootstrapSession(mgr)
	// /session exposes session history the same dirfs-over-a-real-
	// directory way /local, /env, and /config do — sessionDir is "" only
	// when bootstrapSession's os.UserHomeDir itself failed (see its own
	// doc comment), in which case there's no real directory to bind at
	// all. MkdirAll here (dirfs.New requires the directory to already
	// exist, unlike config.EnsureDefault's own mkdir) means /session is
	// bound unconditionally whenever a home directory exists, even on a
	// fresh install that has never had `9vcs` on PATH: reading past
	// history back is plain disk I/O, no 9vcs needed, and a directory
	// session.New's own ensureRepo hasn't touched yet is just an empty
	// history/-less /session until 9vcs becomes available and records
	// something into it. This is what lets a generic 9P-browsing tool
	// (see github.com/sandgorgon/9mux) reach session history the same
	// way it already reaches /jobs and /local, rather than needing its
	// own bespoke session-viewer pane — see that project's README for
	// the design this completes.
	if sessionDir != "" {
		if err := os.MkdirAll(sessionDir, 0755); err == nil {
			if fs, err := dirfs.New(sessionDir); err == nil {
				namespace.BindFS(fs, "", "/session", ns.Replace)
			}
		}
	}
	env := eval.NewGlobalEnv(namespace)
	if recorder != nil {
		// The local-side half of @host{} session recording: the remote
		// peer's own Recorder (if it has one) already logs an ordinary
		// entry for the job in its own history, on its own side — this
		// hook is what appends "I ran X on host Y" to *this* shell's
		// history too. See session.Recorder.RecordProxy's doc comment.
		env.SetProxyRecorder(func(host string, remoteID int, argv []string, tsStart, tsEnd time.Time, exitCode *int, signal string) {
			recorder.RecordProxy(session.ProxyJob{
				Host: host, RemoteID: remoteID, Argv: argv,
				TSStart: tsStart, TSEnd: tsEnd, Exit: exitCode, Signal: signal,
			})
		})
	}
	// config.Load runs before dotfiles.Load: config.ky's defaults (e.g.
	// fullscreen_programs) should already be in scope by the time
	// common.ky/hosts/<hostname>.ky run, so they can extend rather than
	// having to redeclare them whole
	// (fullscreen_programs := fullscreen_programs + ["mytool"]).
	config.Load(env)
	// Loaded last, once /jobs, /local, -listen, session history, and
	// /config are all already wired up: common.ky/hosts/<hostname>.ky may
	// reasonably want to bind, dial, or background jobs of their own.
	dotfiles.Load(env)
	return env, recorder, sessionDir, envScratchDir
}

// bootstrapSession sets up ~/.config/9/session and attaches it to mgr's
// OnFinish hook, so every job that reaches a terminal state — whether
// backgrounded via kyu's `&` or a synchronous foreground %cmd routed
// through /jobs — gets a history line for free (see package session's
// doc comment). Any failure here (no 9vcs on PATH, no home directory, a
// 9vcs error) is printed once and otherwise ignored: session history is
// a feature 9sh can run perfectly well without, not a startup
// requirement. The returned dir is "" only when os.UserHomeDir itself
// failed (nothing meaningful to read); any other failure (no 9vcs on
// PATH in particular) still returns the real dir, since reading past
// history back doesn't need 9vcs at all.
func bootstrapSession(mgr *job.Manager) (*session.Recorder, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "9sh: session history disabled:", err)
		return nil, ""
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}
	dir := filepath.Join(home, ".config", "9", "session")
	rec, err := session.New(dir, host)
	if err != nil {
		fmt.Fprintln(os.Stderr, "9sh: session history disabled:", err)
		return nil, dir
	}
	rec.Attach(mgr)
	return rec, dir
}

// mouse reporting isn't on by default — tui.App.Run doesn't enable it
// itself (not every app wants click-to-focus), the same convention
// every tui example that wants clicks follows. Bracketed paste (mode
// 2004) is the same story — tui's own input.Decoder already parses it
// into a PasteEvent (see kyu-repl's HandleEvent, the first consumer),
// it just isn't turned on for the caller automatically.
const (
	enableMouse  = "\x1b[?1000h\x1b[?1006h"
	disableMouse = "\x1b[?1000l\x1b[?1006l"

	enablePaste  = "\x1b[?2004h"
	disablePaste = "\x1b[?2004l"
)

// runTUI launches package replui's single-screen interactive TUI as
// 9sh's primary interactive experience — a native kyu REPL (sharing env
// with every other mode via bootstrap) with live syntax highlighting,
// Ctrl-R history search, and Tab completion; see replui's own doc
// comment for why it's still a real tui.Model rather than a bare
// widget (the built-in help overlay and the fullscreen-%cmd handoff
// both need something above the REPL widget itself in the tree).
func runTUI(env *eval.Env) error {
	// Direct stdio inheritance for a fullscreen %cmd (see
	// runExternalFullscreen) would race tui.App.Run's own raw-mode stdin
	// reader for every keystroke and write into a screen buffer the TUI
	// still thinks it owns -- see Env.SetPassthroughBlocked's doc
	// comment. Inside the TUI, a fullscreen command instead goes through
	// the checkout-and-pty-handoff path (package replui), which this
	// setting doesn't affect; this only guards the fallback case where
	// no fullscreen handler is registered at all (a nil w.env in a
	// bare-widget test, e.g.).
	env.SetPassthroughBlocked("no fullscreen handler registered — run this from 9sh's plain REPL (-repl) or a script instead")

	app := tui.NewApp(replui.New(env), 80, 24) // Run resizes to the real terminal size on start
	defer app.Close()

	fmt.Print(enableMouse)
	defer fmt.Print(disableMouse)
	fmt.Print(enablePaste)
	defer fmt.Print(disablePaste)

	return app.Run()
}

// scriptArgsList builds the kyu-visible `args` value from a script's
// own argv (everything after the script path itself, flag.Args()[1:]).
func scriptArgsList(scriptArgs []string) *value.List {
	out := make([]value.Value, len(scriptArgs))
	for i, a := range scriptArgs {
		out[i] = value.String(a)
	}
	return value.NewList(out)
}

func runSource(src string, env *eval.Env) bool {
	// Covers both script/-c execution (line ~95) and the plain line
	// REPL's per-line parse (repl(), below) -- native_programs (see
	// kyu/eval's IsNativeProgram) is already loaded into env by the time
	// either reaches here, since config.Load/dotfiles.Load both run
	// during bootstrap before main ever calls runSource.
	p := parser.New(src, parser.WithNativeProgramLookup(func(name string) bool {
		return eval.IsNativeProgram(env, name)
	}))
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
		return false
	}
	v, err := eval.Eval(prog, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	if v.Kind() != "null" {
		printResult(v)
	}
	return true
}

// printResult prints a top-level evaluation result. Bytes is special-cased:
// value.Bytes.String() deliberately stays a "<N bytes>" summary everywhere
// else (a record/table field showing full %cmd output inline would be
// unreadable), but a bare %cmd at the REPL is exactly the case where a real
// shell would just show you the output — so here, and only here, the raw
// content is written directly instead of going through the summary.
func printResult(v value.Value) {
	if b, ok := v.(value.Bytes); ok {
		os.Stdout.Write(b)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			fmt.Println()
		}
		return
	}
	fmt.Println(v.String())
}

// repl reads statements from stdin, accumulating lines until every
// bracket/brace/paren opened so far is closed — a closure body like
// `{ |j|\n  j.status == "running"\n}` spans several lines, so a strict
// line-at-a-time reader would misparse it mid-statement. This is the
// fallback for non-interactive/piped stdin (runTUI needs a real raw-
// mode terminal) or an explicit -repl; replui's own kyu-repl widget
// uses the same parser.BracketDepth check for the TUI.
// repl's stdin loop is a bare bufio.Scanner — no raw mode, nothing else
// ever reads os.Stdin — so unlike the TUI (see Env.SetPassthroughBlocked's
// doc comment), a Ctrl-C here is a real OS SIGINT, delivered
// asynchronously regardless of what the process is doing. signal.Notify
// below overrides Go's default "kill the process" disposition for it:
// this goroutine forwards each SIGINT to whatever foreground %cmd
// currently has an interrupt handler registered (see
// Env.SetInterruptHandler), or does nothing if nothing's running —
// matching a normal shell's "Ctrl-C at an idle prompt does nothing."
func repl(env *eval.Env) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if h := env.InterruptHandler(); h != nil {
				h()
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	var buf string
	prompt := "9sh> "

	fmt.Print(prompt)
	for scanner.Scan() {
		buf += scanner.Text() + "\n"

		if parser.BracketDepth(buf) > 0 {
			fmt.Print("...  ")
			continue
		}

		if trimmedNonEmpty(buf) {
			runSource(buf, env)
		}
		buf = ""
		fmt.Print(prompt)
	}
	fmt.Println()
}

func trimmedNonEmpty(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}
