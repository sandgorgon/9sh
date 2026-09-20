package eval

import (
	"context"
	"fmt"
	"strings"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/value"
)

// maxSourceDepth bounds source() calling source(), so a file that
// (directly or through others) sources itself is an error, not a stack
// overflow that takes the whole shell down.
const maxSourceDepth = 32

// biSource implements `source(path)`: reads a kyu file out of the
// namespace and runs it against the session's global Env — the same
// thing config.Load/dotfiles.Load do for the startup files, callable on
// demand for any namespace path. `source(/ns/binds)` replays this
// namespace's own bind list; `source(/n/host/ns/binds)` would replay a
// peer's.
//
// Defines and binds land in the session (the global Env), not in a
// scope local to the caller, exactly as if the file's text had been
// typed at the prompt. A file that doesn't parse is an in-stream
// ErrorVal listing every parse error (nothing in it runs); a runtime
// error is a hard abort, prefixed with the path, like any other
// script failure.
//
// This runs arbitrary kyu — %cmd included — with the shell's full
// authority, so sourcing a file from a remote peer is trusting that
// peer with your session, well beyond the read-only trust `bind
// dial(...)` alone extends. Reading it first (`cat(path)`) is the
// review step, same as it would be for a downloaded shell script.
func biSource(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("source: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("source: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("source: no namespace attached to this environment")
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := walkAll(ctx, root, splitPath(string(p)))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("source: %v", err)}, nil
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("source: %v", err)}, nil
	}
	if st.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("source: %s: is a directory", p)}, nil
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("source: %v", err)}, nil
	}
	defer f.Close()
	src, err := readAllFile(ctx, f)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("source: %v", err)}, nil
	}

	global := env.root()
	if global.sourceDepth >= maxSourceDepth {
		return nil, fmt.Errorf("source: %s: nested more than %d deep (does it source itself?)", p, maxSourceDepth)
	}
	global.sourceDepth++
	defer func() { global.sourceDepth-- }()

	ps := parser.New(string(src), parser.WithNativeProgramLookup(func(name string) bool {
		return isNativeProgram(global, name)
	}))
	prog := ps.ParseProgram()
	if errs := ps.Errors(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return value.ErrorVal{Msg: fmt.Sprintf("source: %s: %s", p, strings.Join(msgs, "; "))}, nil
	}
	if _, err := Eval(prog, global); err != nil {
		return nil, fmt.Errorf("source: %s: %w", p, err)
	}
	return value.Null{}, nil
}
