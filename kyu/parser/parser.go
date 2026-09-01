// Package parser builds a kyu ast.Program from a token stream using a Pratt
// (precedence-climbing) expression parser.
package parser

import (
	"fmt"
	"strconv"
	"time"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/lexer"
	"github.com/sandgorgon/9sh/kyu/token"
)

// BracketDepth lexes src and returns its net paren/brace/bracket depth
// — 0 means src is a syntactically complete-enough unit to attempt
// parsing (no unclosed delimiter), positive means more input is needed
// (an interactive REPL should keep accumulating), matching Go/JS-style
// "did the user press Enter mid-expression" detection. It's a shared
// utility, not parser-specific state: both cmd/9sh's line REPL and
// kyu's native tui REPL pane use it to decide when to submit.
func BracketDepth(src string) int {
	l := lexer.New(src)
	depth := 0
	for {
		tok := l.Next()
		switch tok.Kind {
		case token.LPAREN, token.LBRACE, token.LBRACKET:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACKET:
			depth--
		case token.EOF:
			return depth
		}
	}
}

type Parser struct {
	l *lexer.Lexer

	cur  token.Token
	peek token.Token

	errs []error
}

func New(src string) *Parser {
	p := &Parser{l: lexer.New(src)}
	p.next()
	p.next()
	return p
}

func (p *Parser) next() {
	p.cur = p.peek
	p.peek = p.l.Next()
}

func (p *Parser) Errors() []error { return p.errs }

func (p *Parser) errorf(format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf("line %d: %s", p.cur.Line, fmt.Sprintf(format, args...)))
}

// ParseProgram parses a full source file/script into a Program.
func (p *Parser) ParseProgram() *ast.Program {
	prog := &ast.Program{}
	p.skipTerminators()
	for p.cur.Kind != token.EOF {
		if stmt := p.parseStmt(); stmt != nil {
			prog.Stmts = append(prog.Stmts, stmt)
		}
		// parseStmt (on success or failure) always leaves cur on the last
		// token it looked at, never past it — advance once here so the
		// loop doesn't reparse the same token forever.
		p.next()
		p.skipTerminators()
	}
	return prog
}

func (p *Parser) skipTerminators() {
	for p.cur.Kind == token.NEWLINE || p.cur.Kind == token.SEMI {
		p.next()
	}
}

func (p *Parser) parseStmt() ast.Stmt {
	if p.cur.Kind == token.BIND {
		return p.parseBindStmt()
	}
	if p.cur.Kind == token.UNBIND {
		return p.parseUnbindStmt()
	}
	if p.cur.Kind == token.DOLLAR {
		return p.parsePassthroughStmt()
	}
	if p.cur.Kind == token.IDENT && p.peek.Kind == token.DEFINE {
		return p.parseDefineStmt()
	}
	expr := p.parseValueExpr()
	if expr == nil {
		return nil
	}
	// parseExpr leaves cur on expr's last token, so the lookahead for a
	// trailing '=' is on peek, not cur.
	if p.peek.Kind == token.ASSIGN {
		if !isAssignable(expr) {
			p.errorf("invalid assignment target")
			return nil
		}
		p.next() // cur: expr's last token -> '='
		tok := p.cur
		p.next() // cur: '=' -> start of RHS
		val := p.parseValueExpr()
		return &ast.AssignStmt{Tok: tok, Target: expr, Val: val}
	}
	return &ast.ExprStmt{X: expr}
}

func isAssignable(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Ident, *ast.FieldAccess:
		return true
	}
	return false
}

func (p *Parser) parseDefineStmt() ast.Stmt {
	name := p.cur
	p.next() // consume ident
	tok := p.cur
	p.next() // consume :=
	val := p.parseValueExpr()
	return &ast.DefineStmt{NameTok: name, Tok: tok, Name: name.Literal, Val: val}
}

// parseValueExpr parses one expression and, if it's immediately followed
// by '&', wraps it as a Background — kyu's job-backgrounding sugar. This
// is checked here (at every place a statement's value-producing
// expression is parsed: a define's RHS, an assignment's RHS, and a bare
// expression statement) rather than as a general infix/postfix operator,
// since '&' is a statement-shaped verb ("run this as a job"), not
// something that composes inside a larger expression.
func (p *Parser) parseValueExpr() ast.Expr {
	expr := p.parseExpr(LOWEST)
	if expr == nil {
		return nil
	}
	if p.peek.Kind != token.AMP {
		return expr
	}
	ext, ok := expr.(*ast.ExternalCall)
	if !ok {
		p.errorf("'&' (background) is only supported on an external command call (%%cmd), got %T", expr)
		return nil
	}
	p.next() // cur: expr's last token -> '&'
	return &ast.Background{Tok: p.cur, Call: ext}
}

// parseBindStmt parses `bind SRC, DST[, before|after|replace]`. SRC and
// DST are comma-separated, not bare-whitespace-juxtaposed as the design
// doc first sketched: the lexer decides '/' vs division from only the
// preceding token, and a DST path starting with '/' right after SRC ends
// in an identifier (e.g. a namespace-union `a + b`) would otherwise
// re-lex as division (`b / dst`) — exactly Phase 1's PATH-vs-division
// issue one level removed. A comma is never ambiguous with anything, and
// matches how every other multi-value kyu construct (record/list/call
// args) already separates elements.
func (p *Parser) parseBindStmt() ast.Stmt {
	tok := p.cur
	p.next() // consume 'bind'
	src := p.parseExpr(LOWEST)
	if src == nil {
		return nil
	}
	if !p.expectPeekOrCur(token.COMMA) {
		return nil
	}
	p.next() // consume ',' -> start of DST
	dst := p.parseExpr(LOWEST)
	if dst == nil {
		return nil
	}
	disp := "replace"
	if p.peek.Kind == token.COMMA {
		p.next() // cur: dst's last token -> ','
		p.next() // consume ',' -> disposition ident
		if p.cur.Kind != token.IDENT || !isDispositionWord(p.cur.Literal) {
			p.errorf("expected a disposition (before/after/replace), got %s(%q)", p.cur.Kind, p.cur.Literal)
			return nil
		}
		disp = p.cur.Literal
	}
	return &ast.BindStmt{Tok: tok, Src: src, Dst: dst, Disposition: disp}
}

// parseUnbindStmt parses `unbind DST` — a single expression, unlike
// bind's SRC, DST[, disposition], since there's nothing to graft and no
// disposition to choose.
func (p *Parser) parseUnbindStmt() ast.Stmt {
	tok := p.cur
	p.next() // consume 'unbind'
	dst := p.parseExpr(LOWEST)
	if dst == nil {
		return nil
	}
	return &ast.UnbindStmt{Tok: tok, Dst: dst}
}

func isDispositionWord(s string) bool {
	return s == "before" || s == "after" || s == "replace"
}

// parseBlock parses statements up to (not consuming) a closing '}'.
func (p *Parser) parseBlock() []ast.Stmt {
	var stmts []ast.Stmt
	p.skipTerminators()
	for p.cur.Kind != token.RBRACE && p.cur.Kind != token.EOF {
		if s := p.parseStmt(); s != nil {
			stmts = append(stmts, s)
		}
		// see ParseProgram: parseStmt never advances past its last token.
		p.next()
		p.skipTerminators()
	}
	return stmts
}

// ---- precedence-climbing expression parser ----

type precedence int

const (
	LOWEST precedence = iota
	PIPE_
	OR_
	AND_
	EQUALITY
	RELATIONAL
	ADDITIVE
	MULTIPLICATIVE
	POSTFIX_ // '?' and field access binds tighter than binary ops
	CALL_
)

var precedences = map[token.Kind]precedence{
	token.PIPE:     PIPE_,
	token.OR:       OR_,
	token.AND:      AND_,
	token.EQ:       EQUALITY,
	token.NEQ:      EQUALITY,
	token.LT:       RELATIONAL,
	token.GT:       RELATIONAL,
	token.LE:       RELATIONAL,
	token.GE:       RELATIONAL,
	token.PLUS:     ADDITIVE,
	token.MINUS:    ADDITIVE,
	token.STAR:     MULTIPLICATIVE,
	token.SLASH:    MULTIPLICATIVE,
	token.MOD:      MULTIPLICATIVE,
	token.LPAREN:   CALL_,
	token.DOT:      CALL_,
	token.QUESTION: CALL_,
}

func (p *Parser) peekPrecedence() precedence {
	if pr, ok := precedences[p.peek.Kind]; ok {
		return pr
	}
	return LOWEST
}

func (p *Parser) parseExpr(prec precedence) ast.Expr {
	left := p.parsePrefix()
	if left == nil {
		return nil
	}
	for p.peek.Kind != token.NEWLINE && p.peek.Kind != token.SEMI && prec < p.peekPrecedence() {
		p.next()
		left = p.parseInfix(left)
		if left == nil {
			return nil
		}
	}
	return left
}

func (p *Parser) parsePrefix() ast.Expr {
	switch p.cur.Kind {
	case token.IDENT:
		return p.parseIdentOrCall()
	case token.INT:
		return p.parseIntLit()
	case token.FLOAT:
		return p.parseFloatLit()
	case token.STRING:
		return &ast.StringLit{Tok: p.cur, Val: p.cur.Literal}
	case token.DURATION:
		return p.parseDurationLit()
	case token.PATH:
		return &ast.PathLit{Tok: p.cur, Val: p.cur.Literal}
	case token.TRUE:
		return &ast.BoolLit{Tok: p.cur, Val: true}
	case token.FALSE:
		return &ast.BoolLit{Tok: p.cur, Val: false}
	case token.NULL:
		return &ast.NullLit{Tok: p.cur}
	case token.NOT, token.MINUS:
		return p.parseUnary()
	case token.LPAREN:
		return p.parseGroupedExpr()
	case token.LBRACE:
		return p.parseBraceExpr()
	case token.LBRACKET:
		return p.parseListOrTableLit()
	case token.PERCENT:
		return p.parseExternalCall()
	case token.IF:
		return p.parseIfExpr()
	case token.WHILE:
		return p.parseWhileExpr()
	case token.BREAK:
		return &ast.BreakExpr{Tok: p.cur}
	case token.CONTINUE:
		return &ast.ContinueExpr{Tok: p.cur}
	case token.AT:
		return p.parseAtHost()
	default:
		p.errorf("unexpected token %s(%q)", p.cur.Kind, p.cur.Literal)
		return nil
	}
}

func (p *Parser) parseIdentOrCall() ast.Expr {
	ident := &ast.Ident{Tok: p.cur, Name: p.cur.Literal}
	return ident
}

func (p *Parser) parseIntLit() ast.Expr {
	v, err := strconv.ParseInt(p.cur.Literal, 10, 64)
	if err != nil {
		p.errorf("invalid int literal %q: %v", p.cur.Literal, err)
		return nil
	}
	return &ast.IntLit{Tok: p.cur, Val: v}
}

func (p *Parser) parseFloatLit() ast.Expr {
	v, err := strconv.ParseFloat(p.cur.Literal, 64)
	if err != nil {
		p.errorf("invalid float literal %q: %v", p.cur.Literal, err)
		return nil
	}
	return &ast.FloatLit{Tok: p.cur, Val: v}
}

func (p *Parser) parseDurationLit() ast.Expr {
	lit := p.cur.Literal
	unit := lit
	for len(unit) > 0 && (unit[0] >= '0' && unit[0] <= '9' || unit[0] == '.') {
		unit = unit[1:]
	}
	d, err := time.ParseDuration(lit)
	if err != nil {
		p.errorf("invalid duration literal %q: %v", lit, err)
		return nil
	}
	return &ast.DurationLit{Tok: p.cur, Raw: lit, Nanos: int64(d)}
}

func (p *Parser) parseUnary() ast.Expr {
	tok := p.cur
	op := p.cur.Kind
	p.next()
	x := p.parseExpr(POSTFIX_)
	return &ast.UnaryExpr{Tok: tok, Op: op, X: x}
}

func (p *Parser) parseGroupedExpr() ast.Expr {
	p.next() // consume '('
	expr := p.parseExpr(LOWEST)
	if !p.expectPeekOrCur(token.RPAREN) {
		return nil
	}
	return expr
}

// expectPeekOrCur consumes cur if it already is k; otherwise requires peek
// to be k and advances onto it. Used after parseExpr, which leaves cur on
// the expression's last token.
func (p *Parser) expectPeekOrCur(k token.Kind) bool {
	if p.cur.Kind == k {
		return true
	}
	if p.peek.Kind != k {
		p.errorf("expected %s, got %s(%q)", k, p.peek.Kind, p.peek.Literal)
		return false
	}
	p.next()
	return true
}

// parseBraceExpr disambiguates a Closure `{ |params| body }` / `{ body }`
// from a RecordLit `{ field: expr, ... }` by lookahead: a RecordLit's first
// token (when non-empty) is always IDENT ':'.
func (p *Parser) parseBraceExpr() ast.Expr {
	tok := p.cur
	if p.peek.Kind == token.RBRACE {
		p.next() // consume '}'
		return &ast.RecordLit{Tok: tok}
	}
	if p.peek.Kind == token.PIPE {
		return p.parseClosure(tok)
	}
	// Try record literal: IDENT ':' ...
	if p.peek.Kind == token.IDENT {
		return p.parseRecordOrBareClosure(tok)
	}
	// otherwise: a closure body with no explicit params
	return p.parseClosureBody(tok, nil)
}

func (p *Parser) parseRecordOrBareClosure(tok token.Token) ast.Expr {
	p.next() // move onto first IDENT
	nameTok := p.cur
	if p.peek.Kind == token.COLON {
		return p.parseRecordLitFrom(tok, nameTok)
	}
	// Not `ident:` — this is a closure body starting with an identifier
	// expression; reparse cur as the start of a statement/expression.
	return p.parseClosureBodyFromCur(tok, nil)
}

func (p *Parser) parseRecordLitFrom(tok token.Token, firstName token.Token) ast.Expr {
	rec := &ast.RecordLit{Tok: tok}
	name := firstName.Literal
	p.next() // consume ident, cur = ':'
	p.next() // consume ':', cur = start of value expr
	val := p.parseExpr(LOWEST)
	rec.Fields = append(rec.Fields, ast.RecordField{Name: name, Value: val})
	for p.peek.Kind == token.COMMA {
		p.next() // consume value's last token -> now at ','... actually move to ','
		p.next() // consume ','
		if p.cur.Kind == token.RBRACE {
			break
		}
		fname := p.cur.Literal
		if !p.expectPeekOrCur(token.COLON) {
			return nil
		}
		p.next() // consume ':'
		fval := p.parseExpr(LOWEST)
		rec.Fields = append(rec.Fields, ast.RecordField{Name: fname, Value: fval})
	}
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	return rec
}

func (p *Parser) parseClosure(tok token.Token) ast.Expr {
	p.next() // cur: '{' -> opening '|'
	p.next() // cur: opening '|' -> first param (or closing '|' if none)
	var params []ast.Param
	sawDefault := false
	for p.cur.Kind == token.IDENT {
		name := p.cur.Literal
		var def ast.Expr
		if p.peek.Kind == token.ASSIGN {
			p.next() // cur: name -> '='
			p.next() // cur: '=' -> start of default expr
			// PIPE_, not LOWEST: '|' is both the closure param list's own
			// closing delimiter and the pipe infix operator, so parsing
			// at LOWEST would swallow the closing '|' as a pipe
			// continuation instead of stopping there.
			def = p.parseExpr(PIPE_)
			if def == nil {
				return nil
			}
			sawDefault = true
		} else if sawDefault {
			p.errorf("parameter %q has no default, but an earlier parameter does — defaults must trail", name)
			return nil
		}
		params = append(params, ast.Param{Name: name, Default: def})
		p.next() // cur: param name, or default expr's last token -> ',' or '|'
		if p.cur.Kind == token.COMMA {
			p.next()
		}
	}
	if p.cur.Kind != token.PIPE {
		p.errorf("expected '|' to close closure params, got %s", p.cur.Kind)
		return nil
	}
	p.next() // consume closing '|'
	return p.parseClosureBodyFromCur(tok, params)
}

func (p *Parser) parseClosureBody(tok token.Token, params []ast.Param) ast.Expr {
	p.next() // move onto first body token
	return p.parseClosureBodyFromCur(tok, params)
}

func (p *Parser) parseClosureBodyFromCur(tok token.Token, params []ast.Param) ast.Expr {
	body := p.parseBlock()
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	return &ast.Closure{Tok: tok, Params: params, Body: body}
}

func (p *Parser) parseListOrTableLit() ast.Expr {
	tok := p.cur
	p.next() // consume '['
	var elems []ast.Expr
	for p.cur.Kind != token.RBRACKET && p.cur.Kind != token.EOF {
		p.skipTerminators()
		if p.cur.Kind == token.RBRACKET {
			break
		}
		elems = append(elems, p.parseExpr(LOWEST))
		p.next()
		p.skipTerminators()
		if p.cur.Kind == token.COMMA {
			p.next()
			p.skipTerminators()
		}
	}
	if p.cur.Kind != token.RBRACKET {
		p.errorf("expected ']', got %s", p.cur.Kind)
		return nil
	}
	allRecords := len(elems) > 0
	for _, e := range elems {
		if _, ok := e.(*ast.RecordLit); !ok {
			allRecords = false
			break
		}
	}
	if allRecords {
		return &ast.TableLit{Tok: tok, Rows: elems}
	}
	return &ast.ListLit{Tok: tok, Elements: elems}
}

// endsExternalCallArgs reports whether k can never start another %cmd
// argument — an allowlist-shaped check (list what legitimately continues
// argument parsing... inverted to what stops it) so a new operator like
// '&' doesn't silently get swallowed as an argument the way it did before
// this helper existed.
func endsExternalCallArgs(k token.Kind) bool {
	switch k {
	case token.NEWLINE, token.SEMI, token.EOF, token.PIPE, token.AMP,
		token.RPAREN, token.RBRACE, token.RBRACKET, token.COMMA:
		return true
	}
	return false
}

func (p *Parser) parseExternalCall() ast.Expr {
	tok := p.cur
	p.next() // consume '%'
	if p.cur.Kind != token.IDENT {
		p.errorf("expected command name after '%%', got %s", p.cur.Kind)
		return nil
	}
	name := p.cur.Literal
	call := &ast.ExternalCall{Tok: tok, Name: name}
	for !endsExternalCallArgs(p.peek.Kind) {
		p.next()
		arg := p.parsePrefix()
		if arg == nil {
			return nil
		}
		call.Args = append(call.Args, arg)
	}
	return call
}

// parsePassthroughStmt parses `$cmd arg1 arg2 ...` — see
// ast.PassthroughStmt's doc comment for why this is a Stmt, not an Expr
// (and thus never reached from parsePrimary/parseExpr). Argument parsing
// mirrors parseExternalCall exactly, including reuse of
// endsExternalCallArgs for the same argument/statement boundary.
func (p *Parser) parsePassthroughStmt() ast.Stmt {
	tok := p.cur
	p.next() // consume '$'
	if p.cur.Kind != token.IDENT {
		p.errorf("expected command name after '$', got %s", p.cur.Kind)
		return nil
	}
	name := p.cur.Literal
	call := &ast.PassthroughStmt{Tok: tok, Name: name}
	for !endsExternalCallArgs(p.peek.Kind) {
		p.next()
		arg := p.parsePrefix()
		if arg == nil {
			return nil
		}
		call.Args = append(call.Args, arg)
	}
	if p.peek.Kind == token.AMP {
		p.errorf("'&' is not supported on $cmd — it already runs directly against the terminal, not through /jobs")
		return nil
	}
	return call
}

func (p *Parser) parseIfExpr() ast.Expr {
	tok := p.cur
	p.next() // consume 'if'
	cond := p.parseExpr(LOWEST)
	if !p.expectPeekOrCur(token.LBRACE) {
		return nil
	}
	p.next() // consume '{'
	thenStmts := p.parseBlock()
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	ie := &ast.IfExpr{Tok: tok, Cond: cond, Then: thenStmts}
	if p.peek.Kind == token.ELSE {
		p.next() // cur: '}' -> ELSE
		p.next() // cur: ELSE -> '{'
		if p.cur.Kind != token.LBRACE {
			p.errorf("expected '{' after else, got %s", p.cur.Kind)
			return nil
		}
		p.next() // consume '{'
		ie.Else = p.parseBlock()
		if !p.expectPeekOrCur(token.RBRACE) {
			return nil
		}
	}
	return ie
}

// parseWhileExpr parses `while cond { body }` — identical shape to
// parseIfExpr, minus the else branch.
func (p *Parser) parseWhileExpr() ast.Expr {
	tok := p.cur
	p.next() // consume 'while'
	cond := p.parseExpr(LOWEST)
	if !p.expectPeekOrCur(token.LBRACE) {
		return nil
	}
	p.next() // consume '{'
	body := p.parseBlock()
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	return &ast.WhileExpr{Tok: tok, Cond: cond, Body: body}
}

// parseAtHost parses `@host { ... }`. host is a bareword identifier
// directly after '@', the same treatment %cmd's external command name
// gets after '%' — exempt from the usual bareword-ambiguity concerns
// since nothing infix-operator-shaped can follow '@' at this position.
func (p *Parser) parseAtHost() ast.Expr {
	tok := p.cur // '@'
	p.next()     // consume '@' -> host ident
	if p.cur.Kind != token.IDENT {
		p.errorf("expected a host name after '@', got %s(%q)", p.cur.Kind, p.cur.Literal)
		return nil
	}
	host := p.cur.Literal
	if !p.expectPeekOrCur(token.LBRACE) {
		return nil
	}
	p.next() // consume '{'
	body := p.parseBlock()
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	return &ast.AtHost{Tok: tok, Host: host, Body: body}
}

func (p *Parser) parseInfix(left ast.Expr) ast.Expr {
	switch p.cur.Kind {
	case token.DOT:
		return p.parseFieldAccess(left)
	case token.LPAREN:
		return p.parseCall(left)
	case token.QUESTION:
		return &ast.ErrCheck{Tok: p.cur, X: left}
	case token.PIPE:
		return p.parsePipe(left)
	default:
		return p.parseBinary(left)
	}
}

func (p *Parser) parseFieldAccess(left ast.Expr) ast.Expr {
	tok := p.cur
	if !p.expectPeekOrCur(token.IDENT) {
		return nil
	}
	return &ast.FieldAccess{Tok: tok, Recv: left, Field: p.cur.Literal}
}

func (p *Parser) parseCall(fn ast.Expr) ast.Expr {
	tok := p.cur
	p.next() // consume '('
	var args []ast.Expr
	for p.cur.Kind != token.RPAREN && p.cur.Kind != token.EOF {
		p.skipTerminators()
		if p.cur.Kind == token.RPAREN {
			break
		}
		args = append(args, p.parseExpr(LOWEST))
		p.next()
		p.skipTerminators()
		if p.cur.Kind == token.COMMA {
			p.next()
			p.skipTerminators()
		}
	}
	if p.cur.Kind != token.RPAREN {
		p.errorf("expected ')', got %s", p.cur.Kind)
		return nil
	}
	return &ast.Call{Tok: tok, Fn: fn, Args: args}
}

func (p *Parser) parsePipe(left ast.Expr) ast.Expr {
	tok := p.cur
	p.next() // consume '|'
	right := p.parsePipeRHS()
	return &ast.PipeExpr{Tok: tok, Left: left, Right: right}
}

// parsePipeRHS special-cases "ident { ... }" immediately after a pipe as
// sugar for a single-argument call, e.g. `where { |j| ... }` instead of
// `where({|j| ...})`. This sugar is intentionally scoped to pipe-RHS
// position only (not offered generally in parsePrefix) because a general
// "ident directly followed by '{' is a call" rule would swallow the body
// brace of `if cond { ... }` whenever cond is a bare identifier.
func (p *Parser) parsePipeRHS() ast.Expr {
	if p.cur.Kind == token.IDENT && p.peek.Kind == token.LBRACE {
		tok := p.cur
		fn := &ast.Ident{Tok: p.cur, Name: p.cur.Literal}
		p.next() // cur: ident -> '{'
		arg := p.parseBraceExpr()
		return &ast.Call{Tok: tok, Fn: fn, Args: []ast.Expr{arg}}
	}
	return p.parseExpr(PIPE_)
}

func (p *Parser) parseBinary(left ast.Expr) ast.Expr {
	tok := p.cur
	op := p.cur.Kind
	curPrec := precedences[op]
	p.next()
	right := p.parseExpr(curPrec)
	return &ast.BinaryExpr{Tok: tok, Op: op, Left: left, Right: right}
}
