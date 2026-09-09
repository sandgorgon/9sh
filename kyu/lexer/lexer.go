// Package lexer turns kyu source text into a token stream.
package lexer

import (
	"strings"

	"github.com/sandgorgon/9sh/kyu/token"
)

type Lexer struct {
	src  []rune
	pos  int
	line int
	col  int

	// lastKind drives two disambiguations that depend on lexer state alone,
	// not the parser's grammar: '/' as division vs. a bare Path literal, and
	// '%' as the modulo operator vs. the %cmd external-call sigil. It also
	// drives Go-style automatic newline insertion.
	lastKind token.Kind

	// lastWasExternalName is a third, narrower disambiguation in the same
	// family as lastKind: true for exactly one token immediately after
	// lexExternalName produces the %cmd command-name IDENT, false
	// otherwise (including for every other IDENT). Kept separate from
	// lastKind rather than folded into it because the command name must
	// still emit as an ordinary token.IDENT (parseExternalCall depends on
	// that) — this only affects lexSlashOrPath's exemption, not the token's
	// own Kind. See lexSlashOrPath's doc comment for why it's needed.
	lastWasExternalName bool

	// NativeProgramLookup, when set, reports whether a just-lexed IDENT
	// names a live "native program" (see kyu/eval's isNativeProgram —
	// an external program that's namespace-aware and callable bareword,
	// no % sigil, e.g. 9ed). nil (the default, and every existing
	// lexer.New caller) means no native programs are recognized — zero
	// behavior change for any caller that doesn't opt in. When it does
	// match, lexIdent sets lastWasExternalName exactly the way
	// lexExternalName already does for a %cmd's own name, so a following
	// bare '/path' argument lexes as PATH, not division — the identical
	// hazard %/$ external names already had to solve, reused here rather
	// than duplicated. Checked unconditionally, not gated to a
	// statement-start position the way '%' itself is: unlike '%', which
	// is genuinely ambiguous with the modulo operator, a name in this
	// list can only ever mean the native program (kyu/eval's
	// isNativeProgram already refuses to match anything that's also a
	// real identifier binding), so there's no comparable ambiguity to
	// gate against.
	NativeProgramLookup func(name string) bool
}

// valuesCanEndStatement/valuesCanPrecedeSlash are the token kinds after which
// a '/' means division and a physical newline ends a statement (ASI).
func endsValue(k token.Kind) bool {
	switch k {
	case token.IDENT, token.INT, token.FLOAT, token.STRING, token.DURATION,
		token.PATH, token.TRUE, token.FALSE, token.NULL,
		token.RPAREN, token.RBRACE, token.RBRACKET:
		return true
	}
	return false
}

func New(src string) *Lexer {
	return &Lexer{src: []rune(src), line: 1, col: 1, lastKind: token.ILLEGAL}
}

func (l *Lexer) peek() rune {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *Lexer) peekAt(off int) rune {
	if l.pos+off >= len(l.src) {
		return 0
	}
	return l.src[l.pos+off]
}

func (l *Lexer) advance() rune {
	r := l.src[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }
func isLetter(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
func isPathChar(r rune) bool {
	return isLetter(r) || isDigit(r) || r == '/' || r == '.' || r == '-' || r == '_'
}

// Next returns the next token in the stream. Callers should stop on an EOF token.
func (l *Lexer) Next() token.Token {
	newlineSeen := l.skipSpaceAndComments()
	if newlineSeen && endsValue(l.lastKind) {
		return l.emit(token.NEWLINE, "\n")
	}

	if l.pos >= len(l.src) {
		return l.emit(token.EOF, "")
	}

	line, col := l.line, l.col
	r := l.peek()

	switch {
	case (isLetter(r) || isDigit(r)) && l.lastKind == token.PERCENT:
		return l.lexExternalName(line, col)
	// A digit-leading native-program name (9ed, 9term, ... -- Plan-9-
	// style tool names, the exact reason lexExternalName exists at all)
	// would otherwise fall straight into lexNumber below and choke on
	// the trailing letters, since number-vs-identifier can't be told
	// apart from the first rune alone once digit-leading identifiers are
	// allowed outside '%'. Only digit-leading needs this pre-lex
	// lookahead -- a letter-leading name is handled after the fact, in
	// lexIdent, since lexIdent's own ordinary scan already produces the
	// right token either way and checking post-lex avoids ever calling
	// NativeProgramLookup for a keyword (if/while/true/...). See
	// matchesDigitLeadingNativeName's own doc comment for why this is
	// scoped to isDigit(r) only, not isLetter(r) too.
	case isDigit(r) && l.NativeProgramLookup != nil && l.matchesDigitLeadingNativeName():
		return l.lexExternalName(line, col)
	case isDigit(r):
		return l.lexNumber(line, col)
	case isLetter(r):
		return l.lexIdent(line, col)
	case r == '"':
		return l.lexString(line, col)
	case r == '/':
		return l.lexSlashOrPath(line, col)
	}

	l.advance()
	switch r {
	case '=':
		if l.peek() == '=' {
			l.advance()
			return l.emitAt(token.EQ, "==", line, col)
		}
		return l.emitAt(token.ASSIGN, "=", line, col)
	case ':':
		if l.peek() == '=' {
			l.advance()
			return l.emitAt(token.DEFINE, ":=", line, col)
		}
		return l.emitAt(token.COLON, ":", line, col)
	case '|':
		if l.peek() == '|' {
			l.advance()
			return l.emitAt(token.OR, "||", line, col)
		}
		return l.emitAt(token.PIPE, "|", line, col)
	case '%':
		if !endsValue(l.lastKind) && (isLetter(l.peek()) || isDigit(l.peek())) {
			return l.emitAt(token.PERCENT, "%", line, col)
		}
		return l.emitAt(token.MOD, "%", line, col)
	case '+':
		return l.emitAt(token.PLUS, "+", line, col)
	case '-':
		return l.emitAt(token.MINUS, "-", line, col)
	case '*':
		return l.emitAt(token.STAR, "*", line, col)
	case '!':
		if l.peek() == '=' {
			l.advance()
			return l.emitAt(token.NEQ, "!=", line, col)
		}
		return l.emitAt(token.NOT, "!", line, col)
	case '<':
		if l.peek() == '=' {
			l.advance()
			return l.emitAt(token.LE, "<=", line, col)
		}
		return l.emitAt(token.LT, "<", line, col)
	case '>':
		if l.peek() == '=' {
			l.advance()
			return l.emitAt(token.GE, ">=", line, col)
		}
		return l.emitAt(token.GT, ">", line, col)
	case '&':
		if l.peek() == '&' {
			l.advance()
			return l.emitAt(token.AND, "&&", line, col)
		}
		return l.emitAt(token.AMP, "&", line, col)
	case '@':
		return l.emitAt(token.AT, "@", line, col)
	case '?':
		return l.emitAt(token.QUESTION, "?", line, col)
	case '.':
		if !endsValue(l.lastKind) && isDigit(l.peek()) {
			l.pos--
			l.col--
			return l.lexNumber(line, col)
		}
		return l.emitAt(token.DOT, ".", line, col)
	case ',':
		return l.emitAt(token.COMMA, ",", line, col)
	case ';':
		return l.emitAt(token.SEMI, ";", line, col)
	case '(':
		return l.emitAt(token.LPAREN, "(", line, col)
	case ')':
		return l.emitAt(token.RPAREN, ")", line, col)
	case '{':
		return l.emitAt(token.LBRACE, "{", line, col)
	case '}':
		return l.emitAt(token.RBRACE, "}", line, col)
	case '[':
		return l.emitAt(token.LBRACKET, "[", line, col)
	case ']':
		return l.emitAt(token.RBRACKET, "]", line, col)
	}

	return l.emitAt(token.ILLEGAL, string(r), line, col)
}

// skipSpaceAndComments consumes spaces, tabs, comments (# ...) and newlines,
// reporting whether at least one newline was crossed.
func (l *Lexer) skipSpaceAndComments() bool {
	sawNewline := false
	for l.pos < len(l.src) {
		r := l.peek()
		switch r {
		case '\n':
			sawNewline = true
			l.advance()
		case ' ', '\t', '\r':
			l.advance()
		case '#':
			for l.pos < len(l.src) && l.peek() != '\n' {
				l.advance()
			}
		default:
			return sawNewline
		}
	}
	return sawNewline
}

func (l *Lexer) lexNumber(line, col int) token.Token {
	start := l.pos
	for isDigit(l.peek()) {
		l.advance()
	}
	isFloat := false
	if l.peek() == '.' && isDigit(l.peekAt(1)) {
		isFloat = true
		l.advance()
		for isDigit(l.peek()) {
			l.advance()
		}
	}

	// duration suffix: ns, us, µs, ms, s, m, h
	if _, ok := l.matchDurationUnit(); ok {
		return l.emitAt(token.DURATION, string(l.src[start:l.pos]), line, col)
	}

	lit := string(l.src[start:l.pos])
	if isFloat {
		return l.emitAt(token.FLOAT, lit, line, col)
	}
	return l.emitAt(token.INT, lit, line, col)
}

func (l *Lexer) matchDurationUnit() (string, bool) {
	for _, unit := range []string{"ns", "us", "µs", "ms", "s", "m", "h"} {
		ur := []rune(unit)
		if l.pos+len(ur) > len(l.src) {
			continue
		}
		if string(l.src[l.pos:l.pos+len(ur)]) != unit {
			continue
		}
		// don't swallow into a longer identifier, e.g. "5starts"
		if l.pos+len(ur) < len(l.src) && isLetter(l.src[l.pos+len(ur)]) {
			continue
		}
		for range ur {
			l.advance()
		}
		return unit, true
	}
	return "", false
}

// lexExternalName scans the command-name token immediately after a '%'
// sigil. Unlike an ordinary kyu identifier, it allows internal hyphens
// (docker-compose, apt-get, ...) since there's no infix-subtraction
// ambiguity in this position — nothing legal can precede an external
// command name but the sigil itself. It's never a keyword.
func (l *Lexer) lexExternalName(line, col int) token.Token {
	start := l.pos
	for isLetter(l.peek()) || isDigit(l.peek()) || l.peek() == '-' {
		l.advance()
	}
	tok := l.emitAt(token.IDENT, string(l.src[start:l.pos]), line, col)
	l.lastWasExternalName = true
	return tok
}

// matchesDigitLeadingNativeName reports whether the current position
// starts a digit-leading native-program name (9ed, 9term, ...) —
// Next()'s pre-lex lookahead, needed only for the digit-leading case
// (see its own doc comment for why letter-leading doesn't need this).
//
// Deliberately letters+digits only, no '-', unlike lexExternalName's own
// scan: a hyphen here would let a no-space subtraction like `docker-
// compose` (kyu's '-' needs no surrounding whitespace) silently lex as
// one native-call token instead of two identifiers and a MINUS, if
// "docker-compose" ever were itself a configured native-program name.
// Scoping prefix-free recognition to un-hyphenated names avoids that
// collision entirely — a hyphenated program name is still reachable via
// the unambiguous %name form, which has no such conflict since nothing
// but the sigil can precede it.
//
// Also skips the lookup entirely for a plain integer literal (a
// non-letter digit run, the overwhelmingly common case at a digit-
// leading position): a native program name always mixes in at least one
// letter, so a lone number can never match and isn't worth the
// NativeProgramLookup call.
func (l *Lexer) matchesDigitLeadingNativeName() bool {
	i := l.pos
	sawLetter := false
	for i < len(l.src) && (isLetter(l.src[i]) || isDigit(l.src[i])) {
		if isLetter(l.src[i]) {
			sawLetter = true
		}
		i++
	}
	if !sawLetter {
		return false
	}
	return l.NativeProgramLookup(string(l.src[l.pos:i]))
}

func (l *Lexer) lexIdent(line, col int) token.Token {
	start := l.pos
	for isLetter(l.peek()) || isDigit(l.peek()) {
		l.advance()
	}
	lit := string(l.src[start:l.pos])
	kind := token.LookupIdent(lit)
	tok := l.emitAt(kind, lit, line, col)
	// Checked after lexing, not before: only a plain identifier (kind ==
	// IDENT, not a keyword LookupIdent matched, e.g. "if"/"while"/"true")
	// can ever be a native program name, so this never spends a lookup on
	// a keyword. lexIdent's own scan already excludes '-', so there's no
	// hyphen-vs-subtraction ambiguity to worry about here the way
	// matchesDigitLeadingNativeName's own doc comment describes for the
	// digit-leading case — lit is always exactly what a plain kyu
	// identifier could be.
	if kind == token.IDENT && l.NativeProgramLookup != nil && l.NativeProgramLookup(lit) {
		l.lastWasExternalName = true
	}
	return tok
}

func (l *Lexer) lexString(line, col int) token.Token {
	l.advance() // opening quote
	var b strings.Builder
	for l.pos < len(l.src) && l.peek() != '"' {
		r := l.advance()
		if r == '\\' && l.pos < len(l.src) {
			esc := l.advance()
			switch esc {
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			case 'r':
				b.WriteRune('\r')
			case '"':
				b.WriteRune('"')
			case '\\':
				b.WriteRune('\\')
			default:
				b.WriteRune(esc)
			}
			continue
		}
		b.WriteRune(r)
	}
	if l.pos < len(l.src) {
		l.advance() // closing quote
	}
	return l.emitAt(token.STRING, b.String(), line, col)
}

// lexSlashOrPath disambiguates the division operator from a bare Path
// literal using only lexer state: a '/' can start a Path only where a value
// could not have just ended (expression-start position). A prior PATH is
// exempted: paths aren't divisible, so "bind /a /b" is two path arguments,
// never a division expression. The command-name IDENT right after a '%'/'$'
// sigil is exempted the same way (lastWasExternalName): "%cat /path" must
// not re-lex "/path" as "cat / path" dividing a bareword by a path, since
// external-call arguments are a juxtaposed positional list, never operands
// of an infix operator against the command name. This only covers the
// first argument — a later bareword Path after a non-Path argument (e.g.
// `%grep "foo" /path`) still hits the general rule, a known, narrower gap.
func (l *Lexer) lexSlashOrPath(line, col int) token.Token {
	if endsValue(l.lastKind) && l.lastKind != token.PATH && !l.lastWasExternalName {
		l.advance()
		return l.emitAt(token.SLASH, "/", line, col)
	}
	start := l.pos
	for isPathChar(l.peek()) {
		l.advance()
	}
	return l.emitAt(token.PATH, string(l.src[start:l.pos]), line, col)
}

func (l *Lexer) emit(k token.Kind, lit string) token.Token {
	return l.emitAt(k, lit, l.line, l.col)
}

func (l *Lexer) emitAt(k token.Kind, lit string, line, col int) token.Token {
	l.lastKind = k
	l.lastWasExternalName = false // see lexExternalName, the one place that overrides this right after
	return token.Token{Kind: k, Literal: lit, Line: line, Col: col}
}
