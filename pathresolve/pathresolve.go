// Package pathresolve resolves a command name to an executable path
// using a specific PATH value, standing in for exec.LookPath (which
// only ever consults this process's own os.Getenv("PATH")) wherever a
// subprocess's PATH comes from somewhere else — kyu's /env namespace,
// not 9sh's own real process environment. Without this, setenv("PATH",
// ...) would only change what a child process sees about its own
// environment, not which binary 9sh actually launches for it: Go's
// exec.Command resolves name against the calling process's PATH at
// construction time, before any Cmd.Env override is ever applied.
//
// See kyu/eval/external.go's evalPassthroughStmt and job/job.go's
// startSubprocess for the two callers.
package pathresolve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LookPath resolves name to an executable path using the PATH entry
// found in env ("NAME=VALUE" strings, exec.Cmd.Env's own shape) instead
// of this process's real environment.
//
// If name already contains a path separator, it's returned as-is if
// executable — matching exec.LookPath's own behavior of never
// consulting PATH for a name that's already a path. If env has no PATH
// entry at all, falls back to the real exec.LookPath (this process's
// own PATH) so callers with nothing to override see today's unchanged
// behavior.
func LookPath(name string, env []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		if isExecutable(name) {
			return name, nil
		}
		return "", fmt.Errorf("%s: not an executable file", name)
	}
	pathVar, ok := lookupEnv(env, "PATH")
	if !ok {
		return exec.LookPath(name)
	}
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			dir = "." // POSIX shell convention: an empty PATH entry means cwd
		}
		candidate := filepath.Join(dir, name)
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: executable file not found in PATH", name)
}

func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v, true
		}
	}
	return "", false
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	mode := info.Mode()
	return !mode.IsDir() && mode&0111 != 0
}
