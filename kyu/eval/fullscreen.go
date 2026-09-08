package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/pathresolve"
)

// isFullscreenProgram reports whether name is listed in the kyu-level
// fullscreen_programs variable — the config-driven list (see package
// config's config.ky, bound into the namespace at /config) of external
// commands that need to own the real screen and keyboard (vim, top,
// ssh, ...) rather than have their output captured into a job's growBuf
// like an ordinary %cmd. A missing or wrong-typed variable is treated
// as "no fullscreen programs configured," never an error — this check
// runs on every %cmd, so a bad config value must not be the reason an
// otherwise-fine command fails.
func isFullscreenProgram(env *Env, name string) bool {
	v, ok := env.Get("fullscreen_programs")
	if !ok {
		return false
	}
	list, ok := v.(*value.List)
	if !ok {
		return false
	}
	for _, elem := range list.Elems {
		if s, ok := elem.(value.String); ok && string(s) == name {
			return true
		}
	}
	return false
}

// checkoutEntry tracks one namespace-only Path argument that
// runExternalFullscreen materialized to a real scratch location (see
// materializeNamespacePath in checkout.go), so it can be written back
// once the fullscreen program is done with it.
type checkoutEntry struct {
	nsPath value.Path
	mat    *materialized
}

func cleanupCheckouts(checkouts []checkoutEntry) {
	for _, c := range checkouts {
		os.RemoveAll(c.mat.scratchRoot)
	}
}

// writeBackCheckouts writes every tracked checkout back into the
// namespace, then removes its scratch directory. Errors are reported
// but don't stop the remaining checkouts from being attempted or
// cleaned up -- one bad write-back shouldn't leak every other scratch
// directory or hide problems with the rest.
func writeBackCheckouts(name string, checkouts []checkoutEntry) *value.ErrorVal {
	ctx := context.Background()
	var firstErr *value.ErrorVal
	for _, c := range checkouts {
		if err := writeBackNamespacePath(ctx, c.mat); err != nil && firstErr == nil {
			firstErr = &value.ErrorVal{Msg: fmt.Sprintf("%%%s: writing back %s: %v", name, c.nsPath, err)}
		}
		os.RemoveAll(c.mat.scratchRoot)
	}
	return firstErr
}

// runExternalFullscreen runs a %cmd whose name is in fullscreen_programs
// (see isFullscreenProgram): a program that needs to own the real
// screen and keyboard, not have its output captured. Any argument that
// evaluates to a namespace-only Path (see resolvesInNamespaceOnly) is
// transparently materialized via the same machinery checkout() uses
// (materializeNamespacePath), instead of erroring the way an ordinary
// %cmd would (checkNamespaceOnlyPath) -- the program gets a real
// scratch path, and changes are written back into the namespace once it
// exits.
//
// A piped-in value (%cmd | %vim) is silently ignored -- there's no
// sensible way to hand piped bytes to a program that's about to own the
// real terminal's stdin for interactive keyboard input.
//
// Unlike runExternalDirect/runExternalViaJob, this never goes through
// /jobs at all (see the design's v1 scope note) -- parity with the
// $cmd it supersedes, which never had /jobs visibility either.
//
// Backgrounding a fullscreen program is rejected before this function
// is ever reached -- see evalBackground's own guard in namespace.go.
func runExternalFullscreen(env *Env, name string, argExprs []ast.Expr) (value.Value, error) {
	ctx := context.Background()
	args := make([]string, len(argExprs))
	var checkouts []checkoutEntry
	for i, a := range argExprs {
		v, err := evalExpr(a, env)
		if err != nil {
			cleanupCheckouts(checkouts)
			return nil, err
		}
		if p, ok := v.(value.Path); ok && resolvesInNamespaceOnly(env, p) {
			mat, err := materializeNamespacePath(ctx, env.Namespace(), p)
			if err != nil {
				cleanupCheckouts(checkouts)
				return nil, fmt.Errorf("%%%s: %w", name, err)
			}
			checkouts = append(checkouts, checkoutEntry{nsPath: p, mat: mat})
			args[i] = mat.scratchPath
			continue
		}
		s, err := argString(v)
		if err != nil {
			cleanupCheckouts(checkouts)
			return nil, fmt.Errorf("%%%s: argument %d: %w", name, i, err)
		}
		args[i] = s
	}

	envVars, err := envSlice(ctx, env.Namespace())
	if err != nil {
		cleanupCheckouts(checkouts)
		return nil, fmt.Errorf("%%%s: reading /env: %w", name, err)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = env.Cwd()
	cmd.Env = envVars
	// Re-resolve against envVars's own PATH -- see pathresolve's doc
	// comment (exec.Command already resolved name against 9sh's own real
	// PATH, which the Cmd.Env assignment above never affects).
	if resolved, err := pathresolve.LookPath(name, envVars); err != nil {
		cleanupCheckouts(checkouts)
		return value.ErrorVal{Msg: fmt.Sprintf("%%%s: %v", name, err)}, nil
	} else {
		cmd.Path = resolved
		cmd.Err = nil
	}

	if reason := env.PassthroughBlocked(); reason != "" {
		handler := env.FullscreenHandler()
		if handler == nil {
			cleanupCheckouts(checkouts)
			return value.ErrorVal{Msg: fmt.Sprintf("%%%s: %s", name, reason)}, nil
		}
		// The handler (package replui) starts cmd itself, attached to a real
		// pty -- this function must not block, so any exit-code/write-back
		// handling happens later, from onDone, not here.
		handler(cmd, func(waitErr error) {
			env.SetLastExitCode(exitCodeFromWaitErr(waitErr))
			if ev := writeBackCheckouts(name, checkouts); ev != nil {
				fmt.Fprintf(os.Stderr, "9sh: %s\n", ev.Msg)
			}
		})
		return value.Null{}, nil
	}

	// Not inside the TUI (plain REPL or a script): safe to inherit stdio
	// directly and block synchronously, same as $cmd always did -- the
	// caller (cmd/9sh's repl()) is already a one-command-at-a-time loop.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cleanupCheckouts(checkouts)
		return value.ErrorVal{Msg: fmt.Sprintf("%%%s: %v", name, err)}, nil
	}
	// A -repl Ctrl-C should interrupt just this process -- see
	// Env.SetInterruptHandler's doc comment.
	env.SetInterruptHandler(func() { cmd.Process.Signal(os.Interrupt) })
	defer env.SetInterruptHandler(nil)
	_ = cmd.Wait() // non-zero exit is ordinary data, not a Go-level error here
	code := cmd.ProcessState.ExitCode()
	env.SetLastExitCode(&code)

	if ev := writeBackCheckouts(name, checkouts); ev != nil {
		return *ev, nil
	}
	return value.Null{}, nil
}

// exitCodeFromWaitErr extracts a *exec.Cmd.Wait() error into the same
// shape cmd.ProcessState.ExitCode() would give the direct-inheritance
// path just above -- 0 on a clean exit (waitErr == nil), the real exit
// code for an *exec.ExitError, or -1 for anything else (a signal, a
// start failure surfacing here instead of where it's normally caught)
// matching os/exec's own ExitCode() convention for "no real exit code
// available." The TUI-attached path's onDone only ever receives the
// widget.Terminal-wrapped Wait() result, never cmd.ProcessState
// directly, so this is that path's own equivalent of the three lines
// just above it.
func exitCodeFromWaitErr(waitErr error) *int {
	code := 0
	if waitErr != nil {
		code = -1
		if exitErr, ok := errors.AsType[*exec.ExitError](waitErr); ok {
			code = exitErr.ExitCode()
		}
	}
	return &code
}
