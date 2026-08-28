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
	"flag"
	"fmt"
	"os"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/term"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/ns"
	"github.com/sandgorgon/9sh/pane"
)

func main() {
	forceRepl := flag.Bool("repl", false,
		"use the line-based REPL even when a real terminal is available (default: launch the pane multiplexer)")
	flag.Parse()

	env := bootstrap()

	if args := flag.Args(); len(args) > 0 {
		src, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !runSource(string(src), env) {
			os.Exit(1)
		}
		return
	}

	if !*forceRepl && term.IsTerminal(os.Stdin) {
		if err := runTUI(env); err != nil {
			fmt.Fprintln(os.Stderr, "9sh:", err)
			os.Exit(1)
		}
		return
	}

	repl(env)
}

// bootstrap wires up 9sh's namespace and returns the shared kyu
// evaluation environment — used by every mode (script, line REPL, and
// the tui pane multiplexer's own kyu-repl pane) so kyu code behaves
// identically no matter how it's reached.
func bootstrap() *eval.Env {
	namespace := ns.New()
	// Bootstrap binds: 9sh's own Go-level setup, not something kyu's
	// `bind` (which only reshapes what's already in the namespace) can
	// do — see ns.Namespace.BindFS's doc.
	if err := namespace.BindFS(job.New(job.NewManager()), "", "/jobs", ns.Replace); err != nil {
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
	return eval.NewGlobalEnv(namespace)
}

// mouse reporting isn't on by default — tui.App.Run doesn't enable it
// itself (not every app wants click-to-focus), the same convention
// every tui example that wants clicks follows.
const (
	enableMouse  = "\x1b[?1000h\x1b[?1006h"
	disableMouse = "\x1b[?1000l\x1b[?1006l"
)

// runTUI launches the pane multiplexer as 9sh's primary interactive
// experience: it starts with one native kyu REPL pane (sharing env
// with every other mode via bootstrap), with shell and namespace-
// browser panes addable from the control strip. See package pane's
// doc comment for the design rationale (why minimize is click/Enter-
// driven rather than a global hotkey, and why a pane's process
// survives being minimized).
func runTUI(env *eval.Env) error {
	m := pane.New(env, pane.KyuReplSpec("kyu", env))
	app := tui.NewApp(m, 80, 24) // Run resizes to the real terminal size on start
	defer app.Close()

	fmt.Print(enableMouse)
	defer fmt.Print(disableMouse)

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
