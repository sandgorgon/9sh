package pane

import (
	"context"
	"sort"
	"strings"

	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/lexer"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/token"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/pathresolve"
)

var (
	promptStyle = cell.Style{Fg: cell.ANSIColor(6)}
	resultStyle = cell.Style{}
	errorStyle  = cell.Style{Fg: cell.ANSIColor(1)}

	// Live-input syntax-highlighting palette — see highlightSpans. Kept
	// distinct from promptStyle/errorStyle/cursorStyle's own colors
	// (cyan/red/yellow) so a token's color always means the same thing
	// regardless of where else that color shows up in the pane.
	keywordStyle = cell.Style{Fg: cell.ANSIColor(5)} // magenta
	stringStyle  = cell.Style{Fg: cell.ANSIColor(2)} // green
	numberStyle  = cell.Style{Fg: cell.ANSIColor(3)} // yellow (foreground here, unlike cursorStyle's yellow background — visually distinct)
	pathStyle    = cell.Style{Fg: cell.ANSIColor(4)} // blue
	// sigilStyle is bold cyan, not plain cyan (promptStyle) — %/$/@ are
	// kyu's own "this is special" markers, worth standing out even from
	// the prompt's already-cyan "9sh> ".
	sigilStyle = cell.Style{Fg: cell.ANSIColor(6), Attr: cell.AttrBold}

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
	// spans, when non-nil, overrides text/style for rendering (see
	// Paint): per-token styling for a live (not-yet-submitted) input
	// line — see highlightSpans. nil for every transcript line
	// (results/errors already evaluated), which render exactly as
	// before this existed; concatenating a spans slice's own text
	// fields always reconstructs the line's plain text exactly, so
	// text/style stay meaningful fallbacks even on a spans line.
	spans []replSpan
}

type replSpan struct {
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

	// searchMode is Ctrl-R (reverse history search, bash's
	// reverse-i-search). While true, handleKey routes almost every key
	// to search-specific handling instead of the normal editing cases —
	// see handleSearchKey. searchIndex is the index into history the
	// current searchQuery matched (len(history) = "no match yet, at the
	// very end"); searchQuery is re-searched from the end of history on
	// every change (not incrementally from searchIndex), matching
	// bash's own behavior, so backspacing the query can find a more
	// recent match again rather than only ever searching further back.
	searchMode  bool
	searchQuery string
	searchIndex int

	// completionCandidates/completionFragmentStart/completionCycle
	// track an in-progress Tab-completion cycle (see completeTab): a
	// second, immediately-repeated Tab with the same fragment cycles
	// through the same candidate list instead of recomputing it, the
	// same way bash cycles through matches on repeated Tab. Any other
	// key clears completionCandidates, so a later, unrelated Tab always
	// starts a fresh completion rather than continuing a stale cycle.
	completionCandidates    []string
	completionFragmentStart int
	completionCycle         int
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
		if ln.spans != nil {
			x := 0
			for _, sp := range ln.spans {
				x += p.Text(x, y, sp.text, sp.style)
			}
			continue
		}
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

// ---- live syntax highlighting (renderInput's input lines only — see
// replLine.spans' own doc comment on why transcript lines are untouched) ----

// tokenStyle maps a lexed token.Kind to its highlight color. Anything
// not listed (identifiers, operators, punctuation) renders unstyled,
// same as resultStyle's existing default.
func tokenStyle(k token.Kind) (cell.Style, bool) {
	switch k {
	case token.IF, token.ELSE, token.WHILE, token.BREAK, token.CONTINUE,
		token.BIND, token.UNBIND, token.TRUE, token.FALSE, token.NULL:
		return keywordStyle, true
	case token.STRING:
		return stringStyle, true
	case token.INT, token.FLOAT, token.DURATION:
		return numberStyle, true
	case token.PATH:
		return pathStyle, true
	case token.PERCENT, token.DOLLAR, token.AT:
		return sigilStyle, true
	}
	return cell.Style{}, false
}

// stringRawLen reports how many runes of *raw* source (both quotes,
// plus any escape sequences like \n/\"/\\ in their two-rune raw form,
// not their decoded one) a STRING token starting at line[start] (which
// must be the opening '"') actually spans. token.Token.Literal for a
// STRING is already-*decoded* content — kyu/lexer's lexString unescapes
// as it scans (see its own doc comment) — so unlike every other token
// kind, its rune count alone can't be used to find where the raw token
// ends; this mirrors lexString's own escape-aware walk, purely for
// length.
func stringRawLen(line []rune, start int) int {
	if start >= len(line) || line[start] != '"' {
		return 1 // defensive: shouldn't happen for a real STRING token
	}
	i := start + 1
	for i < len(line) && line[i] != '"' {
		if line[i] == '\\' && i+1 < len(line) {
			i += 2
			continue
		}
		i++
	}
	if i < len(line) {
		i++ // closing quote
	}
	return i - start
}

// highlightSpans lexes the full (possibly multi-line) input text once
// and returns each line's highlighted spans, keyed by 1-indexed line
// number (matching token.Token.Line — kyu/lexer.New starts both line
// and col at 1). Every line's spans, concatenated, reconstruct that
// line's own raw text exactly: a gap between consecutive tokens (plain
// whitespace, or a discarded "# comment" — kyu/lexer's
// skipSpaceAndComments treats both the same, never emitting a token for
// either) becomes its own unstyled span, so this degrades gracefully
// through a lex error too — an ILLEGAL token still carries a real
// Line/Col/Literal, it just doesn't match any case in tokenStyle.
func highlightSpans(input string) map[int][]replSpan {
	lines := strings.Split(input, "\n")
	lineRunes := make([][]rune, len(lines))
	for i, l := range lines {
		lineRunes[i] = []rune(l)
	}
	cursors := make([]int, len(lines)) // rune position reached so far, per line

	out := map[int][]replSpan{}
	l := lexer.New(input)
	for {
		tok := l.Next()
		if tok.Kind == token.EOF {
			break
		}
		if tok.Kind == token.NEWLINE {
			continue
		}
		lineIdx := tok.Line - 1
		if lineIdx < 0 || lineIdx >= len(lineRunes) {
			continue // defensive: shouldn't happen given New's 1-indexing
		}
		rs := lineRunes[lineIdx]
		start := tok.Col - 1
		if start < 0 || start > len(rs) {
			continue // defensive
		}
		if start > cursors[lineIdx] {
			out[tok.Line] = append(out[tok.Line], replSpan{text: string(rs[cursors[lineIdx]:start]), style: resultStyle})
		}
		length := len([]rune(tok.Literal))
		if tok.Kind == token.STRING {
			length = stringRawLen(rs, start)
		}
		end := min(start+length, len(rs))
		style, ok := tokenStyle(tok.Kind)
		if !ok {
			style = resultStyle
		}
		out[tok.Line] = append(out[tok.Line], replSpan{text: string(rs[start:end]), style: style})
		cursors[lineIdx] = end
	}
	for i, rs := range lineRunes {
		if cursors[i] < len(rs) {
			out[i+1] = append(out[i+1], replSpan{text: string(rs[cursors[i]:]), style: resultStyle})
		}
	}
	return out
}

// renderInput builds the visible replLines for the in-progress
// (possibly multi-line) input, with the same "9sh> " / "...  " prompt
// convention as cmd/9sh's line REPL, and reports which of those lines
// (0-indexed within just the input portion) and column (promptWidth
// included) the cursor sits at. In search mode (see searchStep) it
// instead renders bash's familiar single-line "(search)`query`: match"
// prompt — search doesn't support multi-line input, so there's no
// continuation-prompt case to handle here.
func (w *kyuReplWidget) renderInput() (lines []replLine, cursorLine, cursorCol int) {
	if w.searchMode {
		matched := ""
		if w.searchIndex < len(w.history) {
			matched = w.history[w.searchIndex]
		}
		text := "(search)`" + w.searchQuery + "`: " + matched
		return []replLine{{text: text, style: promptStyle}}, 0, len([]rune(text))
	}

	highlighted := highlightSpans(w.input)
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
		lineText := string(rs[lineStart:i])
		lineNum := len(lines) + 1 // 1-indexed, matches token.Token.Line
		var spans []replSpan
		if hl := highlighted[lineNum]; len(hl) > 0 {
			spans = append(spans, replSpan{text: prefix, style: promptStyle})
			spans = append(spans, hl...)
		}
		lines = append(lines, replLine{text: prefix + lineText, style: promptStyle, spans: spans})
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
// Ctrl-R is checked before everything else, whether or not search mode
// is already active (it means "start searching" the first time,
// "search further back" on every press after) — see searchStep. Once
// in search mode, every other key routes to handleSearchKey instead of
// the normal editing switch below; bash's own reverse-i-search doesn't
// support arbitrary mid-search editing either, so this doesn't try to.
func (w *kyuReplWidget) handleKey(ke input.KeyEvent) tui.Cmd {
	ctrl := ke.Mod&input.ModCtrl != 0
	alt := ke.Mod&input.ModAlt != 0

	if ctrl && ke.Rune == 'r' {
		w.searchStep()
		return nil
	}
	if w.searchMode {
		return w.handleSearchKey(ke)
	}

	// A Tab-completion cycle (see completeTab) only continues on an
	// *immediately repeated* Tab — any other key means the user moved
	// on, so a later, unrelated Tab must start a fresh completion
	// rather than resuming a stale one.
	if ke.Key != input.KeyTab {
		w.completionCandidates = nil
	}

	switch {
	case ke.Key == input.KeyTab && !ctrl && !alt:
		w.completeTab()
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

// ---- reverse history search (Ctrl-R) ----

// searchStep is Ctrl-R: enters reverse history search if not already
// in it (searchIndex starts at len(history), "no match yet"), or
// advances to an older match with the same query if already searching
// — bash's "press Ctrl-R again to go further back".
func (w *kyuReplWidget) searchStep() {
	if !w.searchMode {
		w.searchMode = true
		w.searchQuery = ""
		w.searchIndex = len(w.history)
		return
	}
	w.searchFrom(w.searchIndex - 1)
}

// searchFrom scans history backward from start (inclusive) for the
// first entry containing searchQuery, updating searchIndex on a match.
// No match leaves searchIndex wherever it already was, so renderInput
// simply keeps showing the last successful match — the simplest
// correct behavior, rather than a separate "no match" display state.
func (w *kyuReplWidget) searchFrom(start int) {
	if w.searchQuery == "" {
		return
	}
	for i := start; i >= 0; i-- {
		if strings.Contains(w.history[i], w.searchQuery) {
			w.searchIndex = i
			return
		}
	}
}

// handleSearchKey handles every key while searchMode is active —
// called from handleKey once Ctrl-R itself (handled unconditionally
// there, whether or not search is already active) has been ruled out.
func (w *kyuReplWidget) handleSearchKey(ke input.KeyEvent) tui.Cmd {
	switch {
	case ke.Key == input.KeyEnter:
		w.exitSearch(true)
	case ke.Key == input.KeyEsc:
		w.exitSearch(false)
	case ke.Key == input.KeyBackspace:
		if rs := []rune(w.searchQuery); len(rs) > 0 {
			w.searchQuery = string(rs[:len(rs)-1])
			w.searchFrom(len(w.history) - 1)
		}
	case ke.Rune != 0 && ke.Key == input.KeyNone && ke.Mod == 0:
		w.searchQuery += string(ke.Rune)
		w.searchFrom(len(w.history) - 1)
	}
	return nil
}

// exitSearch leaves search mode with whatever's currently matched
// loaded as the input (if there was ever a match — otherwise w.input
// is simply whatever it already was before search started, since
// searchStep never touches it). doSubmit (Enter) additionally runs it
// through the real submit()/evaluate() path, matching bash's
// reverse-i-search, where Enter runs the found command immediately;
// Esc (doSubmit false) just leaves it in the input line for further
// editing, cursor at the end, the same placement historyPrev already
// uses.
func (w *kyuReplWidget) exitSearch(doSubmit bool) {
	w.searchMode = false
	if w.searchIndex < len(w.history) {
		w.input = w.history[w.searchIndex]
		w.cursor = len(w.runes())
	}
	w.searchQuery = ""
	if doSubmit {
		w.submit()
	}
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
		w.lines = append(w.lines, resultLines(v)...)
	}
}

// resultLines renders a top-level evaluation result as one or more
// replLines. Bytes is special-cased the same way cmd/9sh's line REPL
// special-cases it (see that package's printResult): value.Bytes.String()
// deliberately stays a "<N bytes>" summary everywhere else, but a bare
// %cmd at the REPL is exactly the case where real output is wanted, not a
// summary. Split on newlines rather than handing multi-line text to a
// single replLine — each replLine is one visual row (see scrollOffset/
// cursor-line math above), so an embedded "\n" would render wrong.
func resultLines(v value.Value) []replLine {
	b, ok := v.(value.Bytes)
	if !ok {
		return []replLine{{text: v.String(), style: resultStyle}}
	}
	text := strings.TrimSuffix(string(b), "\n")
	if text == "" {
		return nil
	}
	rows := strings.Split(text, "\n")
	lines := make([]replLine, len(rows))
	for i, row := range rows {
		lines[i] = replLine{text: row, style: resultStyle}
	}
	return lines
}

// ---- tab completion ----

// kyuKeywords is every kyu keyword — kept in sync by hand with
// kyu/token/token.go's own (unexported) keywords map, since a list
// this short and this rarely changing doesn't justify a new exported
// API just for tab completion to reach it.
var kyuKeywords = []string{
	"if", "else", "while", "break", "continue", "bind", "unbind",
	"true", "false", "null",
}

// isIdentRune is the narrower word-boundary tab completion needs:
// isWordRune (cursor movement/kill commands) treats any non-whitespace
// character, punctuation included, as part of a "word" — wrong for
// extracting the identifier fragment being typed, which should stop
// right at a '(' or '.' immediately before it. Matches kyu/lexer's own
// identifier-char rules.
func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// currentIdentBounds returns the rune index the identifier fragment
// immediately before the cursor starts at — equal to w.cursor itself
// if the cursor isn't right after an identifier character at all (e.g.
// right after a space or '('), meaning "complete from scratch," every
// candidate a match.
func (w *kyuReplWidget) currentIdentBounds() (start int) {
	rs := w.runes()
	start = w.cursor
	for start > 0 && isIdentRune(rs[start-1]) {
		start--
	}
	return start
}

// currentExternalNameBounds reports whether the cursor sits inside an
// external-command-name fragment — right after a '%' or '$' sigil,
// kyu's external-call syntax (see kyu/lexer's lexExternalName) — and if
// so, the rune index it starts at. Unlike an ordinary kyu identifier,
// this fragment allows internal hyphens (docker-compose, apt-get, ...),
// matching lexExternalName's own character set; that's the only reason
// this isn't just currentIdentBounds with an extra check.
func (w *kyuReplWidget) currentExternalNameBounds() (start int, ok bool) {
	rs := w.runes()
	start = w.cursor
	for start > 0 && (isIdentRune(rs[start-1]) || rs[start-1] == '-') {
		start--
	}
	if start == 0 {
		return 0, false
	}
	sigil := rs[start-1]
	return start, sigil == '%' || sigil == '$'
}

// externalNameCandidates lists every PATH executable whose name starts
// with fragment, resolving PATH the same way a %cmd/$cmd actually would
// (env.EnvSlice's /env-backed view, not this process's own real PATH —
// see pathresolve's doc comment) so completion never offers a name the
// command wouldn't actually resolve to.
func (w *kyuReplWidget) externalNameCandidates(fragment string) []string {
	envVars, err := w.env.EnvSlice(context.Background())
	if err != nil {
		return nil
	}
	var candidates []string
	for _, n := range pathresolve.Names(envVars) {
		if strings.HasPrefix(n, fragment) {
			candidates = append(candidates, n)
		}
	}
	return candidates
}

// completeTab is Tab: fills in the longest common prefix of every
// candidate matching the fragment before the cursor, or — on an
// *immediately repeated* Tab with the same fragment start (handleKey
// clears completionCandidates on any other key) — cycles to the next
// candidate in that same list instead, wrapping around. Matches classic
// bash/zsh completion: no dropdown/menu UI, just in-place text
// replacement.
//
// Right after a '%'/'$' sigil the candidates are PATH executables (see
// currentExternalNameBounds/externalNameCandidates); everywhere else
// they're env.Names() — every user variable and builtin, see its own
// doc comment — plus kyuKeywords.
func (w *kyuReplWidget) completeTab() {
	externalStart, isExternal := w.currentExternalNameBounds()
	start := externalStart
	if !isExternal {
		start = w.currentIdentBounds()
	}

	if w.completionCandidates != nil && w.completionFragmentStart == start {
		w.completionCycle = (w.completionCycle + 1) % len(w.completionCandidates)
		w.replaceRange(start, w.cursor, w.completionCandidates[w.completionCycle])
		return
	}

	fragment := string(w.runes()[start:w.cursor])
	var candidates []string
	if isExternal {
		candidates = w.externalNameCandidates(fragment)
	} else {
		for _, n := range w.env.Names() {
			if strings.HasPrefix(n, fragment) {
				candidates = append(candidates, n)
			}
		}
		for _, kw := range kyuKeywords {
			if strings.HasPrefix(kw, fragment) {
				candidates = append(candidates, kw)
			}
		}
	}
	if len(candidates) == 0 {
		return
	}
	sort.Strings(candidates)

	w.completionCandidates = candidates
	w.completionFragmentStart = start
	w.completionCycle = 0
	w.replaceRange(start, w.cursor, commonPrefix(candidates))
}

// replaceRange replaces runes [start, end) with replacement, leaving
// the cursor right after the inserted text — the same cursor-advance
// behavior insertText already gives ordinary typing.
func (w *kyuReplWidget) replaceRange(start, end int, replacement string) {
	rs := w.runes()
	merged := make([]rune, 0, len(rs)-(end-start)+len(replacement))
	merged = append(merged, rs[:start]...)
	merged = append(merged, []rune(replacement)...)
	merged = append(merged, rs[end:]...)
	w.input = string(merged)
	w.cursor = start + len([]rune(replacement))
}

// commonPrefix returns the longest string every element of ss starts
// with. ss must be non-empty; the result is ss[0] itself when there's
// only one candidate (a unique match completes fully on the first Tab).
func commonPrefix(ss []string) string {
	prefix := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
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

// WantsRawTab/ReleaseKey implement tui.RawKeyClaimer. Without this,
// tui.App.HandleInput intercepts Tab globally for pane-focus
// navigation before it's ever forwarded to HandleEvent at all (see its
// own doc comment) — completeTab would be silently unreachable dead
// code in the real app despite working correctly in every direct-call
// unit test, since those bypass App.HandleInput entirely. Ctrl+\,
// matching widget.Terminal's own default release key (see its
// TerminalOptions.ReleaseKey), so shell panes and kyu-repl panes share
// one "how do I get my keyboard focus back" muscle memory — not Esc,
// which this widget already uses for a different purpose (exiting
// reverse history search, see exitSearch).
func (w *kyuReplWidget) WantsRawTab() bool { return true }
func (w *kyuReplWidget) ReleaseKey() input.KeyEvent {
	return input.KeyEvent{Rune: '\\', Mod: input.ModCtrl}
}
