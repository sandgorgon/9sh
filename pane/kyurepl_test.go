package pane

import (
	"strings"
	"testing"

	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
)

func newTestReplWidget(t *testing.T) *kyuReplWidget {
	t.Helper()
	return &kyuReplWidget{env: eval.NewGlobalEnv(nil)}
}

func sendRunes(w *kyuReplWidget, s string) {
	for _, r := range s {
		w.HandleEvent(input.KeyEvent{Rune: r})
	}
}

func sendEnter(w *kyuReplWidget) {
	w.HandleEvent(input.KeyEvent{Key: input.KeyEnter})
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
	// pane integration tests in model_test.go/browser_test.go, which
	// drive real tui.App rendering. This test just confirms Focusable/
	// SetFocused/Reconcile satisfy the Widget interface without a
	// separate compile-time assertion elsewhere.
	if !w.Focusable() {
		t.Fatal("kyuReplWidget should be focusable")
	}
	w.SetFocused(true)
	if !w.focused {
		t.Fatal("SetFocused(true) should set focused")
	}
	w.Reconcile(struct{}{})
}
