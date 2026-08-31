// Package ast defines kyu's abstract syntax tree.
package ast

import "github.com/sandgorgon/9sh/kyu/token"

type Node interface {
	node()
}

type Expr interface {
	Node
	exprNode()
}

type Stmt interface {
	Node
	stmtNode()
}

// Program is the root node: a sequence of statements.
type Program struct {
	Stmts []Stmt
}

func (*Program) node() {}

// ---- expressions ----

type Ident struct {
	Tok  token.Token
	Name string
}

type IntLit struct {
	Tok token.Token
	Val int64
}

type FloatLit struct {
	Tok token.Token
	Val float64
}

type StringLit struct {
	Tok token.Token
	Val string
}

type BoolLit struct {
	Tok token.Token
	Val bool
}

type NullLit struct {
	Tok token.Token
}

type DurationLit struct {
	Tok   token.Token
	Raw   string // e.g. "500ms"
	Nanos int64
}

type PathLit struct {
	Tok token.Token
	Val string
}

// RecordLit is a `{ field: expr, ... }` literal.
type RecordLit struct {
	Tok    token.Token
	Fields []RecordField
}

type RecordField struct {
	Name  string
	Value Expr
}

// TableLit is a `[ recordExpr, ... ]` literal — a sequence of records.
type TableLit struct {
	Tok  token.Token
	Rows []Expr
}

// ListLit is a `[ expr, ... ]` literal of arbitrary (non-record) values.
// The parser only distinguishes List vs. Table at eval time is unnecessary:
// syntactically both use '[' ']'; ListLit is kept for non-record elements.
type ListLit struct {
	Tok      token.Token
	Elements []Expr
}

// Closure is a `{ |params| body }` anonymous function, used as an argument
// to pipe-stage functions like where/select/each.
type Closure struct {
	Tok    token.Token
	Params []string
	Body   []Stmt
}

// FieldAccess is `expr.name`.
type FieldAccess struct {
	Tok   token.Token
	Recv  Expr
	Field string
}

// Call is `fn(args...)`.
type Call struct {
	Tok  token.Token
	Fn   Expr
	Args []Expr
}

// ExternalCall is `%cmd arg1 arg2 ...` — a legacy/external binary invocation.
type ExternalCall struct {
	Tok  token.Token
	Name string
	Args []Expr
}

// PipeExpr is `left | right`, where right must evaluate to a callable
// (typically a Call, whose piped-in value is appended as the call's final
// argument) or an ExternalCall.
type PipeExpr struct {
	Tok   token.Token
	Left  Expr
	Right Expr
}

// BinaryExpr covers arithmetic, comparison, and logical infix operators.
type BinaryExpr struct {
	Tok   token.Token
	Op    token.Kind
	Left  Expr
	Right Expr
}

// UnaryExpr covers `!x` and `-x`.
type UnaryExpr struct {
	Tok token.Token
	Op  token.Kind
	X   Expr
}

// ErrCheck is `expr?` — abort-on-first-error postfix operator.
type ErrCheck struct {
	Tok token.Token
	X   Expr
}

// IfExpr is `if cond { thenStmts } else { elseStmts }`; else is optional.
type IfExpr struct {
	Tok  token.Token
	Cond Expr
	Then []Stmt
	Else []Stmt // nil if no else clause
}

// Background is `%cmd args... &`: starts an external command as a job
// and evaluates to a live job record (its fields backed by the job's
// namespace files) rather than blocking for output like a bare
// ExternalCall. Scoped to ExternalCall only for now — backgrounding an
// arbitrary kyu call would mean a native-inproc job, which has no kyu
// syntax yet (see kyu/eval's job wiring).
type Background struct {
	Tok  token.Token // the '&'
	Call *ExternalCall
}

// AtHost is `@host { ... }` — runs the block's job creation (both `&` and
// foreground %cmd) against /n/<host>'s namespace instead of the local one.
// Per the design doc, this is the whole of "proxy jobs": no separate
// remote-job protocol exists or is needed, since /n/<host>/jobs/<id>/*
// already are the remote job's real files once host is bound (see
// `bind`) — evalAtHost just re-roots job creation for the block's
// duration (see Env.JobRoot).
type AtHost struct {
	Tok  token.Token // '@'
	Host string
	Body []Stmt
}

func (*AtHost) exprNode() {}
func (*AtHost) node()     {}

func (*Ident) exprNode()        {}
func (*IntLit) exprNode()       {}
func (*FloatLit) exprNode()     {}
func (*StringLit) exprNode()    {}
func (*BoolLit) exprNode()      {}
func (*NullLit) exprNode()      {}
func (*DurationLit) exprNode()  {}
func (*PathLit) exprNode()      {}
func (*RecordLit) exprNode()    {}
func (*TableLit) exprNode()     {}
func (*ListLit) exprNode()      {}
func (*Closure) exprNode()      {}
func (*FieldAccess) exprNode()  {}
func (*Call) exprNode()         {}
func (*ExternalCall) exprNode() {}
func (*PipeExpr) exprNode()     {}
func (*BinaryExpr) exprNode()   {}
func (*UnaryExpr) exprNode()    {}
func (*ErrCheck) exprNode()     {}
func (*IfExpr) exprNode()       {}
func (*Background) exprNode()   {}

func (*Ident) node()        {}
func (*IntLit) node()       {}
func (*FloatLit) node()     {}
func (*StringLit) node()    {}
func (*BoolLit) node()      {}
func (*NullLit) node()      {}
func (*DurationLit) node()  {}
func (*PathLit) node()      {}
func (*RecordLit) node()    {}
func (*TableLit) node()     {}
func (*ListLit) node()      {}
func (*Closure) node()      {}
func (*FieldAccess) node()  {}
func (*Call) node()         {}
func (*ExternalCall) node() {}
func (*PipeExpr) node()     {}
func (*BinaryExpr) node()   {}
func (*UnaryExpr) node()    {}
func (*ErrCheck) node()     {}
func (*IfExpr) node()       {}
func (*Background) node()   {}

// ---- statements ----

// ExprStmt is a bare expression used as a statement.
type ExprStmt struct {
	X Expr
}

// DefineStmt is `name := expr`.
type DefineStmt struct {
	NameTok token.Token // the identifier's own token — the statement's true start
	Tok     token.Token // ':=' — kept for callers that specifically want the operator
	Name    string
	Val     Expr
}

// AssignStmt is `target = expr`, where target is an Ident or FieldAccess
// (field assignment is what makes namespace-backed fields write-through).
type AssignStmt struct {
	Tok    token.Token
	Target Expr
	Val    Expr
}

// PassthroughStmt is `$cmd arg1 arg2 ...` — an external command run with
// 9sh's own stdin/stdout/stderr connected directly, not through /jobs:
// no job record, no output capture, no session history. It exists for
// programs %cmd (ExternalCall) can't support at all — anything needing a
// live TTY (vim, ssh, a REPL) or output that must stream as it happens —
// since ExternalCall always routes through a job whose stdin/stdout/
// stderr are in-memory buffers (job.go), never the real terminal.
// Statement-only, like BindStmt: it produces no capturable kyu value, so
// unlike ExternalCall it's never offered as a primary-expression option
// in parsePrefix — only parseStmt reaches parsePassthroughStmt — which
// makes it structurally impossible to appear inside a pipe or as an
// operand of a larger expression, rather than merely discouraged.
type PassthroughStmt struct {
	Tok  token.Token
	Name string
	Args []Expr
}

// BindStmt is `bind SRC, DST[, before|after|replace]` — a namespace verb,
// a real keyword (not an ordinary function) per kyu's design: it mutates
// the calling process's own namespace. Disposition defaults to "replace"
// when omitted. Src may be a namespace-union expression (`a + b`),
// evaluating to an NSUnion rather than a single Path.
type BindStmt struct {
	Tok         token.Token
	Src         Expr
	Dst         Expr
	Disposition string // "before" | "after" | "replace"
}

func (*ExprStmt) stmtNode()        {}
func (*DefineStmt) stmtNode()      {}
func (*AssignStmt) stmtNode()      {}
func (*BindStmt) stmtNode()        {}
func (*PassthroughStmt) stmtNode() {}

func (*ExprStmt) node()        {}
func (*DefineStmt) node()      {}
func (*AssignStmt) node()      {}
func (*BindStmt) node()        {}
func (*PassthroughStmt) node() {}
