// Command 9sh is the shell's entry point: a real terminal launches the
// pane multiplexer (a native kyu REPL pane by default, shell and
// namespace-browser panes addable from there); piped/non-terminal
// stdin falls back to a line-based REPL; a script argument runs
// headlessly either way. All three share one bootstrap and one kyu
// evaluation environment, so kyu code behaves identically regardless
// of how it's reached — see bootstrap and package pane's doc comment.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/term"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/dotfiles"
	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/ns"
	"github.com/sandgorgon/9sh/pane"
	"github.com/sandgorgon/9sh/remote"
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
	showVersion := flag.Bool("version", false, "print the 9sh version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("9sh " + version)
		return 0
	}

	env, recorder, sessionDir := bootstrap(*listenAddr)
	if recorder != nil {
		defer recorder.Close()
	}

	if args := flag.Args(); len(args) > 0 {
		src, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !runSource(string(src), env) {
			return 1
		}
		return 0
	}

	if !*forceRepl && term.IsTerminal(os.Stdin) {
		if err := runTUI(env, sessionDir); err != nil {
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
// by every mode (script, line REPL, and the tui pane multiplexer's own
// kyu-repl pane) so kyu code behaves identically no matter how it's
// reached. The returned *session.Recorder is nil if session history
// isn't available this run (no 9vcs on PATH, no writable home
// directory, ...) — that's never fatal to starting the shell at all,
// only to the history feature itself; the returned dir (for
// pane.SessionViewerSpec, see runTUI) is still worth passing on even
// then, since it may hold real history from an earlier run when 9vcs
// *was* available — reading it back is pure disk I/O, no 9vcs needed.
func bootstrap(listenAddr string) (*eval.Env, *session.Recorder, string) {
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

	recorder, sessionDir := bootstrapSession(mgr)
	env := eval.NewGlobalEnv(namespace)
	// Loaded last, once /jobs, /local, -listen, and session history are
	// all already wired up: common.ky/hosts/<hostname>.ky may reasonably
	// want to bind, dial, or background jobs of their own.
	dotfiles.Load(env)
	return env, recorder, sessionDir
}

// bootstrapSession sets up ~/.config/9/session and attaches it to mgr's
// OnFinish hook, so every job that reaches a terminal state — whether
// backgrounded via kyu's `&` or a synchronous foreground %cmd routed
// through /jobs — gets a history line for free (see package session's
// doc comment). Any failure here (no 9vcs on PATH, no home directory, a
// 9vcs error) is printed once and otherwise ignored: session history is
// a feature 9sh can run perfectly well without, not a startup
// requirement. The returned dir is "" only when os.UserHomeDir itself
// failed (nothing meaningful to read even for pane.SessionViewerSpec);
// any other failure (no 9vcs on PATH in particular) still returns the
// real dir, since reading past history back doesn't need 9vcs at all.
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

// runTUI launches the pane multiplexer as 9sh's primary interactive
// experience: it starts with one native kyu REPL pane (sharing env
// with every other mode via bootstrap), with shell, namespace-browser,
// job-viewer, and session-viewer panes addable from the control strip.
// See package pane's doc comment for the design rationale (why
// minimize is click/Enter-driven rather than a global hotkey, and why
// a pane's process survives being minimized). sessionDir is passed
// straight through to pane.New for the "+ history" button; see
// bootstrapSession for what "" versus a real-but-recorder-less dir
// means here.
func runTUI(env *eval.Env, sessionDir string) error {
	m := pane.New(env, sessionDir, pane.KyuReplSpec("kyu", env))
	app := tui.NewApp(m, 80, 24) // Run resizes to the real terminal size on start
	defer app.Close()

	// Land keyboard focus on the kyu-repl pane's own content before Run
	// ever reads real input, not tui.App's zero-value default (the
	// control strip's first button) — see pane.InitialFocusAdvances'
	// doc comment for why this needs replaying real Tab events rather
	// than a simpler fix.
	for range pane.InitialFocusAdvances() {
		app.HandleInput(input.KeyEvent{Key: input.KeyTab})
	}

	fmt.Print(enableMouse)
	defer fmt.Print(disableMouse)
	fmt.Print(enablePaste)
	defer fmt.Print(disablePaste)

	return app.Run()
}

func runSource(src string, env *eval.Env) bool {
	p := parser.New(src)
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
		fmt.Println(v.String())
	}
	return true
}

// repl reads statements from stdin, accumulating lines until every
// bracket/brace/paren opened so far is closed — a closure body like
// `{ |j|\n  j.status == "running"\n}` spans several lines, so a strict
// line-at-a-time reader would misparse it mid-statement. This is the
// fallback for non-interactive/piped stdin (runTUI needs a real raw-
// mode terminal) or an explicit -repl; pane.kyuReplWidget uses the
// same parser.BracketDepth check for the native tui REPL pane.
func repl(env *eval.Env) {
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
