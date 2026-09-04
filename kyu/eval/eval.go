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
	// cd needs the calling Env itself (to call SetCwd) — same
	// closure-capture shape as checkout above. See cd.go's biCd doc
	// comment for why this is a per-Env value, not a real os.Chdir().
	env.Define("cd", &Builtin{Name: "cd", Fn: func(args []value.Value) (value.Value, error) {
		return biCd(env, args)
	}})
	// pwd needs the calling Env itself (to read Cwd()) — same
	// closure-capture shape as cd above. See cd.go's biPwd doc comment.
	env.Define("pwd", &Builtin{Name: "pwd", Fn: func(args []value.Value) (value.Value, error) {
		return biPwd(env, args)
	}})
	// getenv/setenv/unsetenv need the calling Env's namespace — same
	// closure-capture shape as cd/checkout above. See envvars.go.
	env.Define("getenv", &Builtin{Name: "getenv", Fn: func(args []value.Value) (value.Value, error) {
		return biGetenv(env, args)
	}})
	env.Define("setenv", &Builtin{Name: "setenv", Fn: func(args []value.Value) (value.Value, error) {
		return biSetenv(env, args)
	}})
	env.Define("unsetenv", &Builtin{Name: "unsetenv", Fn: func(args []value.Value) (value.Value, error) {
		return biUnsetenv(env, args)
	}})
	// glob needs the calling Env's namespace — same closure-capture
	// shape as cd/checkout above. See glob.go's biGlob doc comment.
	env.Define("glob", &Builtin{Name: "glob", Fn: func(args []value.Value) (value.Value, error) {
		return biGlob(env, args)
	}})
	// exit_code needs the calling Env's LastExitCode -- bash's $?
	// equivalent; see Env.SetLastExitCode's doc comment.
	env.Define("exit_code", &Builtin{Name: "exit_code", Fn: func(args []value.Value) (value.Value, error) {
		if len(args) != 0 {
			return nil, fmt.Errorf("exit_code: expected no arguments, got %d", len(args))
		}
		code := env.LastExitCode()
		if code == nil {
			return value.Null{}, nil
		}
		return value.Int(*code), nil
	}})
	// vars/unset need the calling Env itself (to walk/mutate its scope
	// chain) — same closure-capture shape as cd/checkout above. See
	// vars.go's biVars/biUnset doc comments.
	env.Define("vars", &Builtin{Name: "vars", Fn: func(args []value.Value) (value.Value, error) {
		return biVars(env, args)
	}})
	env.Define("unset", &Builtin{Name: "unset", Fn: func(args []value.Value) (value.Value, error) {
		return biUnset(env, args)
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
	case *ast.UnbindStmt:
		return evalUnbindStmt(st, env)
	case *ast.PassthroughStmt:
		return evalPassthroughStmt(st, env)
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
	case *ast.WhileExpr:
		return evalWhile(x, env)
	case *ast.BreakExpr:
		return nil, breakSignal{}
	case *ast.ContinueExpr:
		return nil, continueSignal{}
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

// breakSignal/continueSignal are sentinel errors evalWhile uses to unwind
// out of a loop body when it hits `break`/`continue` — the same
// "ordinary Go error propagates up through evalBlock/evalStmt until
// something catches it" mechanism evalErrCheck already relies on for
// abort-on-first-error, just caught here specifically instead of
// surfacing to the caller. A break/continue outside any loop propagates
// all the way up as an ordinary eval error, which is the right behavior
// — there's nothing valid to unwind to.
type breakSignal struct{}

func (breakSignal) Error() string { return "break outside a loop" }

type continueSignal struct{}

func (continueSignal) Error() string { return "continue outside a loop" }

// evalWhile runs ast.WhileExpr: re-evaluates Cond before each iteration,
// running Body in a fresh child scope (matching evalIf's Then/Else) as
// long as it's truthy. A nested while's break/continue is caught by its
// own innermost evalWhile — it never bubbles out to an enclosing loop.
func evalWhile(x *ast.WhileExpr, env *Env) (value.Value, error) {
	for {
		cond, err := evalExpr(x.Cond, env)
		if err != nil {
			return nil, err
		}
		if !value.Truthy(cond) {
			return value.Null{}, nil
		}
		_, err = evalBlock(x.Body, NewEnv(env))
		if err != nil {
			if _, ok := err.(breakSignal); ok {
				return value.Null{}, nil
			}
			if _, ok := err.(continueSignal); ok {
				continue
			}
			return nil, err
		}
	}
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

// evalLogical implements `&&`/`||`, short-circuiting on the same
// "truth" evalTruth computes for each operand — ordinary value.Truthy
// for a plain expression, but a bare %cmd operand's truth is its own
// exit code instead (see evalTruth's doc comment): `%grep "x" f &&
// %echo "found"` only echoes if grep actually exited 0, not merely
// because it produced some (possibly empty, always-truthy-as-Bytes)
// stdout. Always returns a plain Bool, in both modes — restructure
// with `if` if you want the actual value of whichever side ran.
func evalLogical(x *ast.BinaryExpr, env *Env) (value.Value, error) {
	lOK, err := evalTruth(x.Left, env)
	if err != nil {
		return nil, err
	}
	if x.Op == token.AND && !lOK {
		return value.Bool(false), nil
	}
	if x.Op == token.OR && lOK {
		return value.Bool(true), nil
	}
	rOK, err := evalTruth(x.Right, env)
	if err != nil {
		return nil, err
	}
	return value.Bool(rOK), nil
}

// evalTruth evaluates e and reports its "truth" for `&&`/`||`. A bare
// %cmd call (*ast.ExternalCall) — checked at the AST level, not by
// inspecting the runtime value, so a %cmd result already captured in a
// variable (`x := %cmd1; x && ...`) is unaffected and uses ordinary
// value.Truthy like anything else — is true only if it actually exited
// 0: a failure to even start the process is an ErrorVal (checked
// directly, since a start failure never updates LastExitCode at all),
// and a process that started but exited non-zero is checked via
// env.LastExitCode(), which the same evalExpr call just below updated
// synchronously (runExternalDirect/runExternalViaJob/evalPassthroughStmt
// all set it before returning). This composes correctly across a
// longer chain (`%cmd1 && %cmd2 && %cmd3`, parsed left-associatively as
// `(%cmd1 && %cmd2) && %cmd3`) without any special-casing beyond
// "check whichever operand a given evalLogical call is looking at":
// the inner pair's result is a plain Bool, whose ordinary truthiness
// already reflects whether that inner chain succeeded, and $cmd can't
// appear here at all since it's a statement, not an expression.
func evalTruth(e ast.Expr, env *Env) (bool, error) {
	v, err := evalExpr(e, env)
	if err != nil {
		return false, err
	}
	if _, ok := e.(*ast.ExternalCall); ok {
		if _, isErr := v.(value.ErrorVal); isErr {
			return false, nil
		}
		code := env.LastExitCode()
		return code != nil && *code == 0, nil
	}
	return value.Truthy(v), nil
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
