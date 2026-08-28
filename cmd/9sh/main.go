// Command 9sh is a minimal kyu REPL/script runner, plus (via -tui) the
// pane multiplexer — enough to sanity-check each part of the language
// and its supporting subsystems as they land. Not yet the real shell:
// the two modes are still separate (no kyu-driven pane creation, no
// dotfiles/session history — later work).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/lexer"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/token"
	"github.com/sandgorgon/9sh/ns"
	"github.com/sandgorgon/9sh/pane"
)

func main() {
	tuiMode := flag.Bool("tui", false, "launch the pane multiplexer instead of the kyu REPL/script runner")
	flag.Parse()

	if *tuiMode {
		if err := runTUI(); err != nil {
			fmt.Fprintln(os.Stderr, "9sh:", err)
			os.Exit(1)
		}
		return
	}

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
	// checkout-able (and generally browsable) out of the box.
	if cwd, err := os.Getwd(); err == nil {
		if fs, err := dirfs.New(cwd); err == nil {
			namespace.BindFS(fs, "", "/local", ns.Replace)
		}
	}
	env := eval.NewGlobalEnv(namespace)

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

	repl(env)
}

// mouse reporting isn't on by default — tui.App.Run doesn't enable it
// itself (not every app wants click-to-focus), the same convention
// every tui example that wants clicks follows.
const (
	enableMouse  = "\x1b[?1000h\x1b[?1006h"
	disableMouse = "\x1b[?1000l\x1b[?1006l"
)

// runTUI launches the pane multiplexer: one shell pane to start, more
// addable via the "+ new pane" control, each independently minimizable
// via its title bar. See package pane's doc comment for the design
// rationale (why minimize is click/Enter-driven rather than a global
// hotkey, and why a pane's process survives being minimized).
func runTUI() error {
	m := pane.New(pane.ShellSpec("shell"))
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
// line-at-a-time reader would misparse it mid-statement.
func repl(env *eval.Env) {
	scanner := bufio.NewScanner(os.Stdin)
	var buf string
	depth := 0
	prompt := "9sh> "

	fmt.Print(prompt)
	for scanner.Scan() {
		line := scanner.Text()
		buf += line + "\n"
		depth += bracketDelta(line)

		if depth > 0 {
			fmt.Print("...  ")
			continue
		}

		if trimmedNonEmpty(buf) {
			runSource(buf, env)
		}
		buf = ""
		depth = 0
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

func bracketDelta(line string) int {
	l := lexer.New(line)
	depth := 0
	for {
		tok := l.Next()
		switch tok.Kind {
		case token.LPAREN, token.LBRACE, token.LBRACKET:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACKET:
			depth--
		case token.EOF:
			return depth
		}
	}
}
