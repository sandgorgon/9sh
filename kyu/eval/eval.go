// Package eval is a tree-walking evaluator for kyu's ast.Program.
package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/token"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// NewGlobalEnv returns a root Env pre-populated with the built-in
// pipe-stage functions, so they resolve through ordinary Ident lookup —
// kyu treats where/select/etc. as plain functions, not keywords.
// namespace may be nil (bind and job-backgrounding then fail with a
// clear error instead of a nil-pointer panic) for callers — mainly
// tests — that don't need either.
func NewGlobalEnv(namespace *ns.Namespace) *Env {
	env := NewEnv(nil)
	env.ns = namespace
	for name, fn := range builtins {
		env.Define(name, &Builtin{Name: name, Fn: fn})
	}
	// checkout needs the namespace itself (to materialize/write back a
	// real subtree), which a plain BuiltinFn has no access to — captured
	// here rather than widening every other builtin's signature for
	// this one case. See checkout.go's biCheckout doc comment.
	env.Define("checkout", &Builtin{Name: "checkout", Fn: func(args []value.Value) (value.Value, error) {
		return biCheckout(namespace, args)
	}})
	return env
}

// Eval runs a full program and returns the value of its last statement.
func Eval(prog *ast.Program, env *Env) (value.Value, error) {
	return evalBlock(prog.Stmts, env)
}

func evalBlock(stmts []ast.Stmt, env *Env) (value.Value, error) {
	var result value.Value = value.Null{}
	for _, s := range stmts {
		v, err := evalStmt(s, env)
		if err != nil {
			return nil, err
		}
		result = v
	}
	return result, nil
}

func evalStmt(s ast.Stmt, env *Env) (value.Value, error) {
	switch st := s.(type) {
	case *ast.ExprStmt:
		return evalExpr(st.X, env)
	case *ast.DefineStmt:
		v, err := evalExpr(st.Val, env)
		if err != nil {
			return nil, err
		}
		env.Define(st.Name, v)
		return value.Null{}, nil
	case *ast.AssignStmt:
		return evalAssign(st, env)
	case *ast.BindStmt:
		return evalBindStmt(st, env)
	default:
		return nil, fmt.Errorf("eval: unknown statement type %T", s)
	}
}

func evalAssign(st *ast.AssignStmt, env *Env) (value.Value, error) {
	v, err := evalExpr(st.Val, env)
	if err != nil {
		return nil, err
	}
	switch tgt := st.Target.(type) {
	case *ast.Ident:
		if !env.Set(tgt.Name, v) {
			return nil, fmt.Errorf("undefined variable %q (use ':=' to define it)", tgt.Name)
		}
	case *ast.FieldAccess:
		recvVal, err := evalExpr(tgt.Recv, env)
		if err != nil {
			return nil, err
		}
		rec, ok := recvVal.(*value.Record)
		if !ok {
			return nil, fmt.Errorf("cannot assign field %q on a %s", tgt.Field, recvVal.Kind())
		}
		if err := rec.SetField(tgt.Field, v); err != nil {
			return nil, fmt.Errorf("cannot assign field %q: %w", tgt.Field, err)
		}
	default:
		return nil, fmt.Errorf("eval: invalid assignment target %T", st.Target)
	}
	return value.Null{}, nil
}

func evalExpr(e ast.Expr, env *Env) (value.Value, error) {
	switch x := e.(type) {
	case *ast.IntLit:
		return value.Int(x.Val), nil
	case *ast.FloatLit:
		return value.Float(x.Val), nil
	case *ast.StringLit:
		return value.String(x.Val), nil
	case *ast.BoolLit:
		return value.Bool(x.Val), nil
	case *ast.NullLit:
		return value.Null{}, nil
	case *ast.DurationLit:
		return value.Duration(x.Nanos), nil
	case *ast.PathLit:
		return value.Path(x.Val), nil
	case *ast.Ident:
		v, ok := env.Get(x.Name)
		if !ok {
			return nil, fmt.Errorf("undefined variable %q", x.Name)
		}
		return v, nil
	case *ast.RecordLit:
		return evalRecordLit(x, env)
	case *ast.TableLit:
		return evalListLike(x.Rows, env)
	case *ast.ListLit:
		return evalListLike(x.Elements, env)
	case *ast.Closure:
		return &ClosureVal{Node: x, Env: env}, nil
	case *ast.FieldAccess:
		return evalFieldAccess(x, env)
	case *ast.Call:
		return evalCall(x, env)
	case *ast.ExternalCall:
		return runExternal(x, nil, env)
	case *ast.PipeExpr:
		return evalPipeExpr(x, env)
	case *ast.BinaryExpr:
		return evalBinary(x, env)
	case *ast.UnaryExpr:
		return evalUnary(x, env)
	case *ast.ErrCheck:
		return evalErrCheck(x, env)
	case *ast.IfExpr:
		return evalIf(x, env)
	case *ast.AtHost:
		return evalAtHost(x, env)
	case *ast.Background:
		return evalBackground(x, env)
	default:
		return nil, fmt.Errorf("eval: unknown expression type %T", e)
	}
}

func evalRecordLit(x *ast.RecordLit, env *Env) (value.Value, error) {
	rec := value.NewRecord()
	for _, f := range x.Fields {
		v, err := evalExpr(f.Value, env)
		if err != nil {
			return nil, err
		}
		rec.Set(f.Name, v)
	}
	return rec, nil
}

func evalListLike(elems []ast.Expr, env *Env) (value.Value, error) {
	vals := make([]value.Value, len(elems))
	for i, e := range elems {
		v, err := evalExpr(e, env)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return value.NewList(vals), nil
}

func evalFieldAccess(x *ast.FieldAccess, env *Env) (value.Value, error) {
	recvVal, err := evalExpr(x.Recv, env)
	if err != nil {
		return nil, err
	}
	rec, ok := recvVal.(*value.Record)
	if !ok {
		return nil, fmt.Errorf("cannot access field %q on a %s", x.Field, recvVal.Kind())
	}
	v, ok := rec.Get(x.Field)
	if !ok {
		return nil, fmt.Errorf("record has no field %q", x.Field)
	}
	return v, nil
}

func evalArgs(args []ast.Expr, env *Env) ([]value.Value, error) {
	vals := make([]value.Value, len(args))
	for i, a := range args {
		v, err := evalExpr(a, env)
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

func evalCall(x *ast.Call, env *Env) (value.Value, error) {
	fnVal, err := evalExpr(x.Fn, env)
	if err != nil {
		return nil, err
	}
	args, err := evalArgs(x.Args, env)
	if err != nil {
		return nil, err
	}
	return call(fnVal, args)
}

// evalPipeExpr evaluates `left | right`: left is computed once, then
// appended as the final argument to right's call — `t | where(p)` runs
// where(p, t). An ExternalCall right-hand side gets left rendered to text
// (or passed through raw if it's already Bytes/String) as its stdin.
func evalPipeExpr(x *ast.PipeExpr, env *Env) (value.Value, error) {
	leftVal, err := evalExpr(x.Left, env)
	if err != nil {
		return nil, err
	}
	switch r := x.Right.(type) {
	case *ast.ExternalCall:
		return runExternal(r, leftVal, env)
	case *ast.Call:
		fnVal, err := evalExpr(r.Fn, env)
		if err != nil {
			return nil, err
		}
		args, err := evalArgs(r.Args, env)
		if err != nil {
			return nil, err
		}
		args = append(args, leftVal)
		return call(fnVal, args)
	default:
		fnVal, err := evalExpr(r, env)
		if err != nil {
			return nil, err
		}
		return call(fnVal, []value.Value{leftVal})
	}
}

func evalIf(x *ast.IfExpr, env *Env) (value.Value, error) {
	cond, err := evalExpr(x.Cond, env)
	if err != nil {
		return nil, err
	}
	if value.Truthy(cond) {
		return evalBlock(x.Then, NewEnv(env))
	}
	if x.Else != nil {
		return evalBlock(x.Else, NewEnv(env))
	}
	return value.Null{}, nil
}

// evalErrCheck implements postfix `?`: an ErrorVal result becomes a hard
// Go error (aborting the pipeline); anything else passes through unchanged.
func evalErrCheck(x *ast.ErrCheck, env *Env) (value.Value, error) {
	v, err := evalExpr(x.X, env)
	if err != nil {
		return nil, err
	}
	if ev, ok := v.(value.ErrorVal); ok {
		return nil, fmt.Errorf("%s", ev.Msg)
	}
	return v, nil
}

func evalUnary(x *ast.UnaryExpr, env *Env) (value.Value, error) {
	v, err := evalExpr(x.X, env)
	if err != nil {
		return nil, err
	}
	switch x.Op {
	case token.NOT:
		return value.Bool(!value.Truthy(v)), nil
	case token.MINUS:
		switch n := v.(type) {
		case value.Int:
			return -n, nil
		case value.Float:
			return -n, nil
		default:
			return nil, fmt.Errorf("cannot negate a %s", v.Kind())
		}
	default:
		return nil, fmt.Errorf("eval: unknown unary operator %s", x.Op)
	}
}

func evalBinary(x *ast.BinaryExpr, env *Env) (value.Value, error) {
	// && and || short-circuit, so the right side is evaluated lazily.
	if x.Op == token.AND || x.Op == token.OR {
		return evalLogical(x, env)
	}
	l, err := evalExpr(x.Left, env)
	if err != nil {
		return nil, err
	}
	r, err := evalExpr(x.Right, env)
	if err != nil {
		return nil, err
	}
	switch x.Op {
	case token.EQ:
		return value.Bool(value.Equal(l, r)), nil
	case token.NEQ:
		return value.Bool(!value.Equal(l, r)), nil
	case token.LT, token.GT, token.LE, token.GE:
		return evalComparison(x.Op, l, r)
	case token.PLUS, token.MINUS, token.STAR, token.SLASH, token.MOD:
		return evalArith(x.Op, l, r)
	default:
		return nil, fmt.Errorf("eval: unknown binary operator %s", x.Op)
	}
}

func evalLogical(x *ast.BinaryExpr, env *Env) (value.Value, error) {
	l, err := evalExpr(x.Left, env)
	if err != nil {
		return nil, err
	}
	if x.Op == token.AND && !value.Truthy(l) {
		return value.Bool(false), nil
	}
	if x.Op == token.OR && value.Truthy(l) {
		return value.Bool(true), nil
	}
	r, err := evalExpr(x.Right, env)
	if err != nil {
		return nil, err
	}
	return value.Bool(value.Truthy(r)), nil
}

func evalComparison(op token.Kind, l, r value.Value) (value.Value, error) {
	c, err := value.Compare(l, r)
	if err != nil {
		return nil, err
	}
	switch op {
	case token.LT:
		return value.Bool(c < 0), nil
	case token.GT:
		return value.Bool(c > 0), nil
	case token.LE:
		return value.Bool(c <= 0), nil
	case token.GE:
		return value.Bool(c >= 0), nil
	default:
		return nil, fmt.Errorf("eval: unknown comparison operator %s", op)
	}
}

// nsUnion special-cases '+' between two paths (or a union and a path) as
// namespace-union construction (`ns := /a + /b`) rather than arithmetic —
// checked before the numeric path in evalArith since Path/NSUnion aren't
// numbers at all.
func nsUnion(l, r value.Value) (value.Value, bool) {
	var paths []value.Path
	switch lv := l.(type) {
	case value.Path:
		paths = append(paths, lv)
	case value.NSUnion:
		paths = append(paths, lv.Paths...)
	default:
		return nil, false
	}
	switch rv := r.(type) {
	case value.Path:
		paths = append(paths, rv)
	case value.NSUnion:
		paths = append(paths, rv.Paths...)
	default:
		return nil, false
	}
	return value.NSUnion{Paths: paths}, true
}

func evalArith(op token.Kind, l, r value.Value) (value.Value, error) {
	if op == token.PLUS {
		if u, ok := nsUnion(l, r); ok {
			return u, nil
		}
		if ls, ok := l.(value.String); ok {
			if rs, ok := r.(value.String); ok {
				return ls + rs, nil
			}
		}
	}
	li, liOK := l.(value.Int)
	ri, riOK := r.(value.Int)
	if liOK && riOK {
		switch op {
		case token.PLUS:
			return li + ri, nil
		case token.MINUS:
			return li - ri, nil
		case token.STAR:
			return li * ri, nil
		case token.SLASH:
			if ri == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return li / ri, nil
		case token.MOD:
			if ri == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			return li % ri, nil
		}
	}
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if !lok || !rok {
		return nil, fmt.Errorf("cannot apply %s to %s and %s", op, l.Kind(), r.Kind())
	}
	switch op {
	case token.PLUS:
		return value.Float(lf + rf), nil
	case token.MINUS:
		return value.Float(lf - rf), nil
	case token.STAR:
		return value.Float(lf * rf), nil
	case token.SLASH:
		if rf == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		return value.Float(lf / rf), nil
	default:
		return nil, fmt.Errorf("eval: operator %s not defined for float operands", op)
	}
}

func toFloat(v value.Value) (float64, bool) {
	switch n := v.(type) {
	case value.Int:
		return float64(n), true
	case value.Float:
		return float64(n), true
	default:
		return 0, false
	}
}
