package eval

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sandgorgon/9sh/kyu/value"
)

// effectiveCwd returns the directory a relative cd(...) argument and
// pwd() both resolve against: Env.Cwd() if cd has ever been called,
// else the real process's own os.Getwd() — matching what a %cmd/$cmd
// subprocess would inherit before any cd(). See Env.Cwd's doc comment.
func effectiveCwd(env *Env) (string, error) {
	if cwd := env.Cwd(); cwd != "" {
		return cwd, nil
	}
	return os.Getwd()
}

// biCd implements `cd(path)`. Like checkout, it needs the calling Env
// itself (to call SetCwd), which a plain BuiltinFn has no access to — see
// NewGlobalEnv's registration, which captures env in a closure the same
// way it already does for checkout. A relative path is resolved against
// effectiveCwd first — cd operates on kyu's own virtual cwd, which the
// real OS process's actual working directory has nothing to do with
// (SetCwd's doc comment), so os.Stat-ing the raw relative string would
// silently check the wrong directory, and storing it unresolved would
// make a later relative cd/pwd() report a bare fragment like "projects"
// instead of a real path. The resolved absolute path must exist and be
// a directory, checked up front (os.Stat) so a bad path is an ordinary
// in-stream ErrorVal, matching dial's "expected, in-stream failure"
// convention, not a hard Go error that aborts the whole script.
func biCd(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("cd: expected 1 argument (a path), got %d", len(args))
	}
	path, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("cd: expected a string path, got %s", args[0].Kind())
	}
	target := string(path)
	if !filepath.IsAbs(target) {
		base, err := effectiveCwd(env)
		if err != nil {
			return value.ErrorVal{Msg: fmt.Sprintf("cd: %v", err)}, nil
		}
		target = filepath.Join(base, target)
	}
	info, err := os.Stat(target)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cd: %v", err)}, nil
	}
	if !info.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("cd: %s: not a directory", target)}, nil
	}
	env.SetCwd(target)
	return value.Null{}, nil
}

// biPwd implements `pwd()`. Mirrors effectiveCwd directly rather than
// shelling out to `%pwd` — cwd is already in-process kyu state (see
// SetCwd's doc comment), so reading it back shouldn't need a subprocess
// round trip.
func biPwd(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("pwd: expected no arguments, got %d", len(args))
	}
	cwd, err := effectiveCwd(env)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("pwd: %v", err)}, nil
	}
	return value.String(cwd), nil
}
