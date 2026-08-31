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
	case isDigit(r):
		return l.lexNumber(line, col)
	case isLetter(r) && (l.lastKind == token.PERCENT || l.lastKind == token.DOLLAR):
		return l.lexExternalName(line, col)
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
		if !endsValue(l.lastKind) && isLetter(l.peek()) {
			return l.emitAt(token.PERCENT, "%", line, col)
		}
		return l.emitAt(token.MOD, "%", line, col)
	case '$':
		// unlike '%', '$' has no competing infix meaning to disambiguate
		// from — it's always the passthrough-command sigil.
		return l.emitAt(token.DOLLAR, "$", line, col)
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
	return l.emitAt(token.IDENT, string(l.src[start:l.pos]), line, col)
}

func (l *Lexer) lexIdent(line, col int) token.Token {
	start := l.pos
	for isLetter(l.peek()) || isDigit(l.peek()) {
		l.advance()
	}
	lit := string(l.src[start:l.pos])
	return l.emitAt(token.LookupIdent(lit), lit, line, col)
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
// never a division expression.
func (l *Lexer) lexSlashOrPath(line, col int) token.Token {
	if endsValue(l.lastKind) && l.lastKind != token.PATH {
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
	return token.Token{Kind: k, Literal: lit, Line: line, Col: col}
}
