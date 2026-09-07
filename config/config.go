// Package config loads 9sh's own settings file, ~/.config/9/config/
// config.ky, as an ordinary kyu script run directly against the shell's
// shared global Env — the same mechanism package dotfiles already uses
// for ~/.config/9/ns/common.ky. Unlike dotfiles (namespace-recipe
// scripts: bind rules, aliases, env defaults), this is for 9sh's own
// settings, starting with fullscreen_programs (see
// kyu/eval/fullscreen.go) — but it's still just kyu code, evaluated the
// same way, not a second declarative format.
//
// Deliberately a dedicated subdirectory of ~/.config/9, sibling to ns/
// and session/, rather than reusing either: binding it into the
// namespace at /config (see cmd/9sh's bootstrap) shouldn't also expose
// dotfiles or session history there.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/parser"
)

// defaultConfig is written to config.ky on first run only — never
// overwrites an existing file, matching dotfiles' own graceful-
// degradation posture for optional, user-owned config. The list is a
// starting point, not a fixed set: it's an ordinary kyu variable, so
// common.ky/hosts/<host>.ky (loaded after this — see cmd/9sh's
// bootstrap) can extend or replace it, and the user can edit config.ky
// directly.
const defaultConfig = `fullscreen_programs := ["vim", "vi", "nvim", "emacs", "top", "htop", "less", "man", "ssh", "nano", "mutt"]
`

// Dir returns this install's settings directory, ~/.config/9/config —
// matching 9auth's identity, dotfiles' ns/ repo, and Phase 4's session
// repo, all anchored at ~/.config/9.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "9", "config"), nil
}

// file returns config.ky's full path under Dir().
func file() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.ky"), nil
}

// EnsureDefault creates Dir() if missing and seeds it with defaultConfig
// if config.ky doesn't already exist there. Never overwrites an
// existing file — a fresh install gets sensible defaults, an existing
// one is left exactly as the user last edited it.
func EnsureDefault() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path, err := file()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil // already exists -- leave it alone
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(defaultConfig), 0644)
}

// Load runs config.ky against env, the same way dotfiles.Load runs
// common.ky — a missing file is a no-op (shouldn't happen once
// EnsureDefault has run, but this doesn't assume it always has: bare
// eval-package tests and other callers may skip EnsureDefault
// entirely). A file that exists but fails to parse or run is reported
// as one warning and skipped, never fatal to starting the shell —
// matching dotfiles.Load's own posture for an optional feature.
func Load(env *eval.Env) {
	path, err := file()
	if err != nil {
		return
	}
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
