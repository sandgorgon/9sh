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

// ---- statements ----

// ExprStmt is a bare expression used as a statement.
type ExprStmt struct {
	X Expr
}

// DefineStmt is `name := expr`.
type DefineStmt struct {
	Tok  token.Token
	Name string
	Val  Expr
}

// AssignStmt is `target = expr`, where target is an Ident or FieldAccess
// (field assignment is what makes namespace-backed fields write-through).
type AssignStmt struct {
	Tok    token.Token
	Target Expr
	Val    Expr
}

func (*ExprStmt) stmtNode()   {}
func (*DefineStmt) stmtNode() {}
func (*AssignStmt) stmtNode() {}

func (*ExprStmt) node()   {}
func (*DefineStmt) node() {}
func (*AssignStmt) node() {}
