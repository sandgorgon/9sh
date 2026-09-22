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

	// isNativeProgram, when set (via WithNativeProgramLookup), reports
	// whether a bareword IDENT names a live native program (see
	// kyu/eval's isNativeProgram) -- checked in parsePrefix's IDENT case
	// before falling into the ordinary parseIdentOrCall path. nil (the
	// default) means no native programs are recognized.
	isNativeProgram func(name string) bool
}

// Option configures a Parser at construction time -- New's cur/peek
// priming below happens before New returns, so anything that needs to
// affect the very first token(s) (isNativeProgram included: it also has
// to reach this Parser's own internal Lexer, see WithNativeProgramLookup)
// must be applied before that priming, not after New returns a *Parser a
// caller could otherwise configure. A plain post-construction field
// works fine for Lexer (see lexer.Lexer.NativeProgramLookup) since
// Lexer.New does no such priming itself.
type Option func(*Parser)

// WithNativeProgramLookup wires fn into both this Parser's own bareword
// recognition and its internal Lexer's identical hazard (a native name
// right before a bare Path argument -- see
// lexer.Lexer.NativeProgramLookup's doc comment for why that needs
// lexer-level cooperation too). Omit it (every existing New caller) for
// the old behavior: no native programs recognized.
func WithNativeProgramLookup(fn func(name string) bool) Option {
	return func(p *Parser) {
		p.isNativeProgram = fn
		p.l.NativeProgramLookup = fn
	}
}

func New(src string, opts ...Option) *Parser {
	p := &Parser{l: lexer.New(src)}
	for _, opt := range opts {
		opt(p)
	}
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
// something that composes inside a larger expression. Any expression can
// be backgrounded now — an *ast.ExternalCall becomes a subprocess job
// (evalBackground), anything else an in-process one (evalBackgroundInproc,
// local-only) — see ast.Background's own doc comment.
//
// '&' may be immediately followed by the plain identifier "pty" (not a
// keyword — LookupIdent has no "pty" entry, so this is a parser-level
// convention, the same way '%' before an IDENT is what makes something
// an external call rather than a new token kind) to request
// job/job.go's opt-in pty instead of plain pipes — see ast.Background's
// Pty field.
func (p *Parser) parseValueExpr() ast.Expr {
	expr := p.parseExpr(LOWEST)
	if expr == nil {
		return nil
	}
	if p.peek.Kind != token.AMP {
		return expr
	}
	p.next() // cur: expr's last token -> '&'
	bg := &ast.Background{Tok: p.cur, Expr: expr}
	if p.peek.Kind == token.IDENT && p.peek.Literal == "pty" {
		p.next() // cur: '&' -> 'pty'
		bg.Pty = true
	}
	return bg
}

// parseBindStmt parses `bind SRC, DST[, before|after|replace][, ro]`. SRC and
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
	// Trailing comma-separated words, in any order, each at most once: a
	// disposition and/or the `ro` flag.
	disp, sawDisp, ro := "replace", false, false
	for p.peek.Kind == token.COMMA {
		p.next() // cur: previous token -> ','
		p.next() // consume ',' -> the word
		switch {
		case p.cur.Kind == token.IDENT && isDispositionWord(p.cur.Literal):
			if sawDisp {
				p.errorf("bind: more than one disposition (%q after %q)", p.cur.Literal, disp)
				return nil
			}
			disp, sawDisp = p.cur.Literal, true
		case p.cur.Kind == token.IDENT && p.cur.Literal == "ro":
			if ro {
				p.errorf("bind: 'ro' given twice")
				return nil
			}
			ro = true
		default:
			p.errorf("expected a disposition (before/after/replace) or ro, got %s(%q)", p.cur.Kind, p.cur.Literal)
			return nil
		}
	}
	return &ast.BindStmt{Tok: tok, Src: src, Dst: dst, Disposition: disp, ReadOnly: ro}
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
		if p.isNativeProgram != nil && p.isNativeProgram(p.cur.Literal) {
			return p.parseNativeCall()
		}
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
	case token.IN_NS:
		return p.parseInNS()
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
	// Deliberately not expectPeekOrCur: its "cur already is k" fast path
	// is wrong here specifically, since RPAREN also closes a Call's own
	// argument list -- an inner expr ending in a Call (or another
	// grouped expr) already leaves cur sitting on *that* production's
	// closing ')', which is a different token from this group's own. A
	// grouped expression's content can never legitimately end with cur
	// already on this group's real closer (the lexer always emits two
	// separate RPARENs for "))"), so peek is required unconditionally.
	if p.peek.Kind != token.RPAREN {
		p.errorf("expected ')', got %s(%q)", p.peek.Kind, p.peek.Literal)
		return nil
	}
	p.next()
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
		token.RPAREN, token.RBRACE, token.RBRACKET, token.COMMA,
		token.AND, token.OR:
		return true
	}
	return false
}

func (p *Parser) parseExternalCall() ast.Expr {
	tok := p.cur
	p.next() // consume '%'
	if p.cur.Kind == token.LPAREN {
		nameExpr := p.parseGroupedExpr()
		if nameExpr == nil {
			return nil
		}
		return p.parseExternalCallArgsFor(&ast.ExternalCall{Tok: tok, NameExpr: nameExpr})
	}
	if p.cur.Kind != token.IDENT {
		p.errorf("expected command name after '%%', got %s", p.cur.Kind)
		return nil
	}
	return p.parseExternalCallArgs(tok, p.cur.Literal)
}

// parseNativeCall is parseExternalCall's prefix-free sibling: reached
// from parsePrefix's IDENT case (see isNativeProgram there) when p.cur
// is already the command name — a native program (see kyu/eval's
// isNativeProgram, e.g. 9ed) needs no '%'/'$' sigil to consume first.
// Always a literal name, never %(expr)'s computed form — see
// ast.ExternalCall's own doc comment for why that's not just an
// oversight. Deliberately builds the exact same *ast.ExternalCall node
// parseExternalCall does, not a new AST type: that's what makes
// backgrounding (evalBackground's own *ast.ExternalCall branch, taken
// over evalBackgroundInproc's for any Background whose Expr is one), the
// fullscreen guard (isFullscreenProgram in evalBackground/runExternal),
// and ordinary job execution (runExternal dispatches on the resolved
// name) all keep working with zero duplicated logic on the eval side.
func (p *Parser) parseNativeCall() ast.Expr {
	return p.parseExternalCallArgs(p.cur, p.cur.Literal)
}

// parseExternalCallArgs builds an ExternalCall named name (tok is
// whichever token the call started at — the '%'/'$' sigil for
// parseExternalCall, the bare command-name IDENT itself for
// parseNativeCall) and consumes its space-separated argument list via
// parseExternalCallArgsFor.
func (p *Parser) parseExternalCallArgs(tok token.Token, name string) ast.Expr {
	return p.parseExternalCallArgsFor(&ast.ExternalCall{Tok: tok, Name: name})
}

// parseExternalCallArgsFor consumes call's space-separated argument
// list — the shared tail parseExternalCallArgs (a literal name) and
// parseExternalCall's %(expr) branch (a computed one) both need
// identically, once each has built call with whichever of Name/NameExpr
// applies.
func (p *Parser) parseExternalCallArgsFor(call *ast.ExternalCall) ast.Expr {
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

// parseAtHost parses `@host { ... }`, `@/path { ... }` and
// `@(expr) { ... }`. All three name the mount point whose /jobs the block
// runs against; the operand is a Path-typed expression, the same as bind's
// destination, so it can be computed:
//
//   - a bare identifier directly after '@' is shorthand for the mount
//     /n/<ident> (the same treatment %cmd's command name gets after '%' —
//     exempt from the usual bareword-ambiguity concerns since nothing
//     infix-operator-shaped can follow '@' at this position). It is
//     always a literal name, never a variable lookup: use @(expr) for that.
//   - a Path literal names any mount point.
//   - a parenthesized expression is evaluated to a Path at run time.
func (p *Parser) parseAtHost() ast.Expr {
	tok := p.cur // '@'
	p.next()     // consume '@' -> the operand's first token
	at := &ast.AtHost{Tok: tok}
	switch p.cur.Kind {
	case token.IDENT:
		at.Host = p.cur.Literal
	case token.PATH:
		if at.Target = p.parsePrefix(); at.Target == nil {
			return nil
		}
	case token.LPAREN:
		if at.Target = p.parseGroupedExpr(); at.Target == nil {
			return nil
		}
	default:
		p.errorf("expected a host name, a path or '(' after '@', got %s(%q)", p.cur.Kind, p.cur.Literal)
		return nil
	}
	if !p.expectPeekOrCur(token.LBRACE) {
		return nil
	}
	p.next() // consume '{'
	at.Body = p.parseBlock()
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	return at
}

// parseInNS parses `in_ns { ... }`.
func (p *Parser) parseInNS() ast.Expr {
	tok := p.cur // 'in_ns'
	if !p.expectPeekOrCur(token.LBRACE) {
		return nil
	}
	p.next() // consume '{'
	body := p.parseBlock()
	if !p.expectPeekOrCur(token.RBRACE) {
		return nil
	}
	return &ast.InNS{Tok: tok, Body: body}
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
