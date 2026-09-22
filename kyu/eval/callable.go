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

// minArity is how many arguments must be supplied — parameters with no
// default. maxArity (len(Params)) is how many can be — every parameter,
// default or not. Params enforces (at parse time) that a defaulted
// parameter never precedes a required one, so every parameter from
// minArity onward has a default.
func (c *ClosureVal) minArity() int {
	n := 0
	for _, p := range c.Node.Params {
		if p.Default == nil {
			n++
		}
	}
	return n
}

func (c *ClosureVal) maxArity() int { return len(c.Node.Params) }

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

// maxCallDepth bounds a closure calling itself (directly or through
// others), the same protection maxSourceDepth (sourcefile.go) already
// gives source() calling source() — a real crash this repo didn't have
// a test for until a backgrounded, genuinely unbounded self-recursive
// closure (`loop := { loop() }; loop() &`) hit it: callClosure's own
// ctx.Done() check below (see its own comment) cannot prevent this by
// itself, because plain Go recursion has no natural yield point the way
// evalWhile's loop does — thousands of recursive calls happen in well
// under a millisecond, blowing the goroutine's stack (a fatal,
// unrecoverable runtime error that takes down the *entire* process, not
// just the one evaluation) long before any cancellation signal could
// realistically be checked and acted on. 10000 is generous for any
// legitimate recursive kyu function while leaving comfortable headroom
// under Go's default ~1GB goroutine stack limit.
const maxCallDepth = 10000

// callClosure invokes c with args. Checks c.Env.CancelContext() first —
// see evalWhile's identical check for why: together with maxCallDepth
// below, this bounds a backgrounded closure's unbounded self-recursion
// the same way evalWhile bounds an unbounded loop, the only other
// unbounded-evaluation shape in this package (see
// evalBackgroundInproc's own doc comment for the full survey). The
// ctx.Done() check is a no-op for ordinary (non-backgrounded)
// evaluation (nil CancelContext) — maxCallDepth's own check is not: it
// applies unconditionally, since unbounded recursion crashes the whole
// process regardless of whether anything ever tries to background or
// kill it.
func callClosure(c *ClosureVal, args []value.Value) (value.Value, error) {
	if ctx := c.Env.CancelContext(); ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	root := c.Env.root()
	root.mu.Lock()
	if root.callDepth >= maxCallDepth {
		root.mu.Unlock()
		return nil, fmt.Errorf("call: nested more than %d deep (does it call itself without a base case?)", maxCallDepth)
	}
	root.callDepth++
	root.mu.Unlock()
	defer func() {
		root.mu.Lock()
		root.callDepth--
		root.mu.Unlock()
	}()

	min, max := c.minArity(), c.maxArity()
	if len(args) < min || len(args) > max {
		if min == max {
			return nil, fmt.Errorf("closure expects %d argument(s), got %d", max, len(args))
		}
		return nil, fmt.Errorf("closure expects %d to %d argument(s), got %d", min, max, len(args))
	}
	child := NewEnv(c.Env)
	for i, p := range c.Node.Params {
		if i < len(args) {
			child.Define(p.Name, args[i])
			continue
		}
		// Missing, so p.Default is guaranteed non-nil by the arity check
		// above (every parameter from minArity onward has one).
		// Evaluated in child, not c.Env, so a later default may
		// reference an earlier parameter already defined this call.
		def, err := evalExpr(p.Default, child)
		if err != nil {
			return nil, fmt.Errorf("evaluating default for parameter %q: %w", p.Name, err)
		}
		child.Define(p.Name, def)
	}
	return evalBlock(c.Node.Body, child)
}
