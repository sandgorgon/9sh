// Package dotfiles loads 9sh's namespace-recipe repo (~/.config/9/ns) at
// shell startup: common.ky, then hosts/<hostname>.ky, each run as an
// ordinary kyu script directly against the shell's shared global Env —
// bind rules, aliases, and env defaults land in the live session exactly
// as if the user had typed them at the REPL.
//
// Deliberately separate from Phase 4's /session repo (hand-edited/
// reviewed vs. auto-appended), and this package only ever reads: sync
// (9vcs sync/clone/offer) is always a manual, user-triggered action run
// directly against ~/.config/9/ns with the plain 9vcs CLI, never wrapped
// or auto-invoked here — dotfile edits deserve a review step, unlike
// /session's auto-checkpoint. New-machine bootstrap (9vcs clone <peer>
// ~/.config/9/ns) is likewise entirely outside 9sh's code.
package dotfiles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/parser"
)

// Dir returns this install's namespace-recipe repo root,
// ~/.config/9/ns — matching 9auth's identity, and Phase 4's session
// repo, both anchored at ~/.config/9.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "9", "ns"), nil
}

// Load runs common.ky, then hosts/<hostname>.ky, against env, in that
// order — the design doc's "loaded in order: common then host-specific."
//
// Neither file existing is not an error: a fresh install, or one that
// hasn't set up dotfiles sync at all, starts up exactly as if this
// package weren't called — matching the graceful-degradation posture
// Phase 4's session recorder already established for an optional
// feature. A file that exists but fails to parse or run is reported as
// one warning and skipped, independently of the other file: a broken
// common.ky must not also block a working hosts/<hostname>.ky (or vice
// versa), and neither ever prevents the shell from starting.
func Load(env *eval.Env) {
	dir, err := Dir()
	if err != nil {
		return
	}
	runIfExists(filepath.Join(dir, "common.ky"), env)

	host, err := os.Hostname()
	if err != nil {
		return
	}
	runIfExists(filepath.Join(dir, "hosts", host+".ky"), env)
}

func runIfExists(path string, env *eval.Env) {
	src, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "9sh: reading %s: %v\n", path, err)
		}
		return
	}
	p := parser.New(string(src))
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "9sh: %s:\n", path)
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, " ", e)
		}
		return
	}
	if _, err := eval.Eval(prog, env); err != nil {
		fmt.Fprintf(os.Stderr, "9sh: %s: %v\n", path, err)
	}
}
