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

// TestPathAsExternalCallFirstArg locks in lastWasExternalName's exemption:
// a bare Path right after a %cmd command name must not re-lex as
// "name / path...", dividing the command name by the path -- the same
// class of ambiguity TestPathVsDivision's `bind` case already covers, just
// for the command-name-to-first-argument transition instead of comma-vs-
// space. Also covers a second bareword Path argument (already-working via
// the pre-existing "a prior PATH is exempted" rule) so a regression in
// either exemption shows up here.
func TestPathAsExternalCallFirstArg(t *testing.T) {
	assertKinds(t, `%cat /src/greeting.txt`, []token.Kind{
		token.PERCENT, token.IDENT, token.PATH, token.EOF,
	})
	assertKinds(t, `%cat /a/b /c/d`, []token.Kind{
		token.PERCENT, token.IDENT, token.PATH, token.PATH, token.EOF,
	})
	// The known, narrower remaining gap (a bareword Path after a non-Path
	// argument still divides) -- documented here so a future fix that
	// closes it updates this test rather than silently leaving it stale.
	assertKinds(t, `%grep "foo" /path`, []token.Kind{
		token.PERCENT, token.IDENT, token.STRING, token.SLASH, token.IDENT, token.EOF,
	})
}

func TestPercentSigilVsModulo(t *testing.T) {
	// expression-start '%' immediately before a letter is the external-call sigil
	assertKinds(t, `%grep foo`, []token.Kind{token.PERCENT, token.IDENT, token.IDENT, token.EOF})
	// infix '%' after a value is modulo
	assertKinds(t, `10 % 3`, []token.Kind{token.INT, token.MOD, token.INT, token.EOF})
}

// lexAllNative is lexAll's sibling for tests exercising
// NativeProgramLookup: same loop, but with that field set before the
// first Next() call (required -- see NativeProgramLookup's own doc
// comment on why a caller must set it before scanning starts, unlike a
// Parser's WithNativeProgramLookup which handles that ordering itself).
func lexAllNative(t *testing.T, src string, isNative func(string) bool) []token.Token {
	t.Helper()
	l := New(src)
	l.NativeProgramLookup = isNative
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

// TestNativeProgramNameLexesAsExternalName is the lexer half of the
// prefix-free "native program" call form (see kyu/eval's
// isNativeProgram, kyu/parser's WithNativeProgramLookup/parseNativeCall):
// a bareword IDENT matching NativeProgramLookup must lex exactly the way
// a %cmd command name does (lexExternalName's IDENT, lastWasExternalName
// set so a following bare Path doesn't re-lex as division) -- including
// the digit-leading case (9ed) that's the entire reason this needs
// lexer-level cooperation at all: outside '%', a leading digit would
// otherwise commit to number-lexing and choke on the trailing letters
// (see TestExternalCommandNameMayStartWithDigit's non-native version of
// the same hazard).
func TestNativeProgramNameLexesAsExternalName(t *testing.T) {
	isNative := func(name string) bool { return name == "9ed" }

	toks := lexAllNative(t, `9ed /some/ns/path`, isNative)
	want := []token.Kind{token.IDENT, token.PATH, token.EOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), toks)
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("token %d = %s(%q), want kind %s", i, toks[i].Kind, toks[i].Literal, k)
		}
	}
	if toks[0].Literal != "9ed" {
		t.Errorf("token 0 literal = %q, want %q", toks[0].Literal, "9ed")
	}
}

// TestNonNativeNameUnaffectedByLookup confirms a name the live lookup
// doesn't match lexes exactly as it always has (an ordinary IDENT, a
// following bare Path re-lexing as division) -- NativeProgramLookup
// being set at all must not change behavior for anything outside it.
func TestNonNativeNameUnaffectedByLookup(t *testing.T) {
	isNative := func(name string) bool { return name == "9ed" }
	toks := lexAllNative(t, `notnative /a`, isNative)
	want := []token.Kind{token.IDENT, token.SLASH, token.IDENT, token.EOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), toks)
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("token %d = %s(%q), want kind %s", i, toks[i].Kind, toks[i].Literal, k)
		}
	}
}

// TestHyphenatedNativeNameNeverCollapsesSubtraction is the regression
// test for a real bug caught in code review: an earlier version of
// NativeProgramLookup's dispatch reused lexExternalName's own hyphen-
// inclusive scan for its pre-lex lookahead too, so a no-space
// subtraction like `docker-compose` (kyu's '-' needs no surrounding
// whitespace) would silently collapse into one native-call token
// instead of IDENT MINUS IDENT whenever "docker-compose" happened to be
// a configured native-program name. A hyphenated bareword name is now
// deliberately never recognized (lexIdent's own scan, and
// matchesDigitLeadingNativeName's lookahead, both exclude '-') --
// reachable only via the unambiguous %name form instead, which has no
// such conflict since nothing but the sigil can precede it.
func TestHyphenatedNativeNameNeverCollapsesSubtraction(t *testing.T) {
	isNative := func(name string) bool { return name == "docker-compose" }
	// Ordinary subtraction, unaffected even though the concatenated
	// span would match if this were ever treated as one candidate.
	toks := lexAllNative(t, `docker-compose`, isNative)
	want := []token.Kind{token.IDENT, token.MINUS, token.IDENT, token.EOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), toks)
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("token %d = %s(%q), want kind %s", i, toks[i].Kind, toks[i].Literal, k)
		}
	}
	// The %-sigil form is unaffected by this restriction -- it was never
	// ambiguous in the first place (nothing but '%' can precede it).
	assertKinds(t, `%docker-compose foo`, []token.Kind{
		token.PERCENT, token.IDENT, token.IDENT, token.EOF,
	})
}

// TestKeywordNeverTriggersNativeProgramLookup confirms a keyword (which
// can never legitimately be a native-program name) is never even handed
// to NativeProgramLookup -- the lookup panics if called at all, so this
// fails loudly if a regression reintroduces a pre-lex check that can't
// yet tell a keyword from a plain identifier.
func TestKeywordNeverTriggersNativeProgramLookup(t *testing.T) {
	panicking := func(name string) bool { panic("NativeProgramLookup called for " + name) }
	toks := lexAllNative(t, `if true { 1 } else { 2 }`, panicking)
	if len(toks) == 0 || toks[len(toks)-1].Kind != token.EOF {
		t.Fatalf("lexing failed unexpectedly: %v", toks)
	}
}

// TestPlainIntegerNeverTriggersNativeProgramLookup is
// matchesDigitLeadingNativeName's own regression test: a pure-digit span
// (an ordinary integer literal, the overwhelmingly common case at a
// digit-leading position) must never reach NativeProgramLookup at all --
// a native program name always mixes in a letter, so this is pure
// wasted work otherwise (flagged in code review as measurable lexing
// overhead on every number literal once any native program is
// configured, e.g. the "9ed" default).
func TestPlainIntegerNeverTriggersNativeProgramLookup(t *testing.T) {
	panicking := func(name string) bool { panic("NativeProgramLookup called for " + name) }
	toks := lexAllNative(t, `42 + 100`, panicking)
	want := []token.Kind{token.INT, token.PLUS, token.INT, token.EOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(want), toks)
	}
}

func TestExternalCommandNameMayStartWithDigit(t *testing.T) {
	// Plan-9-style tool names (9ed, 9term, ...) start with a digit; the '%'
	// sigil must still disambiguate as the external-call form rather than
	// falling back to modulo, and the digit-led name must lex as one IDENT
	// rather than splitting into an INT and a trailing identifier.
	assertKinds(t, `%9ed foo`, []token.Kind{token.PERCENT, token.IDENT, token.IDENT, token.EOF})
}

func TestUnbindKeyword(t *testing.T) {
	assertKinds(t, `unbind /local`, []token.Kind{token.UNBIND, token.PATH, token.EOF})
}

func TestWhileBreakContinueKeywords(t *testing.T) {
	assertKinds(t, `while true { break }`,
		[]token.Kind{token.WHILE, token.TRUE, token.LBRACE, token.BREAK, token.RBRACE, token.EOF})
	assertKinds(t, `continue`, []token.Kind{token.CONTINUE, token.EOF})
}

// TestDollarIsIllegal locks in that '$' is no longer a valid sigil --
// removed along with ast.PassthroughStmt/$cmd (superseded by %cmd's own
// fullscreen-program detection, which works everywhere $cmd's direct-
// terminal path didn't, e.g. inside the TUI pane multiplexer).
func TestDollarIsIllegal(t *testing.T) {
	assertKinds(t, `$vim foo`, []token.Kind{token.ILLEGAL, token.IDENT, token.IDENT, token.EOF})
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
