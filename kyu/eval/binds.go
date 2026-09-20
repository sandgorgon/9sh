package eval

import (
	"context"
	"encoding/json"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biBinds implements `binds()`: every layer bound in the namespace, as a
// Table of Records (dst, src, disp) — the structured view of /ns/binds
// the way ps() is of /jobs. Reads /ns/binds.json through the namespace,
// never the Namespace value directly, so it works on anything that
// serves a /ns the same way. src is Null for a bootstrap bind with no
// kyu spelling (/jobs, /env, ...).
//
// disp is the canonical replay disposition, not the one a layer was
// originally bound with — see ns.Bind.
func biBinds(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("binds: expected no arguments, got %d", len(args))
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("binds: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := openFile(ctx, root, p9.OREAD, "ns", "binds.json")
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("binds: %v", err)}, nil
	}
	defer f.Close()
	b, err := readAllFile(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("binds: %w", err)
	}
	var binds []ns.Bind
	if err := json.Unmarshal(b, &binds); err != nil {
		return nil, fmt.Errorf("binds: %w", err)
	}
	out := make([]value.Value, len(binds))
	for i, x := range binds {
		r := value.NewRecord()
		r.Set("dst", value.Path(x.Dst))
		if x.Src == "" {
			r.Set("src", value.Null{})
		} else {
			r.Set("src", value.String(x.Src))
		}
		r.Set("disp", value.String(x.Disp))
		out[i] = r
	}
	return value.NewList(out), nil
}
