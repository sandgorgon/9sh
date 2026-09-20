package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
)

// evalInNS implements `in_ns { ... }`: runs the block against a private
// copy of the namespace (ns.Namespace.Clone) and puts the original back
// when it ends — normally, on error, or through a break/continue
// unwinding out of an enclosing loop. Binds and unbinds inside don't
// leak out; everything already bound is visible inside, and a bind is a
// view, so a write through a shared bound directory is still a write to
// the real directory.
//
// The swap is process-wide (Env.SwapNamespace), which is exactly what
// makes it work for every builtin and %cmd in the block without each one
// being told — but it also means a background job started with `&`
// inside the block keeps running against whatever it already opened, not
// against the copy: it was allocated through the shared /jobs, and shows
// up in ps() afterwards like any other.
//
// Variables defined inside are block-scoped, like `if`. The value is the
// block's last value. A block that never touches the namespace costs a
// tree copy for nothing, which is cheap: it's the bind tree, not the
// files.
func evalInNS(x *ast.InNS, env *Env) (value.Value, error) {
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("in_ns: no namespace attached to this environment")
	}
	prev := env.SwapNamespace(namespace.Clone())
	defer env.SwapNamespace(prev)
	return evalBlock(x.Body, NewEnv(env))
}
