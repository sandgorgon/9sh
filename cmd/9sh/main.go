// Command 9sh is a minimal kyu REPL/script runner — enough to sanity-check
// the language against real input as each phase lands. It is not the
// shell yet: no tui integration, no dotfiles/session history (later
// phases).
package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/sandgorgon/9p/examples/dirfs"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/lexer"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/token"
	"github.com/sandgorgon/9sh/ns"
)

func main() {
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

	if len(os.Args) > 1 {
		src, err := os.ReadFile(os.Args[1])
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
