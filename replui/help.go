package replui

import (
	"strings"

	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"

	"github.com/sandgorgon/9sh/kyu/eval"
)

// helpText is the built-in help screen's content, assembled once (see
// buildHelpText) from three sections a user actually needs: this
// screen's own keybindings, the kyu language reference, and the
// bash/zsh mental-model section. Multi-pane content (control-strip
// buttons, split/zoom/minimize, F1-F9 pane jump, the namespace-browser/
// job-viewer/session-viewer panes) lived here before this package
// replaced github.com/sandgorgon/9sh's old multi-pane pane package —
// see github.com/sandgorgon/9mux for where that capability went.
var helpText, helpSectionStart = buildHelpText()

// keybindingHelp is the original (and still first) section — kept as
// plain data (not generated from the hotkey-handling code it
// documents) so it can be scanned and edited on its own; keep it in
// sync by hand whenever a binding changes elsewhere in this package
// (kyuReplWidget.handleKey).
var keybindingHelp = []string{
	"Enter                  submit, or continue if brackets are still open",
	"Tab                    complete the identifier before the cursor",
	"                       (variables, builtins, keywords) -- right after",
	"                       a % sigil, an external command from PATH;",
	"                       inside a bare Path, real filesystem and",
	"                       namespace entries merged -- fills the longest",
	"                       common match, cycles candidates on repeated Tab",
	"Ctrl+R                  reverse history search -- type to search,",
	"                         Enter runs the match, Esc loads it without",
	"                         running it",
	"Left/Right              move the cursor; Ctrl+Left/Right by word",
	"Home/End (Ctrl+A/E)     jump to the start/end of the current line",
	"Backspace/Delete        delete before/after the cursor",
	"Ctrl+W                  delete the word before the cursor",
	"Ctrl+U / Ctrl+K          delete to line start / delete to line end",
	"Ctrl+L                   clear the transcript (history is untouched)",
	"Up/Down                  recall previous/next submitted input",
	"                         (only when not mid multi-line input)",
	"PgUp/PgDown, mouse wheel  scroll the transcript",
	"Ctrl+C                   copy the whole transcript",
	"Alt+C                    copy only what's currently visible",
	"paste                    inserts at the cursor",
	"F1                       toggle this help screen",
	"Ctrl+D (at an empty prompt)  quit 9sh",
	"",
	"Running a fullscreen program (vim, top, ssh, ... -- see",
	"fullscreen_programs in /config/config.ky) hands this whole screen",
	"and keyboard to it directly until it exits; a namespace-only Path",
	"argument is checked out and written back automatically, no",
	"checkout() call needed. Mouse wheel and PgUp/PgDown scroll its",
	"scrollback too (up to 10,000 lines), unless the program manages",
	"its own full-screen display (vim, htop, less, ...), in which case",
	"those keys go to it as normal.",
	"",
	"For a real multi-pane terminal (a shell and a kyu session side by",
	"side, say) see github.com/sandgorgon/9mux -- this screen is always",
	"exactly one kyu session, by design (see this package's own doc",
	"comment).",
}

// bashZshHelp is the mental-model section — a plain-text condensation
// of the README's "Coming from bash/zsh: there is no current
// directory", kept in sync with that section by hand (same reasoning
// as keybindingHelp above: hand-maintained, scannable data, not
// generated).
var bashZshHelp = []string{
	"The single easiest wrong assumption to carry over from a Unix",
	"shell: /local (or wherever you've bind-ed things) is NOT \"where",
	"you are\", the way $PWD is.",
	"",
	"The namespace has no cwd. Every namespace path -- a bind target,",
	"glob(...)/ls(...)'s pattern, checkout(...)'s argument, anything",
	"typed as a Path -- is always a full path from the namespace root.",
	"There's no implicit context a partial path resolves against, and",
	"syntactically a Path literal must start with '/'.",
	"",
	"/local is a bootstrap convenience, not a home. At startup 9sh",
	"grafts the real OS directory it was launched from onto /local,",
	"purely so a brand-new session has something real and browsable --",
	"not because /local is a designated place you're meant to work",
	"from. It's one bind among any others you make.",
	"",
	"cd/pwd are real, but they're not about the namespace. External",
	"binaries (%cmd) genuinely need a process cwd to run in, so",
	"cd(path)/pwd() give them one -- a completely separate, OS-path-",
	"only concept. It starts out equal to /local's target (both come",
	"from the same launch-time cwd), which is exactly what makes the",
	"two feel like the same thing. They aren't: rebinding /local never",
	"moves cd's cwd, and cd() never touches /local's bind.",
	"",
	"A namespace path handed straight to %cmd is caught, not",
	"silently wrong: %cat /local/foo -- the natural first mistake this",
	"mental model produces -- errors with a hint to use checkout instead",
	"of reaching the binary as a meaningless literal string. Two",
	"exceptions: a fullscreen program (vim, ... -- fullscreen_programs)",
	"gets a namespace-only Path checked out and written back",
	"automatically instead of erroring, and a native program (9ed,",
	"9vcs, ... -- native_programs) gets the Path passed through as",
	"literal path text, untouched, trusting the program to resolve it",
	"itself -- a native program also doesn't need the % sigil at all,",
	"callable bareword like a builtin. Tab completion inside a bare",
	"Path offers both real filesystem and namespace entries, which is",
	"exactly how this tends to get typed.",
	"",
	"Practical upshot: always write full namespace paths. There's no",
	"relative-path shorthand. join_path(base, ...segments) and",
	"path(str) exist to cut the retyping without smuggling in a cwd --",
	"see the language reference above for both.",
}

// buildHelpText assembles the full document once at package load:
// keybindings, then the kyu language reference (generated from
// eval.Docs() -- the same table help(name) reads, so the two can't
// drift apart), then the bash/zsh mental-model section. Returns the
// combined lines plus each section's starting line index, for '1'/
// '2'/'3' to jump straight to (see helpWidget.HandleEvent).
func buildHelpText() (lines []string, sectionStart [3]int) {
	lines = append(lines, "9sh — help",
		"",
		"Jump to a section: 1) keybindings  2) kyu language reference",
		"                    3) coming from bash/zsh",
		"PgUp/PgDown or the wheel to scroll; Esc, '?', or a click",
		"outside this box to close.",
		"")

	sectionStart[0] = len(lines)
	lines = append(lines, keybindingHelp...)

	lines = append(lines, "", "── kyu language reference ──", "")
	sectionStart[1] = len(lines) - 2
	lines = append(lines, languageReferenceLines()...)

	lines = append(lines, "── coming from bash/zsh ──", "")
	sectionStart[2] = len(lines) - 1
	lines = append(lines, bashZshHelp...)

	return lines, sectionStart
}

// languageReferenceLines renders every eval.Docs() entry as
// "name  signature" followed by its word-wrapped description, one
// blank line between entries. Wrapped at wrapWidth, comfortably inside
// the help modal's ~76-column inner content area (Width: 78 in
// model.go's widget.Modal call, minus a 2-column border).
const wrapWidth = 72

func languageReferenceLines() []string {
	var out []string
	for _, d := range eval.Docs() {
		out = append(out, d.Name+"  "+d.Signature)
		for _, l := range wrapText(d.Description, wrapWidth) {
			out = append(out, "    "+l)
		}
		out = append(out, "")
	}
	return out
}

// wrapText greedily word-wraps s to at most width columns per line —
// good enough for the plain, unstyled reference text this renders;
// not meant to handle anything fancier (no hyphenation, no unicode
// width awareness).
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	lines = append(lines, line)
	return lines
}

// helpNode is the built-in help screen's body — see toggleHelpMsg/
// closeHelpMsg in model.go for how it opens/closes (a widget.Modal
// wrapping this, in Model.View), and helpText above for its content.
func helpNode() tui.Node {
	return tui.Component("help-content", struct{}{}, func() tui.Widget {
		return &helpWidget{}
	})
}

// helpWidget is a plain, read-only scrollable text viewer — no editing,
// unlike kyuReplWidget, which this deliberately doesn't share code
// with: the scroll math is a handful of lines, and sharing it would
// mean generalizing kyuReplWidget for one caller that needs a third of
// what it does. Also deliberately not the same *direction* of scroll
// math: kyuReplWidget's scrollOffset counts back from the bottom
// (right, for a live transcript that should default to showing the
// latest output); this is a static reference document, which should
// default to showing the *top* — scrollOffset here is a plain
// top-anchored offset instead (0 = start of the document), same
// convention a pager like less or man uses.
type helpWidget struct {
	scrollOffset int
	lastHeight   int
}

func (w *helpWidget) Reconcile(props any) bool { return true }

func (w *helpWidget) Paint(p *cell.Painter) {
	width, height := p.Size()
	if width <= 0 || height <= 0 {
		return
	}
	w.lastHeight = height
	maxStart := max0(len(helpText) - height)
	start := clampInt(w.scrollOffset, 0, maxStart)
	end := min(start+height, len(helpText))
	for y, line := range helpText[start:end] {
		p.Text(0, y, line, cell.Style{})
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (w *helpWidget) HandleEvent(e input.Event) tui.Cmd {
	switch ev := e.(type) {
	case input.MouseEvent:
		switch ev.Button {
		case input.MouseWheelUp:
			w.scrollOffset = max0(w.scrollOffset - scrollStep)
		case input.MouseWheelDown:
			w.scrollOffset += scrollStep
		}
	case input.KeyEvent:
		switch {
		case ev.Key == input.KeyEsc, ev.Rune == '?', ev.Rune == 'q':
			// Safe to claim these here (unlike a global hotkey elsewhere
			// in this package): widget.Modal claims focus exclusively
			// for its body while open, so nothing else could receive
			// this key instead.
			return func() tui.Msg { return closeHelpMsg{} }
		case ev.Key == input.KeyPgUp:
			w.scrollOffset = max0(w.scrollOffset - max0(w.lastHeight-1))
		case ev.Key == input.KeyPgDown:
			w.scrollOffset += max0(w.lastHeight - 1)
		case ev.Rune == '1':
			w.scrollOffset = helpSectionStart[0]
		case ev.Rune == '2':
			w.scrollOffset = helpSectionStart[1]
		case ev.Rune == '3':
			w.scrollOffset = helpSectionStart[2]
		}
	}
	return nil
}

func (w *helpWidget) Focusable() bool { return true }
func (w *helpWidget) SetFocused(bool) {}
