package eval

import (
	"context"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biWrite implements `write(path, str)`: replaces path's content with
// str, creating the file if it doesn't exist (in an already-existing
// directory — like cp/mv, it doesn't create parents; mkdir does).
// biAppend is the same but adds to the end. Together with cat they
// close the namespace-native read/write loop: everything cp already
// did between two files, from a string.
//
// It's the same Open(OWRITE|OTRUNC)+Write pattern cp and setenv use, so
// it reaches anything bound in the namespace a file server accepts a
// write for — a real file, a remote /n/host path, /env, /config.
func biWrite(env *Env, args []value.Value) (value.Value, error) {
	return writeString(env, "write", args, false)
}

// biAppend implements `append(path, str)` — see biWrite. The write goes
// at the file's current length, so it needs a file server that reports
// a meaningful length (every real directory does); a synthetic file
// that always reports 0 would be overwritten from the start instead.
func biAppend(env *Env, args []value.Value) (value.Value, error) {
	return writeString(env, "append", args, true)
}

func writeString(env *Env, name string, args []value.Value, appendMode bool) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("%s: expected 2 arguments (a path and a string), got %d", name, len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("%s: first argument must be a path, got %s", name, args[0].Kind())
	}
	s, ok := args[1].(value.String)
	if !ok {
		return nil, fmt.Errorf("%s: second argument must be a string, got %s (to_json() or format() first)", name, args[1].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("%s: no namespace attached to this environment", name)
	}
	parts := splitPath(string(p))
	if len(parts) == 0 {
		return value.ErrorVal{Msg: fmt.Sprintf("%s: cannot write the namespace root", name)}, nil
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := resolveOrCreate(ctx, root, parts)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("%s: %s: %v", name, p, err)}, nil
	}
	st, err := f.Stat(ctx)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("%s: %s: %v", name, p, err)}, nil
	}
	if st.Qid.IsDir() {
		return value.ErrorVal{Msg: fmt.Sprintf("%s: %s: is a directory", name, p)}, nil
	}

	mode, offset := p9.OWRITE|p9.OTRUNC, int64(0)
	if appendMode {
		mode, offset = p9.OWRITE, int64(st.Length)
	}
	if err := f.Open(ctx, mode); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("%s: %s: %v", name, p, err)}, nil
	}
	defer f.Close()
	if _, err := f.Write(ctx, offset, []byte(s)); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("%s: %s: %v", name, p, err)}, nil
	}
	return value.Null{}, nil
}
