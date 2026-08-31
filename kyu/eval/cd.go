package eval

import (
	"fmt"
	"os"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biCd implements `cd(path)`. Like checkout, it needs the calling Env
// itself (to call SetCwd), which a plain BuiltinFn has no access to — see
// NewGlobalEnv's registration, which captures env in a closure the same
// way it already does for checkout. path must exist and be a directory,
// checked up front (os.Stat) so a bad path is an ordinary in-stream
// ErrorVal, matching dial's "expected, in-stream failure" convention,
// not a hard Go error that aborts the whole script.
func biCd(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("cd: expected 1 argument (a path), got %d", len(args))
	}
	path, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("cd: expected a string path, got %s", args[0].Kind())
	}
	info, err := os.Stat(string(path))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("cd: %v", err)}, nil
	}
	if !info.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("cd: %s: not a directory", path)}, nil
	}
	env.SetCwd(string(path))
	return value.Null{}, nil
}
