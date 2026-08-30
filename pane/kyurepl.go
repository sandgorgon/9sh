package pane

import (
	"strings"

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

	// cursorStyle is an explicit, theme-independent block color (ANSI
	// yellow bg, black fg) rather than bare AttrReverse against the
	// terminal's own default colors — reported invisible in practice
	// (2026-08-30): reverse-video against two *unset* colors doesn't
	// reliably read as a visible block on every terminal's own default
	// palette. Yellow reads clearly against both light and dark
	// backgrounds, which this pane can't assume just one of (see New's
	// own doc comment on theme detection).
	cursorStyle = cell.Style{Bg: cell.ANSIColor(3), Fg: cell.ANSIColor(0), Attr: cell.AttrBold}
)

// promptWidth is len("9sh> ") == len("...  ") — both prompts are the
// same width by construction, so cursor-column math in Paint/
// renderInput can rely on one constant rather than measuring per line.
const promptWidth = 5

// scrollStep is the mouse wheel's line-at-a-time scroll amount — a
// wheel "click" conventionally moves a few lines, not a whole page
// (PgUp/PgDown's job).
const scrollStep = 3

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

// kyuReplWidget is a native (in-pane) line editor + transcript for
// kyu: cursor movement (char- and word-wise), Home/End, history
// recall (Up/Down, only when not mid multi-line continuation — see
// historyPrev/historyNext), kill-to-line-start/kill-to-line-end/kill-
// word-backward (Ctrl+U/Ctrl+K/Ctrl+W), bracketed paste, and scrolling
// the transcript independently of the input (scrollOffset). Was
// deliberately scoped down to append/backspace-only editing with no
// history when this pane was a fallback, not the primary way into
// 9sh — this rewrite (2026-08-30) closes that gap now that it is.
//
// Deliberately still no undo/redo: this is a REPL input line, not a
// general text editor, and the kill commands already cover the common
// "I typed the wrong thing, get rid of it" cases undo would otherwise
// be for — a real scope cut, not an oversight, if it turns out to be
// missed.
type kyuReplWidget struct {
	env     *eval.Env
	lines   []replLine
	input   string
	cursor  int // rune index into []rune(input), 0..len(runes(input))
	focused bool

	// history is every submitted top-level input (the full, possibly
	// multi-line, source — not one entry per line), oldest first.
	// historyIndex == len(history) means "not currently browsing
	// history"; historyDraft is whatever was being typed when Up first
	// left that position, restored if Down returns to it — the same
	// "don't lose your half-typed line just for glancing at history"
	// nicety most shells give you.
	history      []string
	historyIndex int
	historyDraft string

	// scrollOffset is how many lines back from the bottom the
	// transcript view is scrolled — 0 means pinned to the latest
	// output. Not clamped here: Paint's own max0 already makes
	// scrolling arbitrarily far past the top a harmless no-op (start
	// just floors at 0), so callers don't need to know the
	// transcript's current length to stay safe. lastHeight is Paint's
	// most recent height, remembered so PgUp/PgDown can scroll by a
	// real page — the same "widget remembers its own last geometry for
	// later event handling" shape widget.Terminal already uses
	// (lastCols/lastRows), just for scroll math instead of pty
	// resizing.
	scrollOffset int
	lastHeight   int
}

func (w *kyuReplWidget) Reconcile(props any) bool { return true }

func (w *kyuReplWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	if width <= 0 || height <= 0 {
		return
	}
	w.lastHeight = height

	inputLines, cursorLine, cursorCol := w.renderInput()
	visible := append(append([]replLine(nil), w.lines...), inputLines...)
	// scrollOffset counts lines back from the very bottom of visible —
	// see its own doc comment on why this needs no clamping here beyond
	// max0 itself.
	start := max0(len(visible) - height - w.scrollOffset)
	end := min(start+height, len(visible))
	shown := visible[start:end]
	for y, ln := range shown {
		p.Text(0, y, ln.text, ln.style)
	}

	// tui.App's real terminal cursor is unconditionally hidden (see its
	// Run loop's hardcoded renderer.Render(..., false)) — every
	// focusable text-entry widget has to draw its own soft cursor
	// instead, the same technique widget.Terminal already uses against
	// its embedded VT screen's cursor. Only drawn when scrolled all the
	// way to the bottom (scrollOffset 0): scrolled up, the input line
	// isn't even in the visible window, and drawing a cursor over
	// whatever historical line happens to occupy that row would be
	// actively misleading. Shows the real rune under the cursor (not
	// always a blank space, now that the cursor can sit mid-line, not
	// just past the end) so moving the cursor over existing text
	// doesn't visually erase it.
	if w.focused && w.scrollOffset == 0 {
		cy := len(w.lines) + cursorLine - start
		if cy >= 0 && cy < len(shown) && cursorCol < width {
			glyph := ' '
			rs := w.runes()
			if w.cursor < len(rs) && rs[w.cursor] != '\n' {
				glyph = rs[w.cursor]
			}
			p.SetCell(cursorCol, cy, glyph, cursorStyle)
		}
	}
}

// renderInput builds the visible replLines for the in-progress
// (possibly multi-line) input, with the same "9sh> " / "...  " prompt
// convention as cmd/9sh's line REPL, and reports which of those lines
// (0-indexed within just the input portion) and column (promptWidth
// included) the cursor sits at.
func (w *kyuReplWidget) renderInput() (lines []replLine, cursorLine, cursorCol int) {
	rs := w.runes()
	lineStart := 0
	col := 0
	for i := 0; i <= len(rs); i++ {
		if i < len(rs) && rs[i] != '\n' {
			continue
		}
		prefix := "9sh> "
		if len(lines) > 0 {
			prefix = "...  "
		}
		lines = append(lines, replLine{text: prefix + string(rs[lineStart:i]), style: promptStyle})
		if w.cursor >= lineStart && w.cursor <= i {
			cursorLine = len(lines) - 1
			col = w.cursor - lineStart
		}
		lineStart = i + 1
	}
	cursorCol = promptWidth + col
	return
}

func (w *kyuReplWidget) HandleEvent(e input.Event) tui.Cmd {
	switch ev := e.(type) {
	case input.PasteEvent:
		w.insertText(ev.Text)
	case input.MouseEvent:
		switch ev.Button {
		case input.MouseWheelUp:
			w.scrollOffset += scrollStep
		case input.MouseWheelDown:
			w.scrollOffset = max0(w.scrollOffset - scrollStep)
		}
	case input.KeyEvent:
		return w.handleKey(ev)
	}
	return nil
}

// handleKey returns a Cmd only for the copy bindings (see
// tui.CopyToClipboard) — every other case mutates the widget directly
// and returns nil, same as before this needed a return value at all.
func (w *kyuReplWidget) handleKey(ke input.KeyEvent) tui.Cmd {
	ctrl := ke.Mod&input.ModCtrl != 0
	alt := ke.Mod&input.ModAlt != 0
	switch {
	case ke.Key == input.KeyEnter:
		w.submit()
	case ke.Key == input.KeyBackspace:
		w.backspace()
	case ke.Key == input.KeyDelete:
		w.deleteForward()
	case ke.Key == input.KeyLeft && ctrl:
		w.moveWordLeft()
	case ke.Key == input.KeyLeft:
		w.moveLeft()
	case ke.Key == input.KeyRight && ctrl:
		w.moveWordRight()
	case ke.Key == input.KeyRight:
		w.moveRight()
	case ke.Key == input.KeyHome, ctrl && ke.Rune == 'a':
		w.moveHome()
	case ke.Key == input.KeyEnd, ctrl && ke.Rune == 'e':
		w.moveEnd()
	case ke.Key == input.KeyUp:
		w.historyPrev()
	case ke.Key == input.KeyDown:
		w.historyNext()
	case ke.Key == input.KeyPgUp:
		// A page short of the full height, matching the "leave a line
		// or two of context" convention most pagers/terminals use so a
		// reader doesn't lose their place jumping page to page.
		w.scrollOffset += max0(w.lastHeight - 1)
	case ke.Key == input.KeyPgDown:
		w.scrollOffset = max0(w.scrollOffset - max0(w.lastHeight-1))
	case ctrl && ke.Rune == 'u':
		w.killToLineStart()
	case ctrl && ke.Rune == 'k':
		w.killToLineEnd()
	case ctrl && ke.Rune == 'w':
		w.killWordBackward()
	case ctrl && ke.Rune == 'c':
		// Not Ctrl+Shift+C: most terminals (including VTE/gnome-
		// terminal, this project's own standing verification target —
		// see the "Real-terminal testing technique" design-doc section)
		// send the identical byte for Ctrl+C and Ctrl+Shift+C on a
		// plain letter key, only genuinely distinguishable with a
		// kitty-keyboard-protocol-aware terminal (term.Capabilities.
		// KittyKeyboard, not assumable). Alt+C for "just what's on
		// screen" instead avoids that ambiguity entirely — Alt+<letter>
		// decodes unambiguously everywhere (see decodeEscape's ESC-
		// prefix dispatch).
		return tui.CopyToClipboard(w.allText())
	case alt && ke.Rune == 'c':
		return tui.CopyToClipboard(w.visibleText())
	case ke.Rune != 0 && ke.Key == input.KeyNone && !ctrl && !alt:
		w.insertRune(ke.Rune)
	}
	return nil
}

// ---- editing primitives — all operate on w.cursor, a rune index into
// []rune(w.input), not a byte index, so multi-byte runes move/delete
// as one unit like everywhere else in this codebase's text handling ----

func (w *kyuReplWidget) runes() []rune { return []rune(w.input) }

// insertText inserts s at the cursor and advances the cursor past it
// — the shared primitive behind both typing a single rune and pasting
// a whole block.
func (w *kyuReplWidget) insertText(s string) {
	if s == "" {
		return
	}
	rs := w.runes()
	ins := []rune(s)
	merged := make([]rune, 0, len(rs)+len(ins))
	merged = append(merged, rs[:w.cursor]...)
	merged = append(merged, ins...)
	merged = append(merged, rs[w.cursor:]...)
	w.input = string(merged)
	w.cursor += len(ins)
}

func (w *kyuReplWidget) insertRune(r rune) { w.insertText(string(r)) }

func (w *kyuReplWidget) backspace() {
	if w.cursor == 0 {
		return
	}
	rs := w.runes()
	rs = append(rs[:w.cursor-1], rs[w.cursor:]...)
	w.input = string(rs)
	w.cursor--
}

func (w *kyuReplWidget) deleteForward() {
	rs := w.runes()
	if w.cursor >= len(rs) {
		return
	}
	rs = append(rs[:w.cursor], rs[w.cursor+1:]...)
	w.input = string(rs)
}

func (w *kyuReplWidget) moveLeft() {
	if w.cursor > 0 {
		w.cursor--
	}
}

func (w *kyuReplWidget) moveRight() {
	if w.cursor < len(w.runes()) {
		w.cursor++
	}
}

// isWordRune is the boundary isWordRune/moveWordLeft/moveWordRight use
// for "word" — anything but whitespace, the same coarse definition
// most terminal/shell word-movement bindings use (no separate
// punctuation-vs-alnum class).
func isWordRune(r rune) bool { return r != ' ' && r != '\t' && r != '\n' }

// moveWordLeft/moveWordRight are allowed to cross a line boundary
// within a multi-line input (isWordRune treats '\n' as non-word, and
// neither function special-cases it further) — unlike the kill
// commands below, plain cursor movement is non-destructive, so
// stepping into an earlier continuation line is unsurprising, matching
// ordinary editor behavior.
func (w *kyuReplWidget) moveWordLeft() {
	rs := w.runes()
	i := w.cursor
	for i > 0 && !isWordRune(rs[i-1]) {
		i--
	}
	for i > 0 && isWordRune(rs[i-1]) {
		i--
	}
	w.cursor = i
}

func (w *kyuReplWidget) moveWordRight() {
	rs := w.runes()
	i := w.cursor
	for i < len(rs) && !isWordRune(rs[i]) {
		i++
	}
	for i < len(rs) && isWordRune(rs[i]) {
		i++
	}
	w.cursor = i
}

// currentLineBounds returns the rune-index range [start,end] of the
// input line the cursor currently sits on, bounded by the nearest '\n'
// on each side (or the buffer's own start/end) — Home/End and the kill
// commands all stay scoped to this so they can't reach into an
// already-typed earlier continuation line.
func (w *kyuReplWidget) currentLineBounds() (start, end int) {
	rs := w.runes()
	start = w.cursor
	for start > 0 && rs[start-1] != '\n' {
		start--
	}
	end = w.cursor
	for end < len(rs) && rs[end] != '\n' {
		end++
	}
	return
}

func (w *kyuReplWidget) moveHome() {
	start, _ := w.currentLineBounds()
	w.cursor = start
}

func (w *kyuReplWidget) moveEnd() {
	_, end := w.currentLineBounds()
	w.cursor = end
}

func (w *kyuReplWidget) killToLineStart() {
	start, _ := w.currentLineBounds()
	rs := w.runes()
	rs = append(rs[:start], rs[w.cursor:]...)
	w.input = string(rs)
	w.cursor = start
}

func (w *kyuReplWidget) killToLineEnd() {
	_, end := w.currentLineBounds()
	rs := w.runes()
	rs = append(rs[:w.cursor], rs[end:]...)
	w.input = string(rs)
}

func (w *kyuReplWidget) killWordBackward() {
	lineStart, _ := w.currentLineBounds()
	rs := w.runes()
	i := w.cursor
	for i > lineStart && !isWordRune(rs[i-1]) {
		i--
	}
	for i > lineStart && isWordRune(rs[i-1]) {
		i--
	}
	rs = append(rs[:i], rs[w.cursor:]...)
	w.input = string(rs)
	w.cursor = i
}

// ---- copy (Ctrl+C / Alt+C — see handleKey) ----

// allText is the entire transcript (every evaluated line, plus the
// in-progress input if any) joined with '\n', regardless of scroll
// position — "copy all of the content in a pane".
func (w *kyuReplWidget) allText() string {
	inputLines, _, _ := w.renderInput()
	all := append(append([]replLine(nil), w.lines...), inputLines...)
	return joinLineText(all)
}

// visibleText is only the lines currently in the scrolled viewport —
// "copy all of the visible text in a pane". Recomputes the same
// window Paint's own start/end does rather than sharing state with it,
// since this only runs on an explicit copy keypress (not a hot path)
// and keeping it independent means a future change to Paint's own
// windowing can't silently change what a screenshot-style copy grabs
// without that being a deliberate, visible diff here too.
func (w *kyuReplWidget) visibleText() string {
	inputLines, _, _ := w.renderInput()
	all := append(append([]replLine(nil), w.lines...), inputLines...)
	height := w.lastHeight
	if height <= 0 {
		height = len(all)
	}
	start := max0(len(all) - height - w.scrollOffset)
	end := min(start+height, len(all))
	return joinLineText(all[start:end])
}

func joinLineText(lines []replLine) string {
	texts := make([]string, len(lines))
	for i, ln := range lines {
		texts[i] = ln.text
	}
	return strings.Join(texts, "\n")
}

// ---- history ----

// historyPrev/historyNext are no-ops while input contains a '\n':
// mid multi-line continuation, Up/Down recalling a different
// submission entirely would be more confusing than useful, and this
// pane doesn't attempt real multi-line-aware history (recalling one
// line at a time within a continuation) — a deliberate, documented
// scope cut, not an oversight.
func (w *kyuReplWidget) historyPrev() {
	if strings.Contains(w.input, "\n") || len(w.history) == 0 {
		return
	}
	if w.historyIndex == len(w.history) {
		w.historyDraft = w.input
	}
	if w.historyIndex > 0 {
		w.historyIndex--
	}
	w.input = w.history[w.historyIndex]
	w.cursor = len(w.runes())
}

func (w *kyuReplWidget) historyNext() {
	if strings.Contains(w.input, "\n") || w.historyIndex >= len(w.history) {
		return
	}
	w.historyIndex++
	if w.historyIndex == len(w.history) {
		w.input = w.historyDraft
	} else {
		w.input = w.history[w.historyIndex]
	}
	w.cursor = len(w.runes())
}

// submit inserts a newline at the cursor and keeps editing if the
// resulting input isn't balanced yet (parser.BracketDepth), or
// evaluates and clears it otherwise. Inserting at the cursor rather
// than always appending at the end matters now that the cursor can sit
// mid-line (e.g. Home, then Enter, to open a new line above what's
// already there) — append-only was fine when this pane had no cursor
// movement at all.
func (w *kyuReplWidget) submit() {
	rs := w.runes()
	withNewline := make([]rune, 0, len(rs)+1)
	withNewline = append(withNewline, rs[:w.cursor]...)
	withNewline = append(withNewline, '\n')
	withNewline = append(withNewline, rs[w.cursor:]...)
	candidate := string(withNewline)
	if parser.BracketDepth(candidate) > 0 {
		w.input = candidate
		w.cursor++
		return
	}
	lines, _, _ := w.renderInput()
	w.lines = append(w.lines, lines...)
	src := w.input
	w.evaluate(src)
	if trimmedNonEmpty(src) {
		w.history = append(w.history, src)
	}
	w.historyIndex = len(w.history)
	w.historyDraft = ""
	w.input = ""
	w.cursor = 0
	// A submit is a deliberate user action, unlike output arriving
	// passively while reviewing history — snapping back to the latest
	// output here is what every real shell does, and unlike a plain
	// scroll-follow-if-already-at-bottom rule (which this pane also
	// has, for free, by never touching scrollOffset on its own — see
	// scrollOffset's own doc comment) this needs to be unconditional:
	// you just typed something, you want to see what happened.
	w.scrollOffset = 0
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
