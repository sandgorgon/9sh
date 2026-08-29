package pane

import (
	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/parser"
)

var (
	promptStyle = cell.Style{Fg: cell.ANSIColor(6)}
	resultStyle = cell.Style{}
	errorStyle  = cell.Style{Fg: cell.ANSIColor(1)}
)

// kyuReplNode is a native (not pty-hosted) kyu REPL: input is
// evaluated directly against env — the same *eval.Env every other
// 9sh entry point shares (see cmd/9sh's bootstrap) — rather than
// shelling out to another 9sh process. env is captured once at
// construction, like widget.Terminal's Command; it never changes for
// this pane's lifetime, so it isn't threaded through Reconcile props.
func kyuReplNode(id int, env *eval.Env) tui.Node {
	return tui.Component(paneKey(id, "kyurepl"), struct{}{}, func() tui.Widget {
		return &kyuReplWidget{env: env}
	})
}

type replLine struct {
	text  string
	style cell.Style
}

// kyuReplWidget's own scope, deliberately: no cursor movement within
// the input line, no history recall — the same feature set as
// cmd/9sh's own line-based REPL, just rendered in a pane instead of
// raw stdout. A real line editor is future work, not required to make
// this pane a genuine, useful kyu entry point today.
type kyuReplWidget struct {
	env     *eval.Env
	lines   []replLine
	input   string
	focused bool
}

func (w *kyuReplWidget) Reconcile(props any) bool { return true }

func (w *kyuReplWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	if width <= 0 || height <= 0 {
		return
	}

	visible := append(append([]replLine(nil), w.lines...), w.inputLines()...)
	start := max0(len(visible) - height)
	shown := visible[start:]
	for y, ln := range shown {
		p.Text(0, y, ln.text, ln.style)
	}

	// tui.App's real terminal cursor is unconditionally hidden (see its
	// Run loop's hardcoded renderer.Render(..., false)) — every
	// focusable text-entry widget has to draw its own soft cursor
	// instead, the same reverse-video-cell technique widget.Terminal
	// already uses against its embedded VT screen's cursor. This
	// widget has no cursor movement within the input line (see the
	// type doc comment: append/backspace only), so the cursor is
	// always at the end of the last visible line, right past whatever
	// text is there.
	if w.focused && len(shown) > 0 {
		cy := len(shown) - 1
		cx := len([]rune(shown[cy].text))
		if cx < width {
			p.SetCell(cx, cy, ' ', cell.Style{Attr: cell.AttrReverse})
		}
	}
}

// inputLines renders the in-progress (possibly multi-line) input with
// the same "9sh> " / "...  " prompt convention as cmd/9sh's line REPL.
func (w *kyuReplWidget) inputLines() []replLine {
	var out []replLine
	start := 0
	for i := 0; i <= len(w.input); i++ {
		if i < len(w.input) && w.input[i] != '\n' {
			continue
		}
		prefix := "9sh> "
		if len(out) > 0 {
			prefix = "...  "
		}
		out = append(out, replLine{text: prefix + w.input[start:i], style: promptStyle})
		start = i + 1
	}
	return out
}

func (w *kyuReplWidget) HandleEvent(e input.Event) tui.Cmd {
	ke, ok := e.(input.KeyEvent)
	if !ok {
		return nil
	}
	switch {
	case ke.Key == input.KeyEnter:
		w.submit()
	case ke.Key == input.KeyBackspace:
		w.backspace()
	case ke.Rune != 0 && ke.Key == input.KeyNone:
		w.input += string(ke.Rune)
	}
	return nil
}

func (w *kyuReplWidget) backspace() {
	r := []rune(w.input)
	if len(r) == 0 {
		return
	}
	w.input = string(r[:len(r)-1])
}

func (w *kyuReplWidget) submit() {
	candidate := w.input + "\n"
	if parser.BracketDepth(candidate) > 0 {
		w.input = candidate
		return
	}
	w.lines = append(w.lines, w.inputLines()...)
	w.evaluate(w.input)
	w.input = ""
}

func (w *kyuReplWidget) evaluate(src string) {
	if !trimmedNonEmpty(src) {
		return
	}
	p := parser.New(src)
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		for _, e := range errs {
			w.lines = append(w.lines, replLine{text: e.Error(), style: errorStyle})
		}
		return
	}
	v, err := eval.Eval(prog, w.env)
	if err != nil {
		w.lines = append(w.lines, replLine{text: err.Error(), style: errorStyle})
		return
	}
	if v.Kind() != "null" {
		w.lines = append(w.lines, replLine{text: v.String(), style: resultStyle})
	}
}

func trimmedNonEmpty(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}

func (w *kyuReplWidget) Focusable() bool         { return true }
func (w *kyuReplWidget) SetFocused(focused bool) { w.focused = focused }
