package eval

import (
	"context"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biWhichBind implements `which_bind(path)`: what serves this path — the
// bind point and union layer a Walk to it goes through. The answer
// `ls` can't give inside a union directory, where a listing merges
// layers and hides which one a name came from.
//
// Returns a Record: path; kind ("layer", "bindpoint", or "tree" — see
// ns.Resolution); dst (the bind point); src (the serving layer's source
// expression, null for a bootstrap bind or a non-layer path); layer
// (0-based union position, null when none); layers (how many layers dst
// has); inner (the path within the layer, null unless kind is "layer").
// A path that doesn't resolve is an ordinary in-stream ErrorVal.
//
// Unlike ps()/binds() this asks the Namespace value directly rather than
// reading a file through it: "which layer would serve this" isn't
// expressible as file content without a lookup-by-path protocol, so it
// is local-namespace only (no @host, no /n/host/... peer's answer).
func biWhichBind(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("which_bind: expected 1 argument (a path), got %d", len(args))
	}
	p, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("which_bind: expected a path, got %s", args[0].Kind())
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("which_bind: no namespace attached to this environment")
	}
	res, err := namespace.Resolve(context.Background(), string(p))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("which_bind: %v", err)}, nil
	}
	r := value.NewRecord()
	r.Set("path", value.Path(res.Path))
	r.Set("kind", value.String(res.Kind))
	r.Set("dst", value.Path(res.Dst))
	if res.Kind == "layer" && res.Src != "" {
		r.Set("src", value.String(res.Src))
	} else {
		r.Set("src", value.Null{})
	}
	if res.Layer >= 0 {
		r.Set("layer", value.Int(res.Layer))
	} else {
		r.Set("layer", value.Null{})
	}
	r.Set("layers", value.Int(res.Layers))
	if res.Kind == "layer" {
		r.Set("inner", value.Path(res.Inner))
	} else {
		r.Set("inner", value.Null{})
	}
	return r, nil
}
