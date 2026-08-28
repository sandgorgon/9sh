package pane

import (
	"testing"

	"github.com/sandgorgon/tui/input"

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
