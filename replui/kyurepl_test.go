package replui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sandgorgon/9p/examples/dirfs"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func newTestReplWidget(t *testing.T) *kyuReplWidget {
	t.Helper()
	return &kyuReplWidget{env: eval.NewGlobalEnv(nil)}
}

// newNamespaceTestEnv binds a real dirfs-backed temp directory (with a
// file and a subdirectory, to exercise both entry kinds) at /x, and
// returns a shared *eval.Env over that namespace — used by the
// namespace-path-completion tests below, which need something real to
// complete against beyond the local real filesystem alone.
func newNamespaceTestEnv(t *testing.T) *eval.Env {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("A"), 0644); err != nil {
		t.Fatalf("seed a.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("B"), 0644); err != nil {
		t.Fatalf("seed sub/b.txt: %v", err)
	}

	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	namespace := ns.New()
	if err := namespace.BindFS(fs, "", "/x", ns.Replace); err != nil {
		t.Fatalf("bind /x: %v", err)
	}
	return eval.NewGlobalEnv(namespace)
}

func sendRunes(w *kyuReplWidget, s string) {
	for _, r := range s {
		w.HandleEvent(input.KeyEvent{Rune: r})
	}
}

// sendEnter drives Enter and, if that went busy (submit() spawning a
// background evaluate() call for a non-blank source -- see its own doc
// comment), waits for and applies that result immediately via
// waitForEvalDone -- these tests talk to a bare *kyuReplWidget with no
// Model/App in play to drive TakePendingMsg for them the way production
// does (via the redraw tick -- see redrawTickCmd's doc comment), so
// every existing test that checks w.lines right after sendEnter keeps
// seeing the result show up synchronously, from this test's own point
// of view, even though it's genuinely computed on another goroutine.
func sendEnter(w *kyuReplWidget) {
	w.HandleEvent(input.KeyEvent{Key: input.KeyEnter})
	if w.busy {
		waitForEvalDone(w)
	}
}

// waitForEvalDone polls w.TakePendingMsg (see its own doc comment) for
// submit()'s background evaluate() call to finish, and applies the
// result via applyEvalResult the moment it does -- a short poll instead
// of a real synchronization primitive is enough here since every
// pending evaluate() call is either pure in-memory kyu code or, in the
// handful of tests that actually run a real subprocess, one that's
// expected to finish quickly (see e.g. skipUnlessOnPath-gated tests).
func waitForEvalDone(w *kyuReplWidget) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if msg := w.TakePendingMsg(); msg != nil {
			if done, ok := msg.(evalDoneMsg); ok {
				w.applyEvalResult(done.result)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func sendKey(w *kyuReplWidget, k input.Key) {
	w.HandleEvent(input.KeyEvent{Key: k})
}

func sendCtrl(w *kyuReplWidget, r rune) {
	w.HandleEvent(input.KeyEvent{Rune: r, Mod: input.ModCtrl})
}

func lastLine(t *testing.T, w *kyuReplWidget) replLine {
	t.Helper()
	if len(w.lines) == 0 {
		t.Fatal("no transcript lines")
	}
	return w.lines[len(w.lines)-1]
}

func TestKyuReplEvaluatesExpression(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 2")
	sendEnter(w)
	if got := lastLine(t, w); got.text != "3" {
		t.Fatalf("last line = %q, want 3", got.text)
	}
}

func TestKyuReplStringResultWithEmbeddedNewlinesSplitsAcrossLines(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, `["a", "b", "c"] | join("\n")`)
	sendEnter(w)
	n := len(w.lines)
	if n < 3 {
		t.Fatalf("got %d transcript lines, want at least 3, got %v", n, w.lines)
	}
	got := []string{w.lines[n-3].text, w.lines[n-2].text, w.lines[n-1].text}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q (full: %v)", i, got[i], want[i], w.lines)
		}
	}
}

func TestKyuReplMultiLineInput(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "if true {")
	sendEnter(w)
	if len(w.lines) != 0 {
		t.Fatalf("should not evaluate mid-input, got lines: %v", w.lines)
	}
	sendRunes(w, `"done"`)
	sendEnter(w)
	if len(w.lines) != 0 {
		t.Fatalf("should still not evaluate, got lines: %v", w.lines)
	}
	sendRunes(w, "}")
	sendEnter(w)
	if got := lastLine(t, w); got.text != "done" {
		t.Fatalf("last line = %q, want done", got.text)
	}
}

func TestKyuReplBackspaceEditsInput(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "12")
	w.HandleEvent(input.KeyEvent{Key: input.KeyBackspace})
	sendEnter(w)
	if got := lastLine(t, w); got.text != "1" {
		t.Fatalf("last line = %q, want 1", got.text)
	}
}

func TestKyuReplParseErrorShowsErrorStyle(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, ")(") // net bracket depth 0 (submits), but not valid syntax
	sendEnter(w)
	if got := lastLine(t, w); got.style != errorStyle {
		t.Fatalf("style = %v, want errorStyle (text: %q)", got.style, got.text)
	}
}

func TestKyuReplRuntimeErrorShowsErrorStyle(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "undefinedVariable")
	sendEnter(w)
	if got := lastLine(t, w); got.style != errorStyle {
		t.Fatalf("style = %v, want errorStyle (text: %q)", got.style, got.text)
	}
}

func TestKyuReplSharedEnvPersistsAcrossSubmits(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "x := 41")
	sendEnter(w)
	sendRunes(w, "x + 1")
	sendEnter(w)
	if got := lastLine(t, w); got.text != "42" {
		t.Fatalf("last line = %q, want 42 (env should persist across submits)", got.text)
	}
}

func TestKyuReplEmptyEnterAddsNoResultLine(t *testing.T) {
	w := newTestReplWidget(t)
	sendEnter(w)
	if len(w.lines) != 1 {
		t.Fatalf("got %d lines, want 1 (just the empty prompt echo), got %v", len(w.lines), w.lines)
	}
}

func TestKyuReplLeftRightMoveCursorForInsertion(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "ac")
	sendKey(w, input.KeyLeft)
	sendRunes(w, "b")
	if w.input != "abc" {
		t.Fatalf("input = %q, want abc", w.input)
	}
	if w.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (right after the inserted b)", w.cursor)
	}
}

func TestKyuReplHomeEndMoveCursor(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abc")
	sendKey(w, input.KeyHome)
	if w.cursor != 0 {
		t.Fatalf("cursor after Home = %d, want 0", w.cursor)
	}
	sendKey(w, input.KeyEnd)
	if w.cursor != 3 {
		t.Fatalf("cursor after End = %d, want 3", w.cursor)
	}
}

func TestKyuReplCtrlAECAliasHomeEnd(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abc")
	sendCtrl(w, 'a')
	if w.cursor != 0 {
		t.Fatalf("cursor after Ctrl+A = %d, want 0", w.cursor)
	}
	sendCtrl(w, 'e')
	if w.cursor != 3 {
		t.Fatalf("cursor after Ctrl+E = %d, want 3", w.cursor)
	}
}

func TestKyuReplDeleteForward(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abc")
	sendKey(w, input.KeyHome)
	sendKey(w, input.KeyDelete)
	if w.input != "bc" {
		t.Fatalf("input after Home+Delete = %q, want bc", w.input)
	}
	if w.cursor != 0 {
		t.Fatalf("cursor after Delete = %d, want unchanged at 0", w.cursor)
	}
}

func TestKyuReplBackspaceMidLine(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abc")
	sendKey(w, input.KeyLeft) // cursor between b and c
	w.HandleEvent(input.KeyEvent{Key: input.KeyBackspace})
	if w.input != "ac" {
		t.Fatalf("input = %q, want ac (deleted the b before the cursor)", w.input)
	}
	if w.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", w.cursor)
	}
}

func TestKyuReplCtrlWKillsWordBackward(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "foo bar")
	sendCtrl(w, 'w')
	if w.input != "foo " {
		t.Fatalf("input after Ctrl+W = %q, want %q", w.input, "foo ")
	}
	if w.cursor != len([]rune(w.input)) {
		t.Fatalf("cursor after Ctrl+W = %d, want end of remaining input", w.cursor)
	}
}

func TestKyuReplCtrlLClearsTranscript(t *testing.T) {
	w := newTestReplWidget(t)
	w.lines = []replLine{{text: "leftover output"}, {text: "more leftover"}}
	w.scrollOffset = 3
	sendCtrl(w, 'l')
	if len(w.lines) != 0 {
		t.Fatalf("lines after Ctrl+L = %v, want empty", w.lines)
	}
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after Ctrl+L = %d, want 0", w.scrollOffset)
	}
}

func TestKyuReplCtrlUKillsToLineStart(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abcdef")
	sendKey(w, input.KeyLeft)
	sendKey(w, input.KeyLeft) // cursor between d and e
	sendCtrl(w, 'u')
	if w.input != "ef" {
		t.Fatalf("input after Ctrl+U = %q, want ef", w.input)
	}
	if w.cursor != 0 {
		t.Fatalf("cursor after Ctrl+U = %d, want 0", w.cursor)
	}
}

func TestKyuReplCtrlKKillsToLineEnd(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abcdef")
	sendKey(w, input.KeyLeft)
	sendKey(w, input.KeyLeft) // cursor between d and e
	sendCtrl(w, 'k')
	if w.input != "abcd" {
		t.Fatalf("input after Ctrl+K = %q, want abcd", w.input)
	}
	if w.cursor != 4 {
		t.Fatalf("cursor after Ctrl+K = %d, want unchanged at 4", w.cursor)
	}
}

func TestKyuReplCtrlLeftRightMoveByWord(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "foo bar baz")
	w.HandleEvent(input.KeyEvent{Key: input.KeyLeft, Mod: input.ModCtrl})
	if w.cursor != 8 { // start of "baz"
		t.Fatalf("cursor after one Ctrl+Left = %d, want 8 (start of baz)", w.cursor)
	}
	w.HandleEvent(input.KeyEvent{Key: input.KeyLeft, Mod: input.ModCtrl})
	if w.cursor != 4 { // start of "bar"
		t.Fatalf("cursor after two Ctrl+Left = %d, want 4 (start of bar)", w.cursor)
	}
	w.HandleEvent(input.KeyEvent{Key: input.KeyRight, Mod: input.ModCtrl})
	if w.cursor != 7 { // end of "bar"
		t.Fatalf("cursor after Ctrl+Right = %d, want 7 (end of bar)", w.cursor)
	}
}

func TestKyuReplHistoryRecall(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 1")
	sendEnter(w)
	sendRunes(w, "2 + 2")
	sendEnter(w)

	sendKey(w, input.KeyUp)
	if w.input != "2 + 2" {
		t.Fatalf("input after one Up = %q, want the most recent submission (2 + 2)", w.input)
	}
	sendKey(w, input.KeyUp)
	if w.input != "1 + 1" {
		t.Fatalf("input after two Ups = %q, want the older submission (1 + 1)", w.input)
	}
	sendKey(w, input.KeyDown)
	if w.input != "2 + 2" {
		t.Fatalf("input after Down = %q, want back to 2 + 2", w.input)
	}
}

func TestKyuReplHistoryDraftRestoredOnDown(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 1")
	sendEnter(w)
	sendRunes(w, "unsent draft")

	sendKey(w, input.KeyUp)
	if w.input != "1 + 1" {
		t.Fatalf("input after Up = %q, want 1 + 1", w.input)
	}
	sendKey(w, input.KeyDown)
	if w.input != "unsent draft" {
		t.Fatalf("input after Down back past the newest entry = %q, want the restored draft", w.input)
	}
}

func TestKyuReplHistoryDisabledMidMultilineInput(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1")
	sendEnter(w)
	sendRunes(w, "if true {")
	sendEnter(w) // still open (bracket depth > 0) -- w.input now contains a real '\n'

	before := w.input
	sendKey(w, input.KeyUp)
	if w.input != before {
		t.Fatalf("Up during multi-line continuation changed input to %q, want unchanged (%q)", w.input, before)
	}
}

func TestKyuReplHistoryModeUniqueMovesDuplicateToEnd(t *testing.T) {
	w := newTestReplWidget(t)
	w.env.Define("history_mode", value.String("unique"))
	sendRunes(w, `"a"`)
	sendEnter(w)
	sendRunes(w, `"b"`)
	sendEnter(w)
	sendRunes(w, `"a"`)
	sendEnter(w)

	want := []string{`"b"`, `"a"`}
	if len(w.history) != len(want) {
		t.Fatalf("history = %v, want %v", w.history, want)
	}
	for i := range want {
		if w.history[i] != want[i] {
			t.Fatalf("history = %v, want %v", w.history, want)
		}
	}
}

func TestKyuReplHistoryModeAllKeepsDuplicates(t *testing.T) {
	w := newTestReplWidget(t) // history_mode unset -- default is "all"
	sendRunes(w, `"a"`)
	sendEnter(w)
	sendRunes(w, `"a"`)
	sendEnter(w)

	want := []string{`"a"`, `"a"`}
	if len(w.history) != len(want) {
		t.Fatalf("history = %v, want %v (duplicates kept)", w.history, want)
	}
}

// TestHistoryBuiltinsReachThisWidget drives history()/history_delete/
// history_clear through a real evaluate() call -- the same
// set-before/clear-after Env.HistoryAccess registration production
// code uses (see evaluate()) -- rather than calling
// historySnapshot/deleteHistoryEntry/clearHistoryEntries directly, so a
// regression in the wiring itself (not just the underlying methods)
// would be caught here. Calling w.evaluate directly (via
// applyEvalResult, applied immediately rather than through submit()'s
// background goroutine and TakePendingMsg), not sendEnter,
// deliberately keeps history_delete/history_clear's own call text out
// of w.history (only submit(), sendEnter's target, appends to it), so
// each assertion below reflects only what the two real submissions put
// there and what the builtin call under test did to it.
func TestHistoryBuiltinsReachThisWidget(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, `"first"`)
	sendEnter(w)
	sendRunes(w, `"second"`)
	sendEnter(w)

	w.applyEvalResult(w.evaluate(`history() | count`))
	if got := w.lines[len(w.lines)-1].text; got != "2" {
		t.Fatalf("history() | count after 2 submissions = %q, want \"2\"", got)
	}

	w.applyEvalResult(w.evaluate(`history_delete(0)`))
	if got := w.lines[len(w.lines)-1].text; got != "true" {
		t.Fatalf("history_delete(0) = %q, want \"true\"", got)
	}
	if len(w.history) != 1 {
		t.Fatalf("history after history_delete(0) = %v, want 1 entry left", w.history)
	}
	if w.history[0] != `"second"` {
		t.Fatalf("history[0] after deleting index 0 = %q, want %q", w.history[0], `"second"`)
	}

	w.applyEvalResult(w.evaluate(`history_clear()`))
	if len(w.history) != 0 {
		t.Fatalf("history after history_clear() = %v, want empty", w.history)
	}
}

func TestKyuReplSearchFindsMatchingHistoryEntry(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, `"alpha"`)
	sendEnter(w)
	sendRunes(w, `"beta"`)
	sendEnter(w)

	sendCtrl(w, 'r')
	if !w.searchMode {
		t.Fatal("Ctrl-R should enter search mode")
	}
	sendRunes(w, "alp")
	if w.searchIndex >= len(w.history) || w.history[w.searchIndex] != `"alpha"` {
		t.Fatalf("search for %q matched index %d, want the \"alpha\" entry", "alp", w.searchIndex)
	}
}

func TestKyuReplSearchRepeatedCtrlRGoesFurtherBack(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, `"match one"`)
	sendEnter(w)
	sendRunes(w, `"match two"`)
	sendEnter(w)

	sendCtrl(w, 'r')
	sendRunes(w, "match")
	if w.history[w.searchIndex] != `"match two"` {
		t.Fatalf("first match = %q, want the most recent (\"match two\")", w.history[w.searchIndex])
	}
	sendCtrl(w, 'r')
	if w.history[w.searchIndex] != `"match one"` {
		t.Fatalf("second Ctrl-R match = %q, want the older entry (\"match one\")", w.history[w.searchIndex])
	}
}

func TestKyuReplSearchEnterSubmitsMatch(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 1")
	sendEnter(w)

	sendCtrl(w, 'r')
	sendRunes(w, "1 + 1")
	sendEnter(w)

	if w.searchMode {
		t.Fatal("Enter should exit search mode")
	}
	if got := lastLine(t, w); got.text != "2" {
		t.Fatalf("last line = %q, want 2 (the matched \"1 + 1\" actually ran)", got.text)
	}
	if w.input != "" {
		t.Fatalf("input after a submitting Enter should be cleared, got %q", w.input)
	}
}

func TestKyuReplSearchEscLoadsMatchWithoutSubmitting(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 1")
	sendEnter(w)
	linesBeforeSearch := len(w.lines)

	sendCtrl(w, 'r')
	sendRunes(w, "1 + 1")
	sendKey(w, input.KeyEsc)

	if w.searchMode {
		t.Fatal("Esc should exit search mode")
	}
	if w.input != "1 + 1" {
		t.Fatalf("input after Esc = %q, want the matched entry loaded for further editing", w.input)
	}
	if len(w.lines) != linesBeforeSearch {
		t.Fatalf("Esc should not submit/evaluate anything, transcript grew from %d to %d lines", linesBeforeSearch, len(w.lines))
	}
}

// spansText concatenates a line's spans back into plain text -- every
// test below checks this equals the original input, the invariant
// highlightSpans's own doc comment promises.
func spansText(spans []replSpan) string {
	var b strings.Builder
	for _, sp := range spans {
		b.WriteString(sp.text)
	}
	return b.String()
}

func TestHighlightSpansKeywordStringIdent(t *testing.T) {
	src := `if x == "hi" { true }`
	spans := highlightSpans(src, nil)[1]
	if got := spansText(spans); got != src {
		t.Fatalf("spans reconstruct to %q, want %q", got, src)
	}
	var gotIf, gotStr, gotIdent bool
	for _, sp := range spans {
		switch {
		case sp.text == "if" && sp.style == keywordStyle:
			gotIf = true
		case sp.text == `"hi"` && sp.style == stringStyle:
			gotStr = true
		case sp.text == "x" && sp.style == resultStyle:
			gotIdent = true
		}
	}
	if !gotIf {
		t.Errorf("expected an \"if\" span styled as a keyword, got %#v", spans)
	}
	if !gotStr {
		t.Errorf(`expected a "hi" span (with quotes) styled as a string, got %#v`, spans)
	}
	if !gotIdent {
		t.Errorf("expected an \"x\" span styled unstyled (identifier), got %#v", spans)
	}
}

func TestHighlightSpansStringWithEscapeReconstructsRawText(t *testing.T) {
	// the raw source is 10 runes ("a\nb" with literal backslash-n,
	// plus quotes) -- the *decoded* Literal is only 3 runes (a, \n, b)
	// -- stringRawLen has to use the raw length, not len(Literal).
	src := `"a\nb"`
	spans := highlightSpans(src, nil)[1]
	if got := spansText(spans); got != src {
		t.Fatalf("spans reconstruct to %q, want %q (raw source, not decoded)", got, src)
	}
}

func TestHighlightSpansSigilsAndPath(t *testing.T) {
	src := `%ls "/local"`
	spans := highlightSpans(src, nil)[1]
	if got := spansText(spans); got != src {
		t.Fatalf("spans reconstruct to %q, want %q", got, src)
	}
	var gotSigil bool
	for _, sp := range spans {
		if sp.text == "%" && sp.style == sigilStyle {
			gotSigil = true
		}
	}
	if !gotSigil {
		t.Errorf("expected a \"%%\" span styled as a sigil, got %#v", spans)
	}
}

func TestHighlightSpansMultiLineAssignsCorrectLine(t *testing.T) {
	src := "while true {\nbreak\n}"
	byLine := highlightSpans(src, nil)
	if got := spansText(byLine[1]); got != "while true {" {
		t.Errorf("line 1 = %q, want %q", got, "while true {")
	}
	if got := spansText(byLine[2]); got != "break" {
		t.Errorf("line 2 = %q, want %q", got, "break")
	}
	if got := spansText(byLine[3]); got != "}" {
		t.Errorf("line 3 = %q, want %q", got, "}")
	}
	var gotWhile, gotBreak bool
	for _, sp := range byLine[1] {
		if sp.text == "while" && sp.style == keywordStyle {
			gotWhile = true
		}
	}
	for _, sp := range byLine[2] {
		if sp.text == "break" && sp.style == keywordStyle {
			gotBreak = true
		}
	}
	if !gotWhile || !gotBreak {
		t.Errorf("expected while/break both styled as keywords on their own lines, got %#v / %#v", byLine[1], byLine[2])
	}
}

func TestHighlightSpansLexErrorStillReconstructsText(t *testing.T) {
	// an unterminated string is a real lex-time situation (the lexer
	// just runs to EOF looking for the closing quote) -- highlighting
	// must not panic or drop text even when input isn't valid syntax.
	src := `"unterminated`
	spans := highlightSpans(src, nil)[1]
	if got := spansText(spans); got != src {
		t.Fatalf("spans reconstruct to %q, want %q", got, src)
	}
}

func TestHighlightSpansEmptyInput(t *testing.T) {
	if spans := highlightSpans("", nil)[1]; len(spans) != 0 {
		t.Errorf("empty input should produce no spans, got %#v", spans)
	}
}

func TestKyuReplLiveInputHasHighlightSpans(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, `if true`)
	lines, _, _ := w.renderInput()
	if len(lines) != 1 || lines[0].spans == nil {
		t.Fatalf("expected the live input line to carry highlight spans, got %#v", lines)
	}
	if got := spansText(lines[0].spans); got != "9sh> if true" {
		t.Fatalf("spans reconstruct to %q, want %q", got, "9sh> if true")
	}
}

func TestKyuReplTabCompletesUniqueKeyword(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "whil")
	sendKey(w, input.KeyTab)
	if w.input != "while" {
		t.Fatalf("input = %q, want %q (the only candidate starting with \"whil\")", w.input, "while")
	}
	if w.cursor != len(w.runes()) {
		t.Fatalf("cursor = %d, want it right after the completed text (%d)", w.cursor, len(w.runes()))
	}
}

func TestKyuReplTabCompletesBuiltin(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "wher")
	sendKey(w, input.KeyTab)
	if w.input != "where" {
		t.Fatalf("input = %q, want %q", w.input, "where")
	}
}

func TestKyuReplTabCompletesUserDefinedVariable(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "myvar123 := 5")
	sendEnter(w)

	sendRunes(w, "myvar")
	sendKey(w, input.KeyTab)
	if w.input != "myvar123" {
		t.Fatalf("input = %q, want %q (a variable defined earlier this session)", w.input, "myvar123")
	}
}

// writeFakeExecutable creates dir/name as an executable file — enough
// for pathresolve.isExecutable, which only checks the mode bits, not
// that the file is actually runnable.
func writeFakeExecutable(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing fake executable %s: %v", name, err)
	}
}

// newTestReplWidget's Env has no /env bound (eval.NewGlobalEnv(nil)'s
// namespace is nil), so EnvSlice returns (nil, nil) and
// externalNameCandidates falls back to pathresolve.Names(nil) — this
// process's own real os.Getenv("PATH"). t.Setenv here is what lets
// these tests control that fallback deterministically instead of
// depending on whatever happens to be on the test runner's real PATH.

func TestKyuReplTabCompletesExternalCommandAfterPercentSigil(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "widget-tool")
	t.Setenv("PATH", dir)

	w := newTestReplWidget(t)
	sendRunes(w, "%widget")
	sendKey(w, input.KeyTab)
	if w.input != "%widget-tool" {
		t.Fatalf("input = %q, want %q", w.input, "%widget-tool")
	}
}

func TestKyuReplTabExternalCommandFragmentIncludesHyphen(t *testing.T) {
	// lexExternalName allows internal hyphens in an external command
	// name (docker-compose, apt-get, ...) -- the fragment scan here
	// must match that, or a Tab typed mid-name would only replace the
	// text after the last hyphen instead of the whole name.
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "apt-get")
	t.Setenv("PATH", dir)

	w := newTestReplWidget(t)
	sendRunes(w, "%apt-g")
	sendKey(w, input.KeyTab)
	if w.input != "%apt-get" {
		t.Fatalf("input = %q, want %q", w.input, "%apt-get")
	}
}

func TestKyuReplTabExternalCommandNoMatchIsNoop(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "widget-tool")
	t.Setenv("PATH", dir)

	w := newTestReplWidget(t)
	sendRunes(w, "%zzz_no_such_thing")
	before := w.input
	sendKey(w, input.KeyTab)
	if w.input != before {
		t.Fatalf("input changed to %q with no matching PATH executable, want unchanged (%q)", w.input, before)
	}
}

func TestKyuReplTabExternalCommandIgnoresNonExecutableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "widget-notes"), []byte("not a program"), 0o644); err != nil {
		t.Fatalf("writing non-executable file: %v", err)
	}
	t.Setenv("PATH", dir)

	w := newTestReplWidget(t)
	sendRunes(w, "%widget")
	before := w.input
	sendKey(w, input.KeyTab)
	if w.input != before {
		t.Fatalf("input changed to %q, want unchanged (%q) -- a non-executable file must not be offered as a completion", w.input, before)
	}
}

// TestKyuReplTabCompletesRealFilesystemPath locks in pathCandidates'
// real-filesystem source: a bare Path fragment (no namespace involved at
// all -- newTestReplWidget's Env has a nil namespace) completes against
// an ordinary real directory the same way external-command completion
// already resolves PATH executables.
func TestKyuReplTabCompletesRealFilesystemPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real-file.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	w := newTestReplWidget(t)
	sendRunes(w, filepath.Join(dir, "real-f"))
	sendKey(w, input.KeyTab)
	want := filepath.Join(dir, "real-file.txt")
	if w.input != want {
		t.Fatalf("input = %q, want %q", w.input, want)
	}
}

// TestKyuReplTabCompletesNamespacePath is the namespace-side sibling:
// /x/a.txt only exists inside the bound namespace (newNamespaceTestEnv's
// dirfs backing directory has a different real path entirely), so this
// only passes if pathCandidates' namespace source (Env.ListNamespaceDir)
// is actually contributing, not just the real-filesystem source.
func TestKyuReplTabCompletesNamespacePath(t *testing.T) {
	env := newNamespaceTestEnv(t)
	w := &kyuReplWidget{env: env}
	sendRunes(w, "/x/a")
	sendKey(w, input.KeyTab)
	if w.input != "/x/a.txt" {
		t.Fatalf("input = %q, want %q", w.input, "/x/a.txt")
	}
}

// TestKyuReplTabCompletesNamespaceDirWithTrailingSlash locks in that a
// namespace directory (not just a real one) gets the same trailing '/'
// convenience real dirs already get, using each entry's Qid.IsDir() —
// without it, a directory dead-ends after one Tab instead of inviting a
// second one straight into it.
func TestKyuReplTabCompletesNamespaceDirWithTrailingSlash(t *testing.T) {
	env := newNamespaceTestEnv(t)
	w := &kyuReplWidget{env: env}
	sendRunes(w, "/x/su")
	sendKey(w, input.KeyTab)
	if w.input != "/x/sub/" {
		t.Fatalf("input = %q, want %q", w.input, "/x/sub/")
	}
}

// TestKyuReplTabPathCompletionAsExternalCallArgument exercises the
// motivating end-to-end case: a bare Path typed as a %cmd argument (the
// kyu/lexer lastWasExternalName fix is what makes this position lex as a
// Path at all) completes against the namespace, not just standalone.
func TestKyuReplTabPathCompletionAsExternalCallArgument(t *testing.T) {
	env := newNamespaceTestEnv(t)
	w := &kyuReplWidget{env: env}
	sendRunes(w, "%cat /x/a")
	sendKey(w, input.KeyTab)
	if w.input != "%cat /x/a.txt" {
		t.Fatalf("input = %q, want %q", w.input, "%cat /x/a.txt")
	}
}

// TestKyuReplTabPathCompletionNoMatchIsNoop mirrors the external-command
// no-match test: a fragment matching nothing in either source leaves the
// input untouched rather than, say, clearing it.
func TestKyuReplTabPathCompletionNoMatchIsNoop(t *testing.T) {
	env := newNamespaceTestEnv(t)
	w := &kyuReplWidget{env: env}
	sendRunes(w, "/x/zzz_no_such_thing")
	before := w.input
	sendKey(w, input.KeyTab)
	if w.input != before {
		t.Fatalf("input changed to %q, want unchanged (%q)", w.input, before)
	}
}

// TestKyuReplTabBarewordIsNotTreatedAsPath is the regression guard for
// currentPathBounds' '/'-prefix requirement: a plain identifier fragment
// (no leading '/') must still complete against env.Names()/kyuKeywords,
// even though its characters alone (letters) also satisfy isPathRune.
func TestKyuReplTabBarewordIsNotTreatedAsPath(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "wher")
	sendKey(w, input.KeyTab)
	if w.input != "where" {
		t.Fatalf("input = %q, want %q (bareword completion, not path completion)", w.input, "where")
	}
}

func TestKyuReplTabFillsCommonPrefixThenCyclesOnRepeat(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "xfoo := 1")
	sendEnter(w)
	sendRunes(w, "xfoobar := 2")
	sendEnter(w)

	sendRunes(w, "x")
	sendKey(w, input.KeyTab)
	if w.input != "xfoo" {
		t.Fatalf("first Tab: input = %q, want the common prefix %q", w.input, "xfoo")
	}

	sendKey(w, input.KeyTab)
	if w.input != "xfoo" && w.input != "xfoobar" {
		t.Fatalf("repeated Tab: input = %q, want one of the two candidates", w.input)
	}
	first := w.input
	sendKey(w, input.KeyTab)
	if w.input == first {
		t.Fatalf("a third Tab should cycle to the *other* candidate, still got %q", w.input)
	}
	// wraps back around to the first cycled candidate
	sendKey(w, input.KeyTab)
	if w.input != first {
		t.Fatalf("cycling should wrap around: got %q, want back to %q", w.input, first)
	}
}

func TestKyuReplTabNoMatchesIsNoop(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "zzz_no_such_thing")
	before := w.input
	sendKey(w, input.KeyTab)
	if w.input != before {
		t.Fatalf("input changed to %q with no matching candidates, want unchanged (%q)", w.input, before)
	}
}

func TestKyuReplTabCycleResetsOnUnrelatedKey(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "xfoo := 1")
	sendEnter(w)
	sendRunes(w, "xfoobar := 2")
	sendEnter(w)

	sendRunes(w, "x")
	sendKey(w, input.KeyTab) // fills to "xfoo"
	sendRunes(w, "z")        // unrelated typing -- input is now "xfooz"
	if w.completionCandidates != nil {
		t.Fatal("typing a non-Tab key should clear the completion cycle state")
	}
}

// TestKyuReplClaimsRawTab locks in a real, previously-missing piece of
// wiring: tui.App.HandleInput intercepts Tab globally for focus
// navigation *before* it ever reaches a widget's own HandleEvent,
// unless the focused widget implements tui.RawKeyClaimer and
// WantsRawTab() returns true (see that interface's own doc comment).
// Without this, completeTab works perfectly in every direct-HandleEvent
// test above and is still silently unreachable dead code in the real
// app -- this test would have caught that, none of the others could.
func TestKyuReplClaimsRawTab(t *testing.T) {
	w := newTestReplWidget(t)
	var _ tui.RawKeyClaimer = w // compile-time: must implement the interface
	if !w.WantsRawTab() {
		t.Fatal("kyuReplWidget must claim raw Tab, or completeTab is unreachable through tui.App")
	}
	want := input.KeyEvent{Rune: '\\', Mod: input.ModCtrl}
	if got := w.ReleaseKey(); got != want {
		t.Errorf("ReleaseKey() = %+v, want Ctrl+\\ (%+v), matching widget.Terminal's own convention", got, want)
	}
}

func TestKyuReplPasteInsertsTextAtCursor(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "ac")
	sendKey(w, input.KeyLeft)
	w.HandleEvent(input.PasteEvent{Text: "XY"})
	if w.input != "aXYc" {
		t.Fatalf("input after paste = %q, want aXYc", w.input)
	}
	if w.cursor != 3 {
		t.Fatalf("cursor after paste = %d, want 3 (right after the pasted text)", w.cursor)
	}
}

func TestKyuReplCtrlCCopiesAllText(t *testing.T) {
	w := newTestReplWidget(t)
	w.lastHeight = 2 // force a tiny viewport -- Ctrl+C must ignore it
	sendRunes(w, `"early"`)
	sendEnter(w)
	sendRunes(w, `"late"`)
	sendEnter(w)

	cmd := w.HandleEvent(input.KeyEvent{Rune: 'c', Mod: input.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a Cmd from Ctrl+C")
	}
	msg, ok := cmd().(tui.ClipboardMsg)
	if !ok {
		t.Fatalf("Cmd produced %T, want tui.ClipboardMsg", cmd())
	}
	if !strings.Contains(msg.Text, "early") || !strings.Contains(msg.Text, "late") {
		t.Fatalf("copy-all text = %q, want both early and late scrolled-out-of-view content included", msg.Text)
	}
}

// TestKyuReplCtrlCInterruptsWhileBusy confirms handleKey's busy gate
// (see busy's own doc comment) routes Ctrl+C to Env.InterruptHandler
// instead of the clipboard-copy binding TestKyuReplCtrlCCopiesAllText
// exercises above -- the actual fix for the TUI's Ctrl+C freeze. A real
// foreground %cmd's interrupt handler is wired by runExternalViaJob/
// runExternalDirect, not simulated here -- see
// TestCtrlCInterruptsRunningForegroundCommand in model_test.go for the
// real-subprocess end-to-end version of this.
func TestKyuReplCtrlCInterruptsWhileBusy(t *testing.T) {
	w := newTestReplWidget(t)
	var interrupted bool
	w.env.SetInterruptHandler(func() { interrupted = true })
	w.busy = true

	if cmd := w.HandleEvent(input.KeyEvent{Rune: 'c', Mod: input.ModCtrl}); cmd != nil {
		t.Fatalf("Ctrl+C while busy should not also copy to clipboard, got Cmd %T", cmd())
	}
	if !interrupted {
		t.Fatal("Ctrl+C while busy should call Env.InterruptHandler")
	}
}

// TestKyuReplBusyBlocksOtherKeysExceptCtrlC confirms the rest of busy's
// gate: every key except Ctrl+C is a no-op while an evaluate() is in
// flight -- this is what makes a second, overlapping evaluate() call
// (and the Env hook clobbering that would cause -- see evaluate's own
// doc comment) impossible.
func TestKyuReplBusyBlocksOtherKeysExceptCtrlC(t *testing.T) {
	w := newTestReplWidget(t)
	w.busy = true
	w.input = "abc"
	w.cursor = 3

	sendRunes(w, "XYZ")
	if w.input != "abc" {
		t.Fatalf("typing while busy should be a no-op, input = %q", w.input)
	}

	if cmd := w.HandleEvent(input.KeyEvent{Key: input.KeyEnter}); cmd != nil {
		t.Fatalf("Enter while busy should be a no-op, got Cmd %T", cmd())
	}
	if w.input != "abc" {
		t.Fatalf("Enter while busy mutated input to %q", w.input)
	}
	if w.busy != true {
		t.Fatal("busy should stay true -- only applyEvalResult clears it")
	}
}

// TestKyuReplSubmitOfNonBlankSourceGoesBusy confirms submit() goes busy
// and spawns a background evaluate() for a non-blank source, but not
// for a blank one (matching evaluate()'s own no-op-on-blank-input
// guard) -- see submit's own doc comment. The Cmd it hands back is
// deliberately NOT the eval result (see evalStartedMsg's doc comment
// for why that would silently reintroduce the freeze this whole fix is
// for) -- it's checked here too, so a regression back to that shape
// would fail loudly.
func TestKyuReplSubmitOfNonBlankSourceGoesBusy(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 2")
	cmd := w.HandleEvent(input.KeyEvent{Key: input.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a Cmd for a non-blank submission")
	}
	if !w.busy {
		t.Fatal("expected busy=true right after submitting, before evaluate() has finished")
	}
	if _, ok := cmd().(evalStartedMsg); !ok {
		t.Fatalf("expected submit()'s Cmd to be the trivial evalStartedMsg, got %T -- "+
			"a real evaluate() call belongs on submit()'s own background goroutine, "+
			"never inside a widget-returned Cmd (see evalStartedMsg's doc comment)", cmd())
	}
	waitForEvalDone(w)
	if w.busy {
		t.Fatal("expected busy=false once the background evaluate() result was applied")
	}

	w2 := newTestReplWidget(t)
	if cmd := w2.HandleEvent(input.KeyEvent{Key: input.KeyEnter}); cmd != nil {
		t.Fatalf("expected no Cmd for a blank submission, got %T", cmd())
	}
	if w2.busy {
		t.Fatal("a blank submission should never go busy")
	}
}

func TestKyuReplAltCCopiesOnlyVisibleText(t *testing.T) {
	w := newTestReplWidget(t)
	w.lastHeight = 2 // force a tiny viewport
	sendRunes(w, `"early"`)
	sendEnter(w)
	sendRunes(w, `"late"`)
	sendEnter(w)

	cmd := w.HandleEvent(input.KeyEvent{Rune: 'c', Mod: input.ModAlt})
	if cmd == nil {
		t.Fatal("expected a Cmd from Alt+C")
	}
	msg, ok := cmd().(tui.ClipboardMsg)
	if !ok {
		t.Fatalf("Cmd produced %T, want tui.ClipboardMsg", cmd())
	}
	if strings.Contains(msg.Text, "early") {
		t.Fatalf("visible-only copy included content scrolled out of the (tiny, 2-line) viewport:\n%s", msg.Text)
	}
	if !strings.Contains(msg.Text, "late") {
		t.Fatalf("visible-only copy missing the latest, in-viewport content:\n%s", msg.Text)
	}
}

func TestKyuReplPgUpPgDownAdjustScrollOffset(t *testing.T) {
	w := newTestReplWidget(t)
	w.lastHeight = 10 // Paint sets this normally; fake it for a pure HandleEvent test

	w.HandleEvent(input.KeyEvent{Key: input.KeyPgUp})
	if w.scrollOffset != 9 { // lastHeight - 1, see HandleEvent's own comment
		t.Fatalf("scrollOffset after one PgUp = %d, want 9", w.scrollOffset)
	}

	w.HandleEvent(input.KeyEvent{Key: input.KeyPgDown})
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after PgUp then PgDown = %d, want back to 0", w.scrollOffset)
	}

	// PgDown past 0 clamps rather than going negative — "scroll below
	// the latest line" isn't a meaningful state.
	w.HandleEvent(input.KeyEvent{Key: input.KeyPgDown})
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after PgDown at 0 = %d, want clamped to 0", w.scrollOffset)
	}
}

func TestKyuReplMouseWheelScrolls(t *testing.T) {
	w := newTestReplWidget(t)

	w.HandleEvent(input.MouseEvent{Button: input.MouseWheelUp})
	if w.scrollOffset != scrollStep {
		t.Fatalf("scrollOffset after one wheel-up = %d, want %d", w.scrollOffset, scrollStep)
	}

	w.HandleEvent(input.MouseEvent{Button: input.MouseWheelDown})
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after wheel-up then wheel-down = %d, want back to 0", w.scrollOffset)
	}

	w.HandleEvent(input.MouseEvent{Button: input.MouseWheelDown})
	if w.scrollOffset != 0 {
		t.Fatalf("scrollOffset after wheel-down at 0 = %d, want clamped to 0", w.scrollOffset)
	}
}

func TestKyuReplPaintDoesNotPanicOnTinyRect(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "1 + 1")
	sendEnter(w)
	// Paint needs a *cell.Painter; exercised indirectly via the full
	// integration tests in model_test.go, which drive real tui.App
	// rendering. This test just confirms Focusable/SetFocused/Reconcile
	// satisfy the Widget interface without a separate compile-time
	// assertion elsewhere.
	if !w.Focusable() {
		t.Fatal("kyuReplWidget should be focusable")
	}
	w.SetFocused(true)
	if !w.focused {
		t.Fatal("SetFocused(true) should set focused")
	}
	w.Reconcile(struct{}{})
}

// TestKyuReplF1IsNotABinding locks in that no function key opens help:
// terminal emulators keep them for themselves, so `?` is the only way in
// — see handleKey's own doc comment.
func TestKyuReplF1IsNotABinding(t *testing.T) {
	w := newTestReplWidget(t)
	if cmd := w.HandleEvent(input.KeyEvent{Key: input.KeyF1}); cmd != nil {
		t.Fatalf("F1 must not produce a Cmd, got %T", cmd())
	}
	if w.input != "" {
		t.Errorf("F1 must not type anything, input = %q", w.input)
	}
}

// TestKyuReplCtrlDAtEmptyPromptQuits locks in the bash/zsh-style
// "Ctrl-D at an empty prompt exits" convention this package uses in
// place of a quit button.
func TestKyuReplCtrlDAtEmptyPromptQuits(t *testing.T) {
	w := newTestReplWidget(t)
	cmd := w.HandleEvent(input.KeyEvent{Rune: 'd', Mod: input.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a Cmd from Ctrl+D at an empty prompt")
	}
	if _, ok := cmd().(tui.QuitMsg); !ok {
		t.Fatalf("Cmd produced %T, want tui.QuitMsg", cmd())
	}
}

// TestKyuReplCtrlDMidInputIsNoop confirms Ctrl+D only quits at an
// empty prompt -- typed input must never be silently discarded.
func TestKyuReplCtrlDMidInputIsNoop(t *testing.T) {
	w := newTestReplWidget(t)
	sendRunes(w, "abc")
	cmd := w.HandleEvent(input.KeyEvent{Rune: 'd', Mod: input.ModCtrl})
	if cmd != nil {
		t.Fatalf("expected no Cmd from Ctrl+D mid-input, got one (input: %q)", w.input)
	}
	if w.input != "abc" {
		t.Fatalf("input = %q, want unchanged (abc)", w.input)
	}
}

// TestKyuReplQuestionMarkOpensHelpOnlyAtAnEmptyPrompt: `?` is the only
// way in (function keys are left to the terminal emulator). It may only claim the key where nothing else could mean
// it — anywhere else `?` is ordinary text, notably the postfix
// error-check operator in `f()?`.
func TestKyuReplQuestionMarkOpensHelpOnlyAtAnEmptyPrompt(t *testing.T) {
	q := input.KeyEvent{Rune: '?'}

	w := newTestReplWidget(t)
	cmd := w.HandleEvent(q)
	if cmd == nil {
		t.Fatal("expected a Cmd from ? at an empty prompt")
	}
	if _, ok := cmd().(toggleHelpMsg); !ok {
		t.Fatalf("Cmd produced %T, want toggleHelpMsg", cmd())
	}
	if w.input != "" {
		t.Errorf("opening help must not type a '?' into the prompt, input = %q", w.input)
	}

	// After text: an ordinary character, no help.
	w = newTestReplWidget(t)
	w.HandleEvent(input.KeyEvent{Rune: 'f'})
	if cmd := w.HandleEvent(q); cmd != nil {
		t.Errorf("? after text must not open help, got Cmd %T", cmd())
	}
	if w.input != "f?" {
		t.Errorf("input = %q, want %q (the ? is the error-check operator)", w.input, "f?")
	}

	// Mid multi-line continuation the input is non-empty (it holds the
	// earlier lines), so it is text there too.
	w = newTestReplWidget(t)
	for _, r := range "f(" {
		w.HandleEvent(input.KeyEvent{Rune: r})
	}
	w.HandleEvent(input.KeyEvent{Key: input.KeyEnter})
	if cmd := w.HandleEvent(q); cmd != nil {
		t.Errorf("? inside an open continuation must not open help, got Cmd %T", cmd())
	}

	// A modified '?' (Ctrl/Alt) is some other binding, never help.
	for _, mod := range []input.Mod{input.ModCtrl, input.ModAlt} {
		w = newTestReplWidget(t)
		if cmd := w.HandleEvent(input.KeyEvent{Rune: '?', Mod: mod}); cmd != nil {
			t.Errorf("modified ? (mod %v) must not open help, got Cmd %T", mod, cmd())
		}
	}
}
