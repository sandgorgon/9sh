package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
)

// ClosureVal is a `{ |params| body }` closure, bundled with the Env it
// closed over.
type ClosureVal struct {
	Node *ast.Closure
	Env  *Env
}

func (*ClosureVal) Kind() string   { return "function" }
func (*ClosureVal) String() string { return "<closure>" }
func (c *ClosureVal) arity() int   { return len(c.Node.Params) }

// BuiltinFn is the Go implementation of a built-in pipe-stage function.
// args is exactly what was written at the call site, in order, with the
// piped-in value (if any) appended as the final element by the pipe
// evaluator — see evalPipeExpr.
type BuiltinFn func(args []value.Value) (value.Value, error)

type Builtin struct {
	Name string
	Fn   BuiltinFn
}

func (*Builtin) Kind() string     { return "function" }
func (b *Builtin) String() string { return "<builtin " + b.Name + ">" }

// call invokes fn with args, dispatching on its concrete type.
func call(fn value.Value, args []value.Value) (value.Value, error) {
	switch f := fn.(type) {
	case *Builtin:
		return f.Fn(args)
	case *ClosureVal:
		return callClosure(f, args)
	default:
		return nil, fmt.Errorf("cannot call value of kind %q", fn.Kind())
	}
}

func callClosure(c *ClosureVal, args []value.Value) (value.Value, error) {
	if len(args) != c.arity() {
		return nil, fmt.Errorf("closure expects %d argument(s), got %d", c.arity(), len(args))
	}
	child := NewEnv(c.Env)
	for i, p := range c.Node.Params {
		child.Define(p, args[i])
	}
	return evalBlock(c.Node.Body, child)
}
