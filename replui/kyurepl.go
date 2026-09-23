package replui

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/term"
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
	// regardless of where else that color shows up on screen.
	keywordStyle = cell.Style{Fg: cell.ANSIColor(5)} // magenta
	stringStyle  = cell.Style{Fg: cell.ANSIColor(2)} // green
	numberStyle  = cell.Style{Fg: cell.ANSIColor(3)} // yellow (foreground here, unlike cursorStyle's yellow background — visually distinct)
	pathStyle    = cell.Style{Fg: cell.ANSIColor(4)} // blue
	// sigilStyle is bold cyan, not plain cyan (promptStyle) — %/@ are
	// kyu's own "this is special" markers, worth standing out even from
	// the prompt's already-cyan "9sh> ".
	sigilStyle = cell.Style{Fg: cell.ANSIColor(6), Attr: cell.AttrBold}

	// cursorStyle is an explicit, theme-independent block color (ANSI
	// yellow bg, black fg) rather than bare AttrReverse against the
	// terminal's own default colors — reverse-video against two *unset*
	// colors doesn't reliably read as a visible block on every
	// terminal's own default palette. Yellow reads clearly against both
	// light and dark backgrounds, which this screen can't assume just
	// one of (see Model.theme's own doc comment on autodetection).
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

// kyuReplNode is 9sh's native (not pty-hosted) kyu REPL: input is
// evaluated directly against w.env — the same *eval.Env every other 9sh
// entry point shares (see cmd/9sh's bootstrap) — rather than shelling
// out to another 9sh process. w is Model's own long-lived instance
// (Model.replWidget), not built fresh here: tui's reconciler only
// retains a Component's Widget "for as long as the Node keeps matching
// the same tree slot" (Widget's own doc comment, tui/node.go), and this
// Node's tree slot goes missing entirely, for as many frames as it
// takes, every time a fullscreen %cmd (vim, 9ed, ...) takes over the
// whole screen (see Model.View swapping to widget.Terminal under
// "fullscreen-term" instead, and back). newWidget handing back the
// *same* pointer every time, rather than minting a fresh
// &kyuReplWidget{}, is what makes the transcript and input history
// survive that round trip regardless of what the reconciler's own
// retained-tree bookkeeping does to the wrapper Node in between — a
// real, previously-shipped bug: returning from a fullscreen program
// used to reset the whole kyu-repl screen (empty transcript, empty
// history) as if 9sh had just started. A fixed key is enough (unlike
// the pane multiplexer this package replaced, which needed one key per
// pane — see 9mux's own history): Model.View only ever mounts one of
// these at a time.
func kyuReplNode(w *kyuReplWidget) tui.Node {
	return tui.Component("kyu-repl", struct{}{}, func() tui.Widget {
		return w
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

// kyuReplWidget is a native line editor + transcript for kyu: cursor
// movement (char- and word-wise), Home/End, history recall (Up/Down,
// only when not mid multi-line continuation — see historyPrev/
// historyNext), kill-to-line-start/kill-to-line-end/kill-word-backward
// (Ctrl+U/Ctrl+K/Ctrl+W), bracketed paste, and scrolling the transcript
// independently of the input (scrollOffset).
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

	// busy is true from the moment submit() hands a non-blank source off
	// to a background evaluate() call until its result comes back
	// through TakePendingMsg/applyEvalResult (see submit's and
	// TakePendingMsg's own doc comments). handleKey checks this before
	// anything else: while busy, every key except Ctrl+C (which calls
	// Env.InterruptHandler instead of its usual clipboard-copy binding)
	// is a no-op. That's what makes a second concurrent evaluate() call
	// impossible -- Env's fullscreen/output-sink/history hooks are
	// process-wide, single-slot fields (see evaluate's own doc comment)
	// that assume evaluate() calls never overlap; busy is the thing that
	// now actually guarantees that, since evaluate() no longer runs on
	// the same goroutine as key handling.
	busy bool

	// mu guards pendingEval, the one piece of state submit()'s
	// background goroutine and TakePendingMsg (called from App's own
	// goroutine) both touch -- see TakePendingMsg's own doc comment for
	// why a plain field can't be used here the way w.lines etc. are
	// (those are only ever touched by whichever single goroutine
	// currently owns w; pendingEval is the sole exception, mirroring
	// widget.Terminal's own exited/exitErr/mu).
	mu          sync.Mutex
	pendingEval *evalResult

	// pendingFullscreen is set by applyEvalResult, from evalResult.
	// fullscreen, when the source just evaluated turned out to be a
	// fullscreen %cmd (see kyu/eval's runExternalFullscreen and
	// evaluate's own doc comment for how that reaches evalResult without
	// evaluate touching w directly). applyEvalResult itself consumes it
	// right after, turning it into a startFullscreenMsg Cmd -- see
	// consumeFullscreenCmd.
	pendingFullscreen *fullscreenAttach

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
		token.BIND, token.UNBIND, token.IN_NS, token.TRUE, token.FALSE, token.NULL:
		return keywordStyle, true
	case token.STRING:
		return stringStyle, true
	case token.INT, token.FLOAT, token.DURATION:
		return numberStyle, true
	case token.PATH:
		return pathStyle, true
	case token.PERCENT, token.AT:
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
func highlightSpans(input string, isNative func(string) bool) map[int][]replSpan {
	lines := strings.Split(input, "\n")
	lineRunes := make([][]rune, len(lines))
	for i, l := range lines {
		lineRunes[i] = []rune(l)
	}
	cursors := make([]int, len(lines)) // rune position reached so far, per line

	out := map[int][]replSpan{}
	l := lexer.New(input)
	l.NativeProgramLookup = isNative
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

	highlighted := highlightSpans(w.input, w.nativeProgramLookup())
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

// handleKey returns a Cmd for the copy bindings (tui.CopyToClipboard),
// `?` at an empty prompt (help), and Ctrl+D at an empty prompt (quit) — every other case
// mutates the widget directly and returns nil. Ctrl-R is checked before
// everything else, whether or not search mode is already active (it
// means "start searching" the first time, "search further back" on
// every press after) — see searchStep. Once in search mode, every
// other key routes to handleSearchKey instead of the normal editing
// switch below; bash's own reverse-i-search doesn't support arbitrary
// mid-search editing either, so this doesn't try to.
// isHelpQuestionMark reports whether ke is a bare `?` typed at a truly
// empty prompt — the only place it's safe to claim: no kyu statement can
// start with `?` (it's the postfix error-check operator, `f()?`), and
// multi-line continuation lives inside w.input, so an empty input is
// never mid-statement. Anywhere else `?` is ordinary text.
func (w *kyuReplWidget) isHelpQuestionMark(ke input.KeyEvent) bool {
	return ke.Rune == '?' && ke.Key == input.KeyNone &&
		ke.Mod&(input.ModCtrl|input.ModAlt) == 0 && w.input == ""
}

func (w *kyuReplWidget) handleKey(ke input.KeyEvent) tui.Cmd {
	ctrl := ke.Mod&input.ModCtrl != 0
	alt := ke.Mod&input.ModAlt != 0

	// While a background evaluate() is in flight, every key except
	// Ctrl+C is a no-op -- see busy's own doc comment for why this also
	// has to hold for Ctrl-R/search, not just ordinary editing. Ctrl+C
	// here reaches Env.InterruptHandler and Env.CancelFunc directly (a
	// decoded keystroke, not a real SIGINT: raw mode means the kernel
	// never raises one for Ctrl+C while this widget owns the terminal),
	// the same two interrupt paths `-repl`'s real SIGINT already uses.
	// Both fire unconditionally: InterruptHandler reaches a blocked
	// foreground %cmd (see Env.SetInterruptHandler's doc comment),
	// CancelFunc reaches a spinning pure-kyu loop or unbounded recursion
	// (see Env.SetCancelContext's doc comment and submit()'s own,
	// above) -- calling whichever one is nil, or already fired, is a
	// harmless no-op, so both are always tried rather than picking one.
	if w.busy {
		if ctrl && ke.Rune == 'c' && w.env != nil {
			if fn := w.env.InterruptHandler(); fn != nil {
				fn()
			}
			if fn := w.env.CancelFunc(); fn != nil {
				fn()
			}
		}
		return nil
	}

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
	case w.isHelpQuestionMark(ke):
		// The multiplexer this package replaced toggled help from a
		// control-strip button; there's no control strip here, so this
		// is its keybinding instead. Deliberately not a function key:
		// terminal emulators keep those for themselves (xfce4-terminal
		// binds F1 to its own Help menu and never forwards it), so a
		// plain character is the only binding that works everywhere. It
		// mirrors the help screen's own close keys, which already
		// include `?`.
		return func() tui.Msg { return toggleHelpMsg{} }
	case ctrl && ke.Rune == 'd' && w.input == "":
		// bash/zsh's own "Ctrl-D at an empty prompt exits the shell" —
		// the TUI's equivalent of EOF on repl()'s bufio.Scanner loop,
		// which has no raw-mode keyboard of its own to bind this to.
		// Only at an empty prompt: mid-input, Ctrl+D should behave like
		// ordinary text-entry muscle memory expects (a no-op here, since
		// this widget has no forward-delete-at-cursor binding on Ctrl+D
		// to begin with), not silently discard what's been typed.
		return tui.Quit()
	case ke.Key == input.KeyTab && !ctrl && !alt:
		w.completeTab()
	case ke.Key == input.KeyEnter:
		return w.submit()
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
	case ctrl && ke.Rune == 'l':
		w.clearTranscript()
	case ctrl && ke.Rune == 'c':
		// Only reached while idle (w.busy's case above returns first
		// otherwise, calling InterruptHandler instead) -- the
		// conventional terminal split: Ctrl+C interrupts a running
		// foreground job, and is free for something else when there
		// isn't one. Not Ctrl+Shift+C: most terminals (including VTE/
		// gnome-terminal, this project's own standing verification
		// target — see the "Real-terminal testing technique" design-doc
		// section) send the identical byte for Ctrl+C and Ctrl+Shift+C
		// on a plain letter key, only genuinely distinguishable with a
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

// clearTranscript implements Ctrl+L: bash/zsh/readline convention for
// "clear the screen." Unlike a real terminal's clear (which just moves
// the scroll region — history is still one scroll-back away), this
// widget owns its own transcript buffer directly (see w.lines' doc
// comment), so clearing means actually discarding it, not just
// repainting over it — there's no separate terminal scrollback to fall
// back to. history (Up/Down, Ctrl-R) is untouched; only the visible
// transcript goes.
func (w *kyuReplWidget) clearTranscript() {
	w.lines = nil
	w.scrollOffset = 0
}

// ---- copy (Ctrl+C / Alt+C — see handleKey) ----

// allText is the entire transcript (every evaluated line, plus the
// in-progress input if any) joined with '\n', regardless of scroll
// position — "copy all of the content on screen".
func (w *kyuReplWidget) allText() string {
	inputLines, _, _ := w.renderInput()
	all := append(append([]replLine(nil), w.lines...), inputLines...)
	return joinLineText(all)
}

// visibleText is only the lines currently in the scrolled viewport —
// "copy all of the visible text on screen". Recomputes the same
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
// widget doesn't attempt real multi-line-aware history (recalling one
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

// historyUnique reports whether history_mode is set to "unique" (see
// docs.go's own entry for it) — "all" (the default: history_mode unset,
// or set to anything else) keeps every submission, duplicates included,
// unchanged from this widget's original behavior.
func (w *kyuReplWidget) historyUnique() bool {
	if w.env == nil {
		return false
	}
	v, ok := w.env.Get("history_mode")
	if !ok {
		return false
	}
	s, ok := v.(value.String)
	return ok && string(s) == "unique"
}

// removeHistoryOccurrences drops every existing entry equal to src —
// history_mode == "unique"'s own primitive (see submit()), an in-place
// filter safe because it only ever writes at an index <= the one it's
// currently reading (standard Go "filter without allocating" idiom).
func (w *kyuReplWidget) removeHistoryOccurrences(src string) {
	out := w.history[:0]
	for _, h := range w.history {
		if h != src {
			out = append(out, h)
		}
	}
	w.history = out
}

// historySnapshot is history()'s Env.HistoryAccess.List: a copy of
// w.history, not the live slice, so a caller holding onto the returned
// value can't alias (and later corrupt, via append's in-place reuse)
// this widget's own backing array.
func (w *kyuReplWidget) historySnapshot() []string {
	out := make([]string, len(w.history))
	copy(out, w.history)
	return out
}

// deleteHistoryEntry is history_delete(index)'s Env.HistoryAccess.Delete.
// historyIndex is clamped afterward the same way it already is
// elsewhere (see historyNext) so a delete can never leave it pointing
// past the end; it doesn't attempt to keep an in-progress Up/Down
// browse position aligned with entries that shifted beneath it — an
// accepted, narrow edge case (deleting history while also mid-recall of
// it), not a correctness issue (nothing can go out of bounds).
func (w *kyuReplWidget) deleteHistoryEntry(index int) bool {
	if index < 0 || index >= len(w.history) {
		return false
	}
	w.history = append(w.history[:index], w.history[index+1:]...)
	if w.historyIndex > len(w.history) {
		w.historyIndex = len(w.history)
	}
	return true
}

// clearHistoryEntries is history_clear()'s Env.HistoryAccess.Clear.
func (w *kyuReplWidget) clearHistoryEntries() {
	w.history = nil
	w.historyIndex = 0
	w.historyDraft = ""
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
		return w.exitSearch(true)
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
// through the real submit() path (see submit's own doc comment),
// matching bash's reverse-i-search, where Enter runs the found command
// immediately -- its returned Cmd is passed through so a fullscreen
// attach from that submission still reaches Model the same way a plain
// Enter's does. Esc (doSubmit false) just leaves it in the input line
// for further editing, cursor at the end, the same placement
// historyPrev already uses.
func (w *kyuReplWidget) exitSearch(doSubmit bool) tui.Cmd {
	w.searchMode = false
	if w.searchIndex < len(w.history) {
		w.input = w.history[w.searchIndex]
		w.cursor = len(w.runes())
	}
	w.searchQuery = ""
	if doSubmit {
		return w.submit()
	}
	return nil
}

// submit inserts a newline at the cursor and keeps editing if the
// resulting input isn't balanced yet (parser.BracketDepth), or
// evaluates and clears it otherwise. Inserting at the cursor rather
// than always appending at the end matters now that the cursor can sit
// mid-line (e.g. Home, then Enter, to open a new line above what's
// already there) — append-only was fine when this widget had no cursor
// movement at all.
//
// The actual evaluation does not happen inline, and — a real trap,
// see evalStartedMsg's own doc comment for the full story — it does
// NOT happen inside the returned tui.Cmd either: submit spawns a real
// goroutine itself and returns only a trivial, already-computed
// evalStartedMsg for the caller to run. w.busy blocks handleKey from
// calling submit() again (or touching anything evaluate's Env hooks
// touch) until the matching evalDoneMsg comes back through
// TakePendingMsg/applyEvalResult -- see busy's own doc comment.
// Callers that already know a Cmd here is meaningless (in particular
// the widget-only tests in kyurepl_test.go, which construct a bare
// *kyuReplWidget with no Model/App to drive TakePendingMsg for them)
// call applyEvalResult(w.evaluate(src)) directly instead of going
// through submit() -- see TestHistoryBuiltinsReachThisWidget.
func (w *kyuReplWidget) submit() tui.Cmd {
	rs := w.runes()
	withNewline := make([]rune, 0, len(rs)+1)
	withNewline = append(withNewline, rs[:w.cursor]...)
	withNewline = append(withNewline, '\n')
	withNewline = append(withNewline, rs[w.cursor:]...)
	candidate := string(withNewline)
	if parser.BracketDepth(candidate) > 0 {
		w.input = candidate
		w.cursor++
		return nil
	}
	lines, _, _ := w.renderInput()
	w.lines = append(w.lines, lines...)
	src := w.input
	if trimmedNonEmpty(src) {
		if w.historyUnique() {
			w.removeHistoryOccurrences(src)
		}
		w.history = append(w.history, src)
	}
	w.historyIndex = len(w.history)
	w.historyDraft = ""
	w.input = ""
	w.cursor = 0
	// A submit is a deliberate user action, unlike output arriving
	// passively while reviewing history — snapping back to the latest
	// output here is what every real shell does, and unlike a plain
	// scroll-follow-if-already-at-bottom rule (which this widget also
	// has, for free, by never touching scrollOffset on its own — see
	// scrollOffset's own doc comment) this needs to be unconditional:
	// you just typed something, you want to see what happened.
	w.scrollOffset = 0
	if !trimmedNonEmpty(src) {
		// Matches evaluate()'s own no-op-on-blank-input guard -- no
		// point spinning up a goroutine (and going busy) for an Enter
		// that evaluate() would immediately no-op on anyway.
		return nil
	}
	w.busy = true
	// Registered for the duration of this one evaluate() call so a real
	// Ctrl+C (handleKey's busy-gate branch, below) can actually stop a
	// foreground `while true {}` or unbounded recursion -- the same
	// Env.CancelContext/CancelFunc mechanism evalBackgroundInproc uses
	// for a backgrounded job, just registered here instead. Cleared
	// before pendingEval is set so a Ctrl+C arriving in the narrow gap
	// between evaluate() finishing and its result being picked up is a
	// harmless no-op (CancelFunc() will be nil by then) rather than
	// re-cancelling an already-finished evaluation.
	if w.env != nil {
		ctx, cancel := context.WithCancel(context.Background())
		w.env.SetCancelContext(ctx, cancel)
	}
	go func() {
		res := w.evaluate(src)
		if w.env != nil {
			w.env.SetCancelContext(nil, nil)
		}
		w.mu.Lock()
		w.pendingEval = &res
		w.mu.Unlock()
	}()
	// This Cmd's own closure does no blocking work -- see
	// evalStartedMsg's doc comment for why that specifically matters
	// here, unlike an ordinary Cmd.
	return func() tui.Msg { return evalStartedMsg{} }
}

// TakePendingMsg implements tui.PendingMsgSource, the same mechanism
// widget.Terminal uses to report a background goroutine's result with
// no HandleEvent call of its own to return a Cmd from (see its own doc
// comment): App.Dispatch calls this on every widget in the tree after
// every render, so whatever submit()'s goroutine placed in pendingEval
// surfaces as an evalDoneMsg on the next Dispatch that happens to run --
// a real keystroke's own Dispatch, or the redraw tick Model starts
// while w.busy is true (see redrawTickMsg's doc comment) so completion
// doesn't have to wait for the user to press an unrelated key. Must
// consume pendingEval so it's never reported twice.
func (w *kyuReplWidget) TakePendingMsg() tui.Msg {
	w.mu.Lock()
	res := w.pendingEval
	w.pendingEval = nil
	w.mu.Unlock()
	if res == nil {
		return nil
	}
	return evalDoneMsg{result: *res}
}

// nativeProgramLookup builds the `func(string) bool` lexer.Lexer/
// parser.Parser both want from w.env (see eval.IsNativeProgram) -- nil
// when w.env is nil (a handful of widget-behavior-only tests that don't
// exercise evaluation semantics, same guard evaluate() below already
// uses for SetFullscreenHandler/SetExternalOutputSink), which both
// Lexer.NativeProgramLookup and parser.WithNativeProgramLookup already
// treat as "no native programs recognized," matching plain old behavior.
func (w *kyuReplWidget) nativeProgramLookup() func(string) bool {
	if w.env == nil {
		return nil
	}
	return func(name string) bool { return eval.IsNativeProgram(w.env, name) }
}

// evalResult is what evaluate produces -- the replLines to append to the
// transcript (any stderr the command captured, in the order it arrived,
// followed by the successful result or the error) and a fullscreen
// attachment if the command that just ran needs one. It's a plain
// returned value rather than something evaluate applies to w directly,
// so evaluate can run safely on the background goroutine submit()
// spawns for it (see submit's own doc comment): w.lines and
// w.pendingFullscreen are also read by Paint on the UI goroutine, so
// only applyEvalResult, always called from whichever single goroutine
// owns w, may touch them.
type evalResult struct {
	lines      []replLine
	fullscreen *fullscreenAttach
}

// applyEvalResult appends res's transcript lines, clears w.busy, and --
// if res carries a fullscreen attachment -- stages it and returns the
// Cmd that hands it to Model, the same way this widget always has (see
// consumeFullscreenCmd). This is exactly the tail end evaluate() itself
// used to run inline, before evaluate had to move off the UI goroutine
// (see evalResult's doc comment) -- callers are Model.Update, handling
// the evalDoneMsg TakePendingMsg reports once submit()'s background
// goroutine finishes, and a handful of kyurepl_test.go tests that call
// evaluate() directly with no Model/App in play to apply its result for
// them (e.g. TestHistoryBuiltinsReachThisWidget).
func (w *kyuReplWidget) applyEvalResult(res evalResult) tui.Cmd {
	w.lines = append(w.lines, res.lines...)
	w.busy = false
	if res.fullscreen == nil {
		return nil
	}
	w.pendingFullscreen = res.fullscreen
	return w.consumeFullscreenCmd()
}

// attachStreamAdapter adapts eval.AttachStream (Read/Write/Close/
// Resize(rows, cols int) — deliberately free of any tui dependency, see
// its own doc comment) to tui/pty.Stream (Resize(term.Size) instead),
// the shape widget.Terminal's Stream option actually wants. Read/
// Write/Close are promoted straight through via the embedded interface;
// only Resize needs translating.
type attachStreamAdapter struct {
	eval.AttachStream
}

func (a attachStreamAdapter) Resize(sz term.Size) error {
	return a.AttachStream.Resize(sz.Rows, sz.Cols)
}

// evaluate parses and evaluates src, returning what happened as an
// evalResult rather than mutating w directly -- see evalResult's own
// doc comment for why. Called from the background goroutine submit()
// spawns, so this runs on a goroutine of its own, concurrently with
// Paint on the UI goroutine:
// it must never touch w.lines or w.pendingFullscreen itself (only
// applyEvalResult may, once this returns). w.history, via the
// SetHistoryAccess hooks below, stays fine to touch directly exactly as
// before: Paint never reads it, and handleKey's busy gate (see busy's
// own doc comment) keeps every history-editing key (Up/Down/Ctrl-R)
// from running while an evaluate() is in flight, so nothing else
// touches it concurrently either.
func (w *kyuReplWidget) evaluate(src string) evalResult {
	if !trimmedNonEmpty(src) {
		return evalResult{}
	}
	p := parser.New(src, parser.WithNativeProgramLookup(w.nativeProgramLookup()))
	prog := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		var res evalResult
		for _, e := range errs {
			res.lines = append(res.lines, replLine{text: e.Error(), style: errorStyle})
		}
		return res
	}
	// Registered only for this one Eval call, same set-before/clear-after
	// pattern Env.SetInterruptHandler already uses -- safe because
	// evaluate() calls never overlap: w.busy keeps handleKey from
	// starting a second one (see busy's own doc comment) while Env's
	// fullscreen/output-sink/history hooks are process-wide, single-slot
	// fields that assume exactly that. w.env is nil in a handful of
	// widget-behavior-only tests that don't exercise evaluation
	// semantics -- guarded the same way eval.Eval below already
	// tolerates a nil Env for those.
	//
	// The fullscreen and stderr hooks close over a local *evalResult
	// (res, below) instead of writing into w the way they used to --
	// see this method's own doc comment for why. A foreground %cmd's
	// captured stderr lands in res.lines the same styled-as-an-ordinary-
	// result way it always has (resultStyle, not errorStyle: stderr
	// isn't necessarily an error in kyu's sense, plenty of real CLI
	// tools use it for progress/informational output) instead of being
	// written to the real os.Stderr, which would otherwise corrupt this
	// widget's own screen -- replui owns the terminal via a diffed cell
	// renderer (see package doc comment), and a raw write outside that
	// renderer's own "what's on screen" bookkeeping desyncs it from
	// reality. SetHistoryAccess is the one hook still wired directly to
	// w's own methods, exactly as before: see this method's doc comment
	// for why that stays safe.
	var res evalResult
	if w.env != nil {
		w.env.SetFullscreenHandler(func(cmd *exec.Cmd, onDone func(err error)) {
			res.fullscreen = &fullscreenAttach{cmd: cmd, onDone: onDone}
		})
		w.env.SetAttachHandler(func(stream eval.AttachStream, onDone func(err error)) {
			res.fullscreen = &fullscreenAttach{stream: attachStreamAdapter{stream}, onDone: onDone}
		})
		w.env.SetExternalOutputSink(func(stderr []byte) {
			res.lines = append(res.lines, textLines(string(stderr), resultStyle)...)
		})
		w.env.SetHistoryAccess(&eval.HistoryAccess{
			List:   w.historySnapshot,
			Delete: w.deleteHistoryEntry,
			Clear:  w.clearHistoryEntries,
		})
	}
	v, err := eval.Eval(prog, w.env)
	if w.env != nil {
		w.env.SetFullscreenHandler(nil)
		w.env.SetAttachHandler(nil)
		w.env.SetExternalOutputSink(nil)
		w.env.SetHistoryAccess(nil)
	}
	if err != nil {
		res.lines = append(res.lines, replLine{text: err.Error(), style: errorStyle})
		return res
	}
	if v.Kind() != "null" {
		res.lines = append(res.lines, resultLines(v)...)
	}
	return res
}

// consumeFullscreenCmd returns a tui.Cmd yielding startFullscreenMsg if
// the evaluation that just ran attached a fullscreen program (staged by
// applyEvalResult into pendingFullscreen), clearing pendingFullscreen so
// it's only ever consumed once; nil otherwise.
func (w *kyuReplWidget) consumeFullscreenCmd() tui.Cmd {
	if w.pendingFullscreen == nil {
		return nil
	}
	attach := w.pendingFullscreen
	w.pendingFullscreen = nil
	return func() tui.Msg { return startFullscreenMsg{attach: attach} }
}

// resultLines renders a top-level evaluation result as one or more
// replLines, splitting on embedded newlines — each replLine is one visual
// row (see scrollOffset/cursor-line math above), so handing one a raw "\n"
// would render wrong (the painter has no notion of a line break mid-row).
// This applies regardless of Value kind: a plain kyu String (e.g. from
// `... | join("\n")`) embeds real newlines exactly like external command
// output does. Bytes is special-cased for its *text*, not its splitting:
// value.Bytes.String() deliberately stays a "<N bytes>" summary everywhere
// else, but a bare %cmd at the REPL is exactly the case where real output
// is wanted, not a summary.
func resultLines(v value.Value) []replLine {
	text := v.String()
	if b, ok := v.(value.Bytes); ok {
		text = string(b)
	}
	return textLines(text, resultStyle)
}

// textLines splits text into one replLine per line, in style -- the
// shared tail of resultLines and appendExternalStderr, factored out
// once a second caller needed the exact same "trim one trailing
// newline, split on the rest, empty text produces no lines" behavior.
func textLines(text string, style cell.Style) []replLine {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	rows := strings.Split(text, "\n")
	lines := make([]replLine, len(rows))
	for i, row := range rows {
		lines[i] = replLine{text: row, style: style}
	}
	return lines
}

// ---- tab completion ----

// kyuKeywords is every kyu keyword — kept in sync by hand with
// kyu/token/token.go's own (unexported) keywords map, since a list
// this short and this rarely changing doesn't justify a new exported
// API just for tab completion to reach it.
var kyuKeywords = []string{
	"if", "else", "while", "break", "continue", "bind", "unbind", "in_ns",
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
// external-command-name fragment — right after a '%' sigil, kyu's
// external-call syntax (see kyu/lexer's lexExternalName) — and if so,
// the rune index it starts at. Unlike an ordinary kyu identifier, this
// fragment allows internal hyphens (docker-compose, apt-get, ...),
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
	return start, sigil == '%'
}

// externalNameCandidates lists every PATH executable whose name starts
// with fragment, resolving PATH the same way a %cmd actually would
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

// isPathRune matches kyu/lexer's own isPathChar rule (letters, digits,
// '/', '.', '-', '_') -- the character set currentPathBounds scans
// backward over to find a bare Path literal's extent.
func isPathRune(r rune) bool {
	return isIdentRune(r) || r == '/' || r == '.' || r == '-'
}

// currentPathBounds reports whether the cursor sits inside a bare Path
// literal (kyu/lexer's Path token: an absolute, '/'-led run of path
// characters) and, if so, the rune index it starts at. Scanning back
// over isPathRune alone isn't enough to prove it's a Path -- an ordinary
// bareword like "cat" matches the same character set -- so this
// additionally requires the run to begin with '/', same as the lexer's
// own Path token can only ever start there.
func (w *kyuReplWidget) currentPathBounds() (start int, ok bool) {
	rs := w.runes()
	start = w.cursor
	for start > 0 && isPathRune(rs[start-1]) {
		start--
	}
	if start == w.cursor || rs[start] != '/' {
		return 0, false
	}
	return start, true
}

// splitPathFragment splits a Path-in-progress like "/src/gree" into the
// directory to list ("/src") and the partial final segment to match
// against each entry's name ("gree"). A fragment with no second '/' yet
// ("/sr") lists the root.
func splitPathFragment(fragment string) (dirPart, partial string) {
	i := strings.LastIndex(fragment, "/")
	dirPart, partial = fragment[:i], fragment[i+1:]
	if dirPart == "" {
		dirPart = "/"
	}
	return dirPart, partial
}

// pathCandidates completes a bare Path fragment against both the real
// filesystem and the attached namespace, merged into one candidate list
// rather than chosen by surrounding context. A Path argument to %cmd
// is legitimately either — a real /etc/hosts is exactly as valid as
// a namespace /local/foo (see kyu/eval's checkNamespaceOnlyPath, which
// has to tell the two apart precisely because both are real
// possibilities there). Completion doesn't need that same precision:
// unlike the guardrail, where guessing wrong means silently reading the
// wrong data, a completion candidate that doesn't fit the call it's
// used in is just an ordinary bash/zsh-style near-miss, corrected the
// moment the command runs. A namespace directory that also has a real
// backing directory (bind's whole point) surfaces once from each source
// under the same name; dedup keeps that from showing twice.
func (w *kyuReplWidget) pathCandidates(fragment string) []string {
	dirPart, partial := splitPathFragment(fragment)

	seen := map[string]bool{}
	var candidates []string
	add := func(name string, isDir bool) {
		if !strings.HasPrefix(name, partial) {
			return
		}
		full := strings.TrimSuffix(dirPart, "/") + "/" + name
		if isDir {
			full += "/"
		}
		if seen[full] {
			return
		}
		seen[full] = true
		candidates = append(candidates, full)
	}

	if entries, err := os.ReadDir(dirPart); err == nil {
		for _, e := range entries {
			add(e.Name(), e.IsDir())
		}
	}
	for _, ent := range w.env.ListNamespaceDir(context.Background(), dirPart) {
		add(ent.Name, ent.Qid.IsDir())
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
// Right after a '%' sigil the candidates are PATH executables (see
// currentExternalNameBounds/externalNameCandidates); inside a bare Path
// literal they're real filesystem and namespace entries merged (see
// currentPathBounds/pathCandidates); everywhere else they're
// env.Names() — every user variable and builtin, see its own doc
// comment — plus kyuKeywords.
func (w *kyuReplWidget) completeTab() {
	externalStart, isExternal := w.currentExternalNameBounds()
	pathStart, isPath := w.currentPathBounds()
	var start int
	switch {
	case isExternal:
		start = externalStart
	case isPath:
		start = pathStart
	default:
		start = w.currentIdentBounds()
	}

	if w.completionCandidates != nil && w.completionFragmentStart == start {
		w.completionCycle = (w.completionCycle + 1) % len(w.completionCandidates)
		w.replaceRange(start, w.cursor, w.completionCandidates[w.completionCycle])
		return
	}

	fragment := string(w.runes()[start:w.cursor])
	var candidates []string
	switch {
	case isExternal:
		candidates = w.externalNameCandidates(fragment)
	case isPath:
		candidates = w.pathCandidates(fragment)
	default:
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
// tui.App.HandleInput intercepts Tab globally for focus navigation
// before it's ever forwarded to HandleEvent at all (see its own doc
// comment) — completeTab would be silently unreachable dead code in
// the real app despite working correctly in every direct-call unit
// test, since those bypass App.HandleInput entirely. ReleaseKey is
// Ctrl+\, matching widget.Terminal's own default (see its
// TerminalOptions.ReleaseKey) purely for muscle-memory consistency with
// a fullscreen attachment's own Terminal — there's nothing else in this
// single-widget screen to release focus *to*, so pressing it is
// harmless, not meaningful. Not Esc, which this widget already uses for
// a different purpose (exiting reverse history search, see exitSearch).
func (w *kyuReplWidget) WantsRawTab() bool { return true }
func (w *kyuReplWidget) ReleaseKey() input.KeyEvent {
	return input.KeyEvent{Rune: '\\', Mod: input.ModCtrl}
}
