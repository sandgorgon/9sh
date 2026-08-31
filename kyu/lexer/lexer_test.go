package lexer

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/token"
)

func lexAll(t *testing.T, src string) []token.Token {
	t.Helper()
	l := New(src)
	var toks []token.Token
	for {
		tok := l.Next()
		toks = append(toks, tok)
		if tok.Kind == token.EOF {
			break
		}
	}
	return toks
}

func assertKinds(t *testing.T, src string, want []token.Kind) {
	t.Helper()
	toks := lexAll(t, src)
	if len(toks) != len(want) {
		var got []string
		for _, tk := range toks {
			got = append(got, tk.Kind.String()+"("+tk.Literal+")")
		}
		t.Fatalf("src %q: got %d tokens %v, want %d kinds %v", src, len(toks), got, len(want), want)
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("src %q: token %d = %s(%q), want kind %s", src, i, toks[i].Kind, toks[i].Literal, k)
		}
	}
}

func TestBasicTokens(t *testing.T) {
	assertKinds(t, `x := 5`, []token.Kind{token.IDENT, token.DEFINE, token.INT, token.EOF})
	assertKinds(t, `a == b`, []token.Kind{token.IDENT, token.EQ, token.IDENT, token.EOF})
	assertKinds(t, `a != b`, []token.Kind{token.IDENT, token.NEQ, token.IDENT, token.EOF})
	assertKinds(t, `a && b || !c`, []token.Kind{
		token.IDENT, token.AND, token.IDENT, token.OR, token.NOT, token.IDENT, token.EOF,
	})
}

func TestStringLiteral(t *testing.T) {
	toks := lexAll(t, `"hello\nworld"`)
	if toks[0].Kind != token.STRING || toks[0].Literal != "hello\nworld" {
		t.Fatalf("got %v", toks[0])
	}
}

func TestNumbers(t *testing.T) {
	toks := lexAll(t, `42 3.14`)
	if toks[0].Kind != token.INT || toks[0].Literal != "42" {
		t.Fatalf("got %v", toks[0])
	}
	if toks[1].Kind != token.FLOAT || toks[1].Literal != "3.14" {
		t.Fatalf("got %v", toks[1])
	}
}

func TestDurations(t *testing.T) {
	toks := lexAll(t, `500ms 2s 1h`)
	want := []struct {
		kind token.Kind
		lit  string
	}{
		{token.DURATION, "500ms"},
		{token.DURATION, "2s"},
		{token.DURATION, "1h"},
	}
	for i, w := range want {
		if toks[i].Kind != w.kind || toks[i].Literal != w.lit {
			t.Errorf("token %d: got %s(%q), want %s(%q)", i, toks[i].Kind, toks[i].Literal, w.kind, w.lit)
		}
	}
}

func TestPathVsDivision(t *testing.T) {
	// expression-start '/' is a Path literal
	assertKinds(t, `/local/bin`, []token.Kind{token.PATH, token.EOF})
	assertKinds(t, `bind /local/bin /bin`, []token.Kind{
		token.BIND, token.PATH, token.PATH, token.EOF,
	})
	// infix '/' after a value is division
	assertKinds(t, `10 / 2`, []token.Kind{token.INT, token.SLASH, token.INT, token.EOF})
	assertKinds(t, `x / y`, []token.Kind{token.IDENT, token.SLASH, token.IDENT, token.EOF})
}

func TestPercentSigilVsModulo(t *testing.T) {
	// expression-start '%' immediately before a letter is the external-call sigil
	assertKinds(t, `%grep foo`, []token.Kind{token.PERCENT, token.IDENT, token.IDENT, token.EOF})
	// infix '%' after a value is modulo
	assertKinds(t, `10 % 3`, []token.Kind{token.INT, token.MOD, token.INT, token.EOF})
}

func TestDollarSigil(t *testing.T) {
	// unlike '%', '$' has no infix meaning to disambiguate from -- it's
	// always the passthrough-command sigil, and the command name after it
	// lexes the same way (lexExternalName) as after '%'.
	assertKinds(t, `$vim foo`, []token.Kind{token.DOLLAR, token.IDENT, token.IDENT, token.EOF})
	assertKinds(t, `$docker-compose up`, []token.Kind{token.DOLLAR, token.IDENT, token.IDENT, token.EOF})
}

func TestExternalCommandNameAllowsHyphens(t *testing.T) {
	// unlike a kyu identifier, a command name right after '%' may contain
	// hyphens (docker-compose, apt-get, ...) with no subtraction ambiguity.
	toks := lexAll(t, `%apt-get update`)
	want := []struct {
		kind token.Kind
		lit  string
	}{
		{token.PERCENT, "%"},
		{token.IDENT, "apt-get"},
		{token.IDENT, "update"},
		{token.EOF, ""},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), toks)
	}
	for i, w := range want {
		if toks[i].Kind != w.kind || toks[i].Literal != w.lit {
			t.Errorf("token %d: got %s(%q), want %s(%q)", i, toks[i].Kind, toks[i].Literal, w.kind, w.lit)
		}
	}
	// a bare kyu identifier still splits on '-': subtraction, not one ident
	assertKinds(t, `sort-by`, []token.Kind{token.IDENT, token.MINUS, token.IDENT, token.EOF})
}

func TestPipeAndFieldAccess(t *testing.T) {
	assertKinds(t, `jobs | where { |j| j.status == "running" }`, []token.Kind{
		token.IDENT, token.PIPE, token.IDENT, token.LBRACE, token.PIPE, token.IDENT, token.PIPE,
		token.IDENT, token.DOT, token.IDENT, token.EQ, token.STRING, token.RBRACE, token.EOF,
	})
}

func TestNewlineInsertion(t *testing.T) {
	// a newline after a value-ending token becomes a statement separator
	assertKinds(t, "x := 1\ny := 2", []token.Kind{
		token.IDENT, token.DEFINE, token.INT, token.NEWLINE,
		token.IDENT, token.DEFINE, token.INT, token.EOF,
	})
	// a newline after an operator (mid-expression) is not inserted
	assertKinds(t, "x :=\n  1 +\n  2", []token.Kind{
		token.IDENT, token.DEFINE, token.INT, token.PLUS, token.INT, token.EOF,
	})
}

func TestComment(t *testing.T) {
	assertKinds(t, "x := 1 # comment\ny", []token.Kind{
		token.IDENT, token.DEFINE, token.INT, token.NEWLINE, token.IDENT, token.EOF,
	})
}
