// Package pane is a minimizable multi-pane multiplexer built on tui's
// retained widget.Terminal and 9sh's own native widgets, arranged in a
// 2D split tree (see splitNode), each with an always-visible title-bar
// row that toggles it between full size and collapsed-to-title-bar.
//
// Five pane kinds exist: KindShell hosts a real pty-attached process
// (a shell, or anything else exec'able) via widget.Terminal; KindKyuRepl
// hosts a native kyu REPL (kyurepl.go) evaluating against the same
// shared *eval.Env as every other entry point into 9sh (see cmd/9sh's
// bootstrap); KindNamespaceBrowser (browser.go) is a live, navigable
// view of whatever's bound in that Env's namespace; KindJobViewer
// (jobviewer.go) spans /jobs plus every /n/<host>/jobs currently
// bound; KindSessionViewer (sessionviewer.go) reads back 9vcs-backed
// session history from disk. The latter three are the namespace-aware
// "differentiator" panes the project's design notes call for.
//
// Minimizing is a click/Enter action on a pane's title bar, not a
// global hotkey: tui.App delivers every key to both Model.Update and
// the focused widget at once, with no way to suppress the latter — a
// hotkey pressed while a Terminal pane is focused would be forwarded
// straight into the hosted shell right alongside whatever Update did
// with it. F1-F9 (jump keyboard focus straight to pane N, see
// paneOrder and Update's input.KeyEvent case) are a deliberate
// exception to that rule: they're the one case where a real global
// hotkey is worth the same forwarding-into-the-hosted-shell tradeoff,
// chosen specifically because F-keys are far less likely than a plain
// letter/digit to collide with anything a real shell or its readline
// bindings already use. This needed sandgorgon/tui#5 and #7 (App.
// SetFocus/FocusIndex, then Run() honoring an Update-triggered
// tui.FocusMsg) to become possible at all — Model.Update has no access
// to the live *tui.App, so before those landed there was no path from
// "Update saw the hotkey" to "focus actually moved". Panes are
// arranged in a layout tree (see splitNode), not a
// flat list; every node in that tree — interior split or pane leaf —
// keeps an explicit, stable key at every level: reconcile.go's key
// matching is scoped per-parent, so an unkeyed ancestor whose position
// (or, here, whose very identity across a tree restructuring) shifts
// would discard and rebuild everything beneath it — including a live
// Terminal's pty — even though the leaf node itself carried a key. See
// appendTopLevelRow's doc comment for the concrete case this guards
// against, and TestAddingSecondPaneKeepsFirstPaneAlive for the
// regression test that catches it.
package pane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/sandgorgon/tui/cell"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/layout"
	"github.com/sandgorgon/tui/style"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/ns"
)

type Kind int

const (
	KindShell Kind = iota
	KindKyuRepl
	KindNamespaceBrowser
	KindJobViewer
	KindSessionViewer
)

// Spec describes a pane to create, at startup (New) or later (AddPane).
type Spec struct {
	Title      string
	Kind       Kind
	Command    *exec.Cmd // KindShell only
	Env        *eval.Env // KindKyuRepl, KindNamespaceBrowser, KindJobViewer
	SessionDir string    // KindSessionViewer only — see SessionViewerSpec
}

// ShellSpec is a convenience Spec running the user's $SHELL (/bin/sh if
// unset).
func ShellSpec(title string) Spec {
	return Spec{Title: title, Kind: KindShell, Command: exec.Command(shellPath())}
}

// KyuReplSpec is a native kyu REPL pane evaluating against env.
func KyuReplSpec(title string, env *eval.Env) Spec {
	return Spec{Title: title, Kind: KindKyuRepl, Env: env}
}

// NamespaceBrowserSpec is a live namespace browser rooted at "/" in
// env's namespace.
func NamespaceBrowserSpec(title string, env *eval.Env) Spec {
	return Spec{Title: title, Kind: KindNamespaceBrowser, Env: env}
}

// JobViewerSpec is a live job viewer spanning /jobs and every /n/<host>
// currently bound in env's namespace — see jobviewer.go.
func JobViewerSpec(title string, env *eval.Env) Spec {
	return Spec{Title: title, Kind: KindJobViewer, Env: env}
}

// SessionViewerSpec is a session-history viewer reading dir (typically
// ~/.config/9/session, the same directory cmd/9sh's bootstrapSession
// passes to session.New) — see sessionviewer.go. dir may be empty (no
// session history was available at startup at all); the pane reports
// that as its own error rather than a silent empty list.
func SessionViewerSpec(title, dir string) Spec {
	return Spec{Title: title, Kind: KindSessionViewer, SessionDir: dir}
}

func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

type paneState struct {
	id        int
	title     string
	kind      Kind
	minimized bool
	exited    bool
	exitErr   error

	// awaitingSplitKind is true between a beginSplitMsg (d/r pressed) and
	// whatever resolves it (a recognized kind key -> splitPaneMsg, or
	// anything else -> cancelSplitMsg) — see paneNode's title-bar key
	// handler, which is the only thing that reads or sets this via those
	// two Msg types.
	awaitingSplitKind bool
	awaitingSplitDir  layout.Direction

	command *exec.Cmd // KindShell
	env     *eval.Env // KindKyuRepl, KindNamespaceBrowser, KindJobViewer

	// KindNamespaceBrowser's own business state — List's cursor (and,
	// by the same convention here, the current path and listing) is
	// caller-owned, not retained inside the widget; see browser.go.
	browserPath    string
	browserEntries []string
	browserCursor  int
	browserErr     string

	// KindJobViewer's own business state — see jobviewer.go.
	jobRows   []string
	jobCursor int
	jobErr    string

	// KindSessionViewer's own business state — see sessionviewer.go.
	// sessionDir is copied from Spec.SessionDir at construction (see
	// withNewPane); it never changes for this pane's lifetime.
	sessionDir    string
	sessionRows   []string
	sessionCursor int
	sessionErr    string
}

// splitNode is one node of the pane-layout tree: either a leaf
// (paneID != 0, referencing a paneState by id — the actual per-pane
// business state lives there, not here) or an interior split (paneID
// == 0, dir meaningful, children the panes/sub-splits it arranges
// along that axis). This is deliberately a separate structure from
// paneState/m.panes: paneState is "what does this pane hold", the
// tree is "where does it sit" — the two vary independently (a pane's
// own state doesn't change when it's moved to a different split).
//
// Every splitNode, leaf or interior, gets its own stable id, used as
// that node's tui.Node.Key in renderSplit. This is load-bearing, not
// cosmetic: reconcile.go's key matching is scoped per-parent (see
// tui/reconcile.go's reconcileChildren), so an interior node's own
// identity has to stay stable across frames for its *children's*
// retained state (a live Terminal's pty in particular) to survive —
// exactly the failure mode Phase 3's original pane work already found
// and fixed once for the flat-list case; this tree has to uphold the
// same invariant one level deeper. See appendTopLevelRow's doc comment
// for the concrete case this guards against.
type splitNode struct {
	id       int
	paneID   int // >0 for a leaf; 0 for an interior split
	dir      layout.Direction
	children []splitChild
}

// splitChild pairs a splitNode with its weight (layout.Fill(weight))
// within the parent split — a minimized leaf overrides this to
// layout.Length(1) at render time instead; see renderSplit.
type splitChild struct {
	weight int
	node   *splitNode
}

// Model is the pane multiplexer's tui.Model.
type Model struct {
	panes       []*paneState
	nextID      int
	root        *splitNode // the pane-layout tree; see splitNode's doc comment
	nextSplitID int
	env         *eval.Env   // shared with every pane's spec that needs one, for the "+" buttons
	theme       style.Theme // for the control strip / title bar background bars — see barStyle
	sessionDir  string      // for the "+ history" button's SessionViewerSpec — see New

	// nextSplitDir is the direction the next control-strip "+" addition
	// splits along — see addPaneMsg's handling in Update. Alternated
	// after every use (Horizontal, Vertical, Horizontal, ...) so
	// repeated "+" clicks grow the tree in both dimensions instead of
	// only ever deepening one axis, the same 2D tiling a title-bar d/r
	// split already gives, now for "+" too.
	nextSplitDir layout.Direction

	// zoomedID is the id of the pane currently filling the whole
	// content area (0 = no zoom) — see toggleZoomMsg and childConstraint.
	// Every other pane stays mounted in the tree (never removed from
	// View()'s output), just collapsed to Length(0) — the same
	// preserve-retained-state-by-collapsing-not-removing approach
	// per-pane minimize already established, applied to every sibling
	// off the zoomed pane's path at once, deliberately not limited to
	// panes sitting along a Vertical axis the way minimize is.
	zoomedID int

	// shellTickRunning tracks whether a shellRedrawTickCmd chain is
	// already in flight, so a shell pane created later (splitPaneMsg/
	// addPaneMsg) doesn't start a redundant second chain alongside one
	// New/Init already started for an earlier shell pane. See
	// shellRedrawTickCmd's doc comment for why this exists at all.
	shellTickRunning bool

	// helpOpen is whether the built-in help screen (see help.go) is
	// currently shown, as a widget.Modal overlay in View() — toggled by
	// the control strip's "help" button (toggleHelpMsg) or closed from
	// inside the modal itself (closeHelpMsg; see helpWidget.HandleEvent
	// for why Esc/'?'/'q' are safe to claim there but not as a global
	// hotkey elsewhere in this package).
	helpOpen bool

	// focusedKey mirrors tui.FocusAware's SetFocusedKey — see that
	// method's doc comment for why a *string (allocated once in New,
	// like panes' own []*paneState pointers) rather than a plain string
	// field: SetFocusedKey is called directly by App.render() on
	// whatever Model App already holds, entirely outside the normal
	// "Update returns a new Model value, App replaces a.model" flow
	// every other field relies on — a plain field's mutation would be
	// silently discarded the moment SetFocusedKey returns, since every
	// Update-produced Model is its own copy. Mutating through the
	// shared pointer instead means every copy of this Model sees the
	// same underlying string.
	focusedKey *string
}

// New builds a Model seeded with the given panes. env is used to build
// new kyu-repl/namespace-browser/job-viewer panes from the control
// strip's "+" buttons — pass the same *eval.Env used elsewhere in the
// process (see cmd/9sh's bootstrap) so kyu state is shared, not
// duplicated per pane. sessionDir is likewise for the "+ history"
// button (see SessionViewerSpec); pass "" if session history isn't
// available this run (cmd/9sh's bootstrapSession already knows) — the
// pane reports that itself rather than cmd/9sh needing to skip adding
// the button at all.
//
// The theme is picked once, at startup, from $COLORFGBG (style.
// DetectAppearance's own doc comment explains why that heuristic and
// not a real query — there's no escape sequence every terminal answers
// for "what's your background color"), defaulting to Dark on anything
// unparseable. 9sh doesn't re-detect this mid-session (a user whose
// terminal theme changes while 9sh is running would need to restart it
// to pick up the new autodetection result — not worth polling for) but
// the control strip's "theme" button (see themeButton/toggleThemeMsg)
// does let the user flip Dark/Light by hand, live, without a restart.
func New(env *eval.Env, sessionDir string, specs ...Spec) Model {
	m := Model{env: env, sessionDir: sessionDir, theme: style.Default(style.DetectAppearance(os.Getenv)), nextSplitDir: layout.Horizontal, focusedKey: new(string)}
	// root is created once, up front, as an (initially empty) Vertical
	// split — never replaced or re-wrapped afterward. The seed panes
	// passed in here (specs) are appended straight to it, since there's
	// nothing yet to split off of; every pane added later, whether via
	// a title-bar d/r split or a control-strip "+" (see addPaneMsg in
	// Update), goes through splitPane instead, reparenting into a fresh
	// interior split rather than appending another root sibling. This
	// is what appendTopLevelRow's doc comment is about — root's
	// identity has to be stable from the very first frame, or promoting
	// a lone leaf into a wrapping split later would change that leaf's
	// parent and discard its retained state (a live Terminal's pty
	// included).
	m.nextSplitID++
	m.root = &splitNode{id: m.nextSplitID, dir: layout.Vertical}
	for _, s := range specs {
		m = m.withNewPane(s)
	}
	// Init (called separately by the App, from this same starting
	// Model) starts the actual first shellRedrawTickCmd when a seed
	// spec is a shell pane; set the flag here so it's already true
	// before Update ever runs, keeping the two in sync from frame one.
	m.shellTickRunning = hasShellPane(m)
	return m
}

// hasShellPane reports whether any pane currently hosts a live
// widget.Terminal — see shellRedrawTickCmd's doc comment.
func hasShellPane(m Model) bool {
	for _, p := range m.panes {
		if p.kind == KindShell {
			return true
		}
	}
	return false
}

type shellRedrawTickMsg struct{}

// shellRedrawInterval balances "shell output shows up promptly" against
// redraw overhead — see shellRedrawTickCmd's doc comment.
const shellRedrawInterval = 50 * time.Millisecond

// shellRedrawTickCmd self-reschedules (see shellRedrawTickMsg's
// handling in Update) for as long as at least one KindShell pane is
// mounted. widget.Terminal's own doc comment explains why this is
// needed: a hosted pty's output updates the widget's internal
// vt.Screen state continuously in a background goroutine, but that
// only becomes visible the next time the App happens to render a
// frame for any other reason. Without this, a shell pane's output
// from a command only appeared after some unrelated keypress forced a
// redraw (e.g. the *next* Enter, not the one that ran the command).
func shellRedrawTickCmd() tui.Cmd {
	return func() tui.Msg {
		time.Sleep(shellRedrawInterval)
		return shellRedrawTickMsg{}
	}
}

// withShellTickIfNeeded starts a shellRedrawTickCmd chain alongside
// cmd when spec just created a new KindShell pane and no chain is
// already running on next — see shellRedrawTickCmd's doc comment.
// Shared by every pane-creation path (splitPaneMsg, both addPaneMsg
// branches) so they can't each start their own redundant chain.
func withShellTickIfNeeded(next Model, spec Spec, cmd tui.Cmd) (Model, tui.Cmd) {
	if spec.Kind != KindShell || next.shellTickRunning {
		return next, cmd
	}
	next.shellTickRunning = true
	if cmd == nil {
		// The common case (KindShell has no initial-load Cmd of its
		// own) — avoid an unnecessary tui.Batch wrapper around just
		// one Cmd.
		return next, shellRedrawTickCmd()
	}
	return next, tui.Batch(cmd, shellRedrawTickCmd())
}

func (m Model) withNewPane(s Spec) Model {
	m.nextID++
	p := newPaneState(m.nextID, s)
	m.panes = append(m.panes, p)
	m.appendTopLevelRow(&splitNode{paneID: p.id})
	return m
}

// newPaneState builds a fresh paneState from spec — shared by
// withNewPane (top-level "+" additions) and splitPane (splitting an
// existing pane), so both construct a pane identically.
func newPaneState(id int, s Spec) *paneState {
	return &paneState{
		id: id, title: s.Title, kind: s.Kind,
		command: s.Command, env: s.Env, browserPath: "/",
		sessionDir: s.SessionDir,
	}
}

// initialLoadCmd returns the Cmd (if any) a freshly created pane needs
// to populate itself — shared by addPaneMsg (a control-strip "+"
// button) and splitPaneMsg (splitting an existing pane), so a
// split-created browser/job/session-viewer pane loads exactly the same
// way a top-level one does. newPane must be the pane just built from
// spec (its id, and for KindNamespaceBrowser its initial browserPath).
func initialLoadCmd(newPane *paneState, spec Spec) tui.Cmd {
	switch spec.Kind {
	case KindNamespaceBrowser:
		return listDirCmd(newPane.id, spec.Env.Namespace(), newPane.browserPath)
	case KindJobViewer:
		return listJobsCmd(newPane.id, spec.Env.Namespace())
	case KindSessionViewer:
		return listSessionCmd(newPane.id, spec.SessionDir)
	}
	return nil
}

// appendTopLevelRow adds leaf as a new full-width row at the bottom of
// the pane stack — used only for New's seed panes (specs), which have
// nothing yet to split off of. Every pane added afterward, whether via
// a title-bar d/r split or a control-strip "+" (see addPaneMsg in
// Update), goes through splitPane instead, so this stays a
// construction-time-only path, not a general "add a pane" one. Always
// appends to m.root's existing children slice,
// never replaces or re-wraps m.root itself: m.root's identity is fixed
// once, in New, specifically so this never has to "promote" an
// existing lone child into a new wrapping split node — doing so would
// change that child's parent from reconcile's point of view (even
// though the child keeps its own stable key), discarding its retained
// widget state, a live Terminal's pty included. Appending a sibling to
// an already-stable, already-keyed parent is the well-supported case
// instead — see tui/reconcile.go's reconcileChildren doc comment on
// why reordering/inserting/removing *siblings* is safe.
func (m *Model) appendTopLevelRow(leaf *splitNode) {
	m.root.children = append(m.root.children, splitChild{weight: defaultPaneWeight, node: leaf})
}

// closePane removes id's pane entirely — from both the flat store and
// the layout tree — relying on tui's reconciler to dispose its retained
// widget state (a Terminal's live pty included) once its Node simply
// stops appearing in the tree, the same disposeTree mechanism that
// already runs for any other tree shrink. Closing the last remaining
// pane quits, the same way the quit button already does — there's no
// sensible empty-screen state to design for instead.
func (m Model) closePane(id int) (Model, tui.Cmd) {
	if m.find(id) == nil {
		return m, nil
	}
	panes := make([]*paneState, 0, len(m.panes))
	for _, p := range m.panes {
		if p.id != id {
			panes = append(panes, p)
		}
	}
	m.panes = panes
	m.root = removeLeafFromTree(m.root, id)
	if m.zoomedID == id {
		// Closing the zoomed pane itself must un-zoom — otherwise
		// zoomedID would reference a pane that no longer exists, and
		// childConstraint's subtreeContainsPane check would never match
		// anything, collapsing every remaining pane to Length(1) with
		// nothing left to fill the freed space.
		m.zoomedID = 0
	}
	if len(m.root.children) == 0 {
		return m, tui.Quit()
	}
	return m, nil
}

// removeLeafFromTree returns n with the leaf for paneID removed.
// Deliberately does *not* collapse an interior node left with only one
// (or zero) children into its own parent's slot — that would "unwrap"
// the remaining child into its grandparent's child list, changing that
// child's parent from reconcile's point of view even though the child
// keeps its own stable key, exactly appendTopLevelRow's promote-a-leaf
// risk, just in reverse. A leftover single-child (or, once nested
// splits exist, empty) interior node is harmless: Box lays out however
// many children it actually has, so a lone child still gets the whole
// available space — this is a cosmetic tree-tidiness question, not a
// correctness one, not worth the regression risk. (Originally planned
// as "collapse the parent"; changed during implementation once this
// risk became clear — see the plan file/session history for why.)
func removeLeafFromTree(n *splitNode, paneID int) *splitNode {
	if n == nil {
		return nil
	}
	if n.paneID != 0 {
		if n.paneID == paneID {
			return nil
		}
		return n
	}
	kept := make([]splitChild, 0, len(n.children))
	for _, c := range n.children {
		if updated := removeLeafFromTree(c.node, paneID); updated != nil {
			kept = append(kept, splitChild{weight: c.weight, node: updated})
		}
	}
	n.children = kept
	return n
}

// splitPane replaces id's own tree slot with a new interior split
// containing id's existing pane and a fresh pane built from spec along
// dir, giving the pair equal weight. Returns the new pane too (nil if
// id wasn't found, in which case m is returned unchanged) so Update
// can fire whatever initial-load Cmd that pane's kind needs — see
// initialLoadCmd, shared with addPaneMsg's own handling.
//
// Splitting a pane that currently holds retained widget state (a live
// Terminal's pty in particular) preserves that state across the
// reparent: the pane's Node moves one level deeper (wrapped in the new
// split), which used to discard retained state entirely, since tui's
// reconciler matched children strictly per-parent, one level at a time
// (see appendTopLevelRow's and removeLeafFromTree's doc comments for
// the same constraint, still relevant for the sibling-level case they
// each handle). Filed as sandgorgon/tui#3; fixed there by a whole-tree
// key index (reconciler falls back to it when a local per-parent match
// misses) and picked up here via the tui v0.1.10 bump — no code change
// needed on this side once the dependency moved.
// TestSplittingLiveShellPanePreservesItsProcess pins down the new
// behavior so a future tui regression would be caught here too.
func (m Model) splitPane(id int, dir layout.Direction, spec Spec) (Model, *paneState) {
	if orig := m.find(id); orig == nil {
		return m, nil
	} else {
		orig.awaitingSplitKind = false
	}
	m.nextID++
	newPane := newPaneState(m.nextID, spec)
	m.panes = append(m.panes, newPane)
	m.nextSplitID++
	splitLeaf(m.root, id, dir, &splitNode{paneID: newPane.id}, m.nextSplitID)
	return m, newPane
}

// splitLeaf walks n looking for the direct child whose leaf is
// targetPaneID, and if found, replaces that child's node in place with
// a new interior split (id newSplitID, direction dir) containing the
// original node and newLeaf, each starting at defaultPaneWeight —
// preserving the outer splitChild's own weight in its parent unchanged.
// Reports whether the target was found (and thus split).
func splitLeaf(n *splitNode, targetPaneID int, dir layout.Direction, newLeaf *splitNode, newSplitID int) bool {
	if n == nil || n.paneID != 0 {
		return false
	}
	for i, c := range n.children {
		if c.node.paneID == targetPaneID {
			wrapped := &splitNode{
				id:  newSplitID,
				dir: dir,
				children: []splitChild{
					{weight: defaultPaneWeight, node: c.node},
					{weight: defaultPaneWeight, node: newLeaf},
				},
			}
			n.children[i] = splitChild{weight: c.weight, node: wrapped}
			return true
		}
		if splitLeaf(c.node, targetPaneID, dir, newLeaf, newSplitID) {
			return true
		}
	}
	return false
}

// defaultPaneWeight is every new pane's starting weight — deliberately
// not 1 (a real, reported bug 2026-08-30): 1 is also resizePane's own
// floor (layout.Fill needs a positive weight to mean anything, and
// tui's own layout.Split floors Fill's weight at 1 internally too —
// see layout.go's Split, "case kindFill: w := c.value; if w <= 0 { w =
// 1 }" — so even bypassing resizePane's own clamp couldn't shrink a
// pane past that). A pane starting at the resize floor means '-'
// (which decrements toward that same floor) is a no-op from the very
// first press — there was never any room to shrink below "the
// original size" in the first place. Starting well above the floor
// instead gives '-' real headroom before it hits that limit, and '+'
// symmetric room to grow.
const defaultPaneWeight = 4

// resizePane adjusts id's own weight within its direct parent's
// children by delta, clamped to a minimum of 1 (layout.Fill needs a
// positive weight to mean anything).
func (m Model) resizePane(id int, delta int) Model {
	adjustWeight(m.root, id, delta)
	return m
}

func adjustWeight(n *splitNode, targetPaneID int, delta int) bool {
	if n == nil || n.paneID != 0 {
		return false
	}
	for i, c := range n.children {
		if c.node.paneID == targetPaneID {
			w := c.weight + delta
			if w < 1 {
				w = 1
			}
			n.children[i].weight = w
			return true
		}
		if adjustWeight(c.node, targetPaneID, delta) {
			return true
		}
	}
	return false
}

// paneOrder returns every pane id in the same depth-first, left-to-
// right document order tui's own reconciler visits the tree in
// (matching renderSplit's traversal exactly, since both walk
// n.children in the same stored order) — this is the order Tab
// visits panes in, and so also the order F1-F9's pane numbering and
// Update's input.KeyEvent case rely on.
func (m Model) paneOrder() []int {
	var order []int
	var walk func(n *splitNode)
	walk = func(n *splitNode) {
		if n == nil {
			return
		}
		if n.paneID != 0 {
			order = append(order, n.paneID)
			return
		}
		for _, c := range n.children {
			walk(c.node)
		}
	}
	walk(m.root)
	return order
}

// addPaneTarget reports which pane a control-strip "+" should split —
// the last pane in paneOrder()'s document order, or false if there are
// none at all. "Last in document order" rather than "whatever's
// focused" is a deliberate simplification, not a placeholder for a
// better answer: tui.Model.Update has no way to learn which widget
// currently has focus (SetFocusCmd only ever sets it — see
// sandgorgon/tui#5/#7 and this file's own top doc comment on F1-F9 for
// the same gap, hit and worked around the same way there). Splitting
// off the last pane instead needs no such visibility and is fully
// deterministic from Model's own state, at the cost of "+" not
// necessarily landing next to whatever the user was just looking at.
func addPaneTarget(m Model) (int, bool) {
	order := m.paneOrder()
	if len(order) == 0 {
		return 0, false
	}
	return order[len(order)-1], true
}

// otherDirection flips Horizontal<->Vertical — see Model.nextSplitDir.
func otherDirection(d layout.Direction) layout.Direction {
	if d == layout.Horizontal {
		return layout.Vertical
	}
	return layout.Horizontal
}

// otherAppearance flips Dark<->Light — see toggleThemeMsg.
func otherAppearance(a style.Appearance) style.Appearance {
	if a == style.Dark {
		return style.Light
	}
	return style.Dark
}

// fKeyPaneNumber reports the 1-indexed pane number an F-key requests
// (F1 -> 1, ... F9 -> 9), or false for any other key. Capped at F9/9
// panes on purpose: beyond that, a dedicated F-key per pane stops
// being a usable mnemonic anyway (real F-key rows only go to F12, and
// F10-F12 are already claimed by some terminals/window managers) —
// panes past the 9th just don't get a jump hotkey or a "[F#]" label.
func fKeyPaneNumber(k input.Key) (int, bool) {
	if k >= input.KeyF1 && k <= input.KeyF9 {
		return int(k-input.KeyF1) + 1, true
	}
	return 0, false
}

// Init kicks off the initial namespace/disk listing for any seed panes
// that need it (namespace browser, job viewer, session viewer) — each
// does real I/O, which belongs in a Cmd, not directly in Update/View.
func (m Model) Init() tui.Cmd {
	var cmds []tui.Cmd
	for _, p := range m.panes {
		switch p.kind {
		case KindNamespaceBrowser:
			cmds = append(cmds, listDirCmd(p.id, p.env.Namespace(), p.browserPath))
		case KindJobViewer:
			cmds = append(cmds, listJobsCmd(p.id, p.env.Namespace()))
		case KindSessionViewer:
			cmds = append(cmds, listSessionCmd(p.id, p.sessionDir))
		}
	}
	if m.shellTickRunning {
		cmds = append(cmds, shellRedrawTickCmd())
	}
	return tui.Batch(cmds...)
}

type toggleMinimizeMsg struct{ id int }
type closePaneMsg struct{ id int }

// beginSplitMsg starts the two-step split flow on id's title bar: dir
// is already chosen (d/r), the next keypress on that same title bar
// picks the new sibling's kind (see splitKindKey) or cancels.
type beginSplitMsg struct {
	id  int
	dir layout.Direction
}

// cancelSplitMsg abandons an in-progress beginSplitMsg without
// splitting — any title-bar key that isn't a recognized kind while
// awaitingSplitKind is true produces this.
type cancelSplitMsg struct{ id int }

// splitPaneMsg actually performs the split: id's pane gets a new
// sibling along dir, built from spec (see splitKindKey/
// defaultSpecForKind for how a title-bar keypress becomes a Spec).
type splitPaneMsg struct {
	id   int
	dir  layout.Direction
	spec Spec
}
type resizePaneMsg struct {
	id    int
	delta int
}
type paneExitedMsg struct {
	id  int
	err error
}
type addPaneMsg struct{ spec Spec }
type quitRequestedMsg struct{}
type toggleThemeMsg struct{}
type toggleHelpMsg struct{}
type closeHelpMsg struct{}

// toggleZoomMsg zooms id to fill the whole content area, or un-zooms
// it back to the normal split layout if it's already the zoomed pane
// — see Model.zoomedID.
type toggleZoomMsg struct{ id int }

// AddPane returns a Msg that adds a new pane, splitting off the last
// pane in document order (see addPaneTarget) rather than appending a
// root-level row — usable from outside this package (e.g. a future
// kyu-level "open a pane" integration) via whatever Cmd/message-
// injection path the host App exposes.
func AddPane(s Spec) tui.Msg { return addPaneMsg{spec: s} }

func (m Model) Update(msg tui.Msg) (tui.Model, tui.Cmd) {
	switch mm := msg.(type) {
	case input.KeyEvent:
		// Every input.Event reaches Update via App.HandleInput's
		// unconditional Dispatch, regardless of which widget currently
		// has focus (see this file's own doc comment on why F1-F9
		// specifically are safe to treat as a real global hotkey here).
		// n is 1-indexed to match the F-key number shown in each title
		// bar's "[F#]" prefix (paneNode); paneOrder()'s Nth entry sits
		// at focus index controlStripFocusables + (N-1)*2, since every
		// pane contributes exactly two consecutive focusables (its
		// title bar, then its content) in that same document order —
		// see controlStripFocusables' own doc comment for the "+2 per
		// pane" half of this arithmetic.
		if n, ok := fKeyPaneNumber(mm.Key); ok {
			if order := m.paneOrder(); n <= len(order) {
				return m, tui.SetFocusCmd(controlStripFocusables + (n-1)*2)
			}
		}
	case closePaneMsg:
		return m.closePane(mm.id)
	case beginSplitMsg:
		if p := m.find(mm.id); p != nil {
			p.awaitingSplitKind = true
			p.awaitingSplitDir = mm.dir
		}
	case cancelSplitMsg:
		if p := m.find(mm.id); p != nil {
			p.awaitingSplitKind = false
		}
	case splitPaneMsg:
		next, newPane := m.splitPane(mm.id, mm.dir, mm.spec)
		if newPane == nil {
			return next, nil
		}
		return withShellTickIfNeeded(next, mm.spec, initialLoadCmd(newPane, mm.spec))
	case resizePaneMsg:
		return m.resizePane(mm.id, mm.delta), nil
	case toggleMinimizeMsg:
		if p := m.find(mm.id); p != nil {
			p.minimized = !p.minimized
		}
	case paneExitedMsg:
		if p := m.find(mm.id); p != nil {
			p.exited = true
			p.exitErr = mm.err
		}
	case addPaneMsg:
		// Splits the last pane in document order rather than appending
		// another root-level row — see addPaneTarget's doc comment for
		// why "last in paneOrder()" and not "whatever's focused" (tui
		// gives Update no way to learn current focus at all, only to
		// set it — see this file's own top doc comment on F1-F9 for the
		// same constraint hit before). Falls back to withNewPane only if
		// there's truly no existing pane to split off of, which
		// shouldn't happen in practice (New always seeds at least one)
		// but keeps this total rather than silently dropping the pane.
		target, ok := addPaneTarget(m)
		if !ok {
			next := m.withNewPane(mm.spec)
			newPane := next.panes[len(next.panes)-1]
			return withShellTickIfNeeded(next, mm.spec, initialLoadCmd(newPane, mm.spec))
		}
		dir := m.nextSplitDir
		next, newPane := m.splitPane(target, dir, mm.spec)
		next.nextSplitDir = otherDirection(dir)
		if newPane == nil {
			return next, nil
		}
		return withShellTickIfNeeded(next, mm.spec, initialLoadCmd(newPane, mm.spec))
	case quitRequestedMsg:
		return m, tui.Quit()
	case toggleThemeMsg:
		m.theme = style.Default(otherAppearance(m.theme.Appearance))
	case toggleHelpMsg:
		m.helpOpen = !m.helpOpen
	case closeHelpMsg:
		m.helpOpen = false
	case toggleZoomMsg:
		if m.zoomedID == mm.id {
			m.zoomedID = 0
		} else if m.find(mm.id) != nil {
			m.zoomedID = mm.id
		}
	case browserMoveMsg:
		if p := m.find(mm.id); p != nil {
			p.browserCursor = clamp(p.browserCursor+mm.delta, 0, max0(len(p.browserEntries)-1))
		}
	case browserEnterMsg:
		if p := m.find(mm.id); p != nil {
			return m, m.browserEnter(p, p.browserCursor)
		}
	case browserClickMsg:
		if p := m.find(mm.id); p != nil {
			p.browserCursor = clamp(mm.index, 0, max0(len(p.browserEntries)-1))
			return m, m.browserEnter(p, p.browserCursor)
		}
	case browserUpMsg:
		if p := m.find(mm.id); p != nil && p.browserPath != "/" {
			newPath := parentNSPath(p.browserPath)
			p.browserPath, p.browserCursor = newPath, 0
			return m, listDirCmd(mm.id, p.env.Namespace(), newPath)
		}
	case browserListedMsg:
		if p := m.find(mm.id); p != nil && p.browserPath == mm.path {
			p.browserErr = ""
			p.browserEntries = mm.names
			if mm.err != nil {
				p.browserErr = mm.err.Error()
				p.browserEntries = nil
			}
		}
	case jobViewerMoveMsg:
		if p := m.find(mm.id); p != nil {
			p.jobCursor = clamp(p.jobCursor+mm.delta, 0, max0(len(p.jobRows)-1))
		}
	case jobViewerRefreshRequestedMsg:
		if p := m.find(mm.id); p != nil {
			return m, listJobsCmd(p.id, p.env.Namespace())
		}
	case jobsListedMsg:
		if p := m.find(mm.id); p != nil {
			p.jobErr = ""
			p.jobRows = mm.rows
			if mm.err != nil {
				p.jobErr = mm.err.Error()
				p.jobRows = nil
			}
			p.jobCursor = clamp(p.jobCursor, 0, max0(len(p.jobRows)-1))
		}
	case sessionViewerMoveMsg:
		if p := m.find(mm.id); p != nil {
			p.sessionCursor = clamp(p.sessionCursor+mm.delta, 0, max0(len(p.sessionRows)-1))
		}
	case sessionViewerRefreshRequestedMsg:
		if p := m.find(mm.id); p != nil {
			return m, listSessionCmd(p.id, p.sessionDir)
		}
	case sessionListedMsg:
		if p := m.find(mm.id); p != nil {
			p.sessionErr = ""
			p.sessionRows = mm.rows
			if mm.err != nil {
				p.sessionErr = mm.err.Error()
				p.sessionRows = nil
			}
			p.sessionCursor = clamp(p.sessionCursor, 0, max0(len(p.sessionRows)-1))
		}
	case shellRedrawTickMsg:
		// shellTickRunning (set wherever a chain starts — New/Init and
		// withShellTickIfNeeded) keeps this to a single chain at a
		// time; reschedule while a shell pane still needs it, or clear
		// the flag and let it die so a shell pane added later starts a
		// fresh chain instead of finding one it thinks is still running.
		if hasShellPane(m) {
			return m, shellRedrawTickCmd()
		}
		m.shellTickRunning = false
		return m, nil
	}
	return m, nil
}

// browserEnter treats entries[idx] as selected: ".." goes up, a
// directory (trailing "/") descends, a plain file is a no-op for v1
// (no file-content view yet).
func (m Model) browserEnter(p *paneState, idx int) tui.Cmd {
	if idx < 0 || idx >= len(p.browserEntries) {
		return nil
	}
	selected := p.browserEntries[idx]
	if selected == ".." {
		newPath := parentNSPath(p.browserPath)
		p.browserPath, p.browserCursor = newPath, 0
		return listDirCmd(p.id, p.env.Namespace(), newPath)
	}
	if !strings.HasSuffix(selected, "/") {
		return nil
	}
	newPath := joinNSPath(p.browserPath, strings.TrimSuffix(selected, "/"))
	p.browserPath, p.browserCursor = newPath, 0
	return listDirCmd(p.id, p.env.Namespace(), newPath)
}

func (m Model) find(id int) *paneState {
	for _, p := range m.panes {
		if p.id == id {
			return p
		}
	}
	return nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func joinNSPath(base, name string) string {
	if base == "/" {
		return "/" + name
	}
	return base + "/" + name
}

func parentNSPath(p string) string {
	trimmed := strings.TrimSuffix(p, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx <= 0 {
		return "/"
	}
	return trimmed[:idx]
}

// ---- namespace listing: real I/O, so it's a Cmd, not done in Update ----

type browserListedMsg struct {
	id    int
	path  string
	names []string
	err   error
}

func listDirCmd(id int, namespace *ns.Namespace, path string) tui.Cmd {
	return func() tui.Msg {
		ctx := context.Background()
		root, err := namespace.Attach(ctx, "9sh", "")
		if err != nil {
			return browserListedMsg{id: id, path: path, err: err}
		}
		f := root
		for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
			if part == "" {
				continue
			}
			f, err = f.Walk(ctx, part)
			if err != nil {
				return browserListedMsg{id: id, path: path, err: err}
			}
		}
		entries, err := ns.ReadDirEntries(ctx, f)
		if err != nil {
			return browserListedMsg{id: id, path: path, err: err}
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
			if e.Qid.IsDir() {
				names[i] += "/"
			}
		}
		sort.Strings(names)
		if path != "/" {
			names = append([]string{".."}, names...)
		}
		return browserListedMsg{id: id, path: path, names: names}
	}
}

// ---- view ----

func (m Model) View() tui.Node {
	// numbers is computed once per frame from the same paneOrder() Update
	// relies on for F1-F9 (see its input.KeyEvent case) — one source of
	// truth for "which pane is number N", not two traversals that could
	// drift apart.
	numbers := make(map[int]int, len(m.panes))
	for i, id := range m.paneOrder() {
		numbers[id] = i + 1
	}
	return tui.Box(layout.Vertical,
		tui.Child(layout.Length(1), m.controlStrip()),
		// true: m.root is always a Vertical split (New's own doc comment)
		// and is never itself a leaf, so this only ever matters for
		// root's direct children — matching reality, they can minimize.
		tui.Child(layout.Fill(1), m.renderSplit(m.root, numbers, true)),
		// Length(0): a widget.Modal's own assigned Rect is never used
		// (real drawing happens via PaintOverlay, a separate full-buffer
		// pass — see Modal's own doc comment), so this deliberately
		// takes no space in the normal Box flow; its Node just needs to
		// exist somewhere in the tree every frame for App to find it.
		tui.Child(layout.Length(0), widget.Modal(helpNode(), widget.ModalOptions{
			Theme:          m.theme,
			Title:          "Help",
			Open:           m.helpOpen,
			Width:          78,
			Height:         24,
			OnOutsideClick: func() tui.Msg { return closeHelpMsg{} },
		})),
	)
}

// renderSplit renders n recursively: a leaf becomes that pane's own
// Node (already stably keyed by paneNode itself, which now also draws
// that pane's own box-drawing frame — see its doc comment); an
// interior split becomes a Box along n.dir, one child per entry in
// n.children, keyed by n's own id — see splitNode's doc comment on why
// every level needs its own stable key, not just the leaves. numbers
// is View()'s once-per-frame pane-number map, threaded down so
// paneNode can show a "[F#]" label without each leaf re-walking the
// whole tree itself.
//
// canMinimize is whether n itself (if it turns out to be a leaf) sits
// along a Vertical split axis — minimize collapses a pane's Length(1)
// along whichever axis n.dir is; that's a sensible "show just the
// title row" for a vertical stack, but collapsing a horizontally-split
// pane's *width* to one column just garbles its title sideways with no
// readable result. So a horizontally-split pane simply can't be
// minimized at all: this is threaded down to paneNode (which disables
// the toggle entirely and drops the chevron when canMinimize is false)
// as well as gating the constraint decided right here — both need to
// agree, or a pane already minimized before being wrapped into a
// horizontal split would visually collapse anyway despite paneNode
// refusing to let it be (re-)minimized going forward. p.minimized
// itself is deliberately left untouched either way (not forced back to
// false) — purely cosmetic here, and would resume applying if this
// pane ever left the horizontal split (no such "unsplit" operation
// exists today, but no reason to lose the stored intent for one that
// might).
func (m Model) renderSplit(n *splitNode, numbers map[int]int, canMinimize bool) tui.Node {
	if n.paneID != 0 {
		return m.paneNode(m.find(n.paneID), numbers[n.paneID], canMinimize)
	}
	childCanMinimize := n.dir == layout.Vertical
	// No divider node inserted between children here (there was one,
	// briefly, 2026-08-29 — see git history if it's ever worth
	// resurrecting): every expanded pane now draws its own complete
	// box-drawing frame (see paneNode), so a separate inter-pane
	// divider line would just be a third, redundant line squeezed
	// between two panes' own borders.
	var children []tui.BoxChild
	for _, c := range n.children {
		children = append(children, tui.Child(m.childConstraint(c, childCanMinimize), m.renderSplit(c.node, numbers, childCanMinimize)))
	}
	return tui.Box(n.dir, children...).Key(splitKey(n.id))
}

// childConstraint decides how much of the parent split's axis c gets.
// Zoom (see toggleZoomMsg) takes priority over ordinary per-pane
// minimize when a pane is zoomed: every subtree that doesn't contain
// the zoomed pane collapses to Length(1) regardless of split axis or
// its own minimized state (unlike minimize, which only ever collapses
// along a Vertical axis — see renderSplit's own doc comment on why —
// zoom deliberately isn't axis-limited, since the point is to get a
// sibling out of the way as much as possible, not to keep it legibly
// glanceable), and the path down to the zoomed pane always gets
// Fill(1) so it actually reaches full size. Ordinary weighted/
// minimize-aware sizing only applies when nothing is zoomed.
func (m Model) childConstraint(c splitChild, canMinimize bool) layout.Constraint {
	if m.zoomedID != 0 {
		if subtreeContainsPane(c.node, m.zoomedID) {
			return layout.Fill(1)
		}
		// Length(0), not minimize's Length(1): zoom's whole point is to
		// fully hide a sibling, not leave it glanceable, and this
		// matters for more than aesthetics — see widget.Terminal.Paint's
		// width<=0||height<=0 early return, which is *why* minimize
		// (collapsing a pane's outer box to Length(1) with a title bar
		// that eats that one row, leaving content exactly 0 rows) never
		// destructively resizes a live pty. A Length(1) collapse along
		// zoom's own axis instead can leave the *other* dimension >0
		// (e.g. a Horizontal-split sibling: width=1, height untouched),
		// which misses that guard and genuinely (if correctly, matching
		// real terminal resize semantics) truncates content that
		// growing back afterward can't restore unless the hosted
		// program redraws itself — confirmed the hard way, via
		// TestZoomingAPaneKeepsSiblingProcessAlive initially failing
		// with exactly that symptom before this was Length(0).
		return layout.Length(0)
	}
	if c.node.paneID != 0 && canMinimize {
		if p := m.find(c.node.paneID); p != nil && p.minimized {
			return layout.Length(1)
		}
	}
	if c.weight <= 1 {
		// '-' resize floor (see resizePane/defaultPaneWeight): plain
		// Fill(1) has no absolute floor of its own — a sibling growing
		// enough via its own '+' could still squeeze this pane arbitrarily
		// small, well past readable. layout.Min(paneMinCells) behaves
		// identically to Fill(1) for the proportional-sharing part (per
		// its own doc comment), so this is a seamless transition, not a
		// jump — it just adds the hard floor Fill never had. Below
		// paneMinCells the answer is minimize (Length(1), title only),
		// not more '-' presses (reported 2026-08-30) — see paneMinCells's
		// own doc comment for why 3 is the right number.
		return layout.Min(paneMinCells)
	}
	return layout.Fill(c.weight)
}

// paneMinCells is '-' resize's absolute floor, in cells, along
// whichever axis is being squeezed — see childConstraint. An expanded
// pane's own frame (see paneNode) always spends 1 cell on a border/
// title row or column at each end, so the smallest size that still
// shows any actual content — one line, or one column, of it — is 1
// (top border/title) + 1 (content) + 1 (bottom border) along a
// Vertical squeeze, or symmetrically 1 (left border) + 1 (content) + 1
// (right border) along a Horizontal one. The same number works for
// both axes only because the frame is 1 cell thick on every side.
const paneMinCells = 3

// subtreeContainsPane reports whether paneID appears anywhere in n's
// subtree — see childConstraint.
func subtreeContainsPane(n *splitNode, paneID int) bool {
	if n == nil {
		return false
	}
	if n.paneID != 0 {
		return n.paneID == paneID
	}
	for _, c := range n.children {
		if subtreeContainsPane(c.node, paneID) {
			return true
		}
	}
	return false
}

func splitKey(id int) string { return fmt.Sprintf("split-%d", id) }

// controlStripFocusables is how many Tab-focusable widgets
// controlStrip contributes, ahead of any pane, in Tab order — kept
// beside controlStrip's own children by hand and used by
// InitialFocusAdvances, since tui ties Tab order to document/paint
// order with no independent override (see InitialFocusAdvances' doc
// comment for why that matters here). Update this if a button is
// added or removed above.
const controlStripFocusables = 8 // + shell, + kyu, + browse, + jobs, + history, help, theme, quit

func (m Model) controlStrip() tui.Node {
	return tui.Box(layout.Horizontal,
		tui.Child(layout.Length(10), m.addPaneButton("+ shell", ShellSpec("shell"))),
		tui.Child(layout.Length(8), m.addPaneButton("+ kyu", KyuReplSpec("kyu", m.env))),
		tui.Child(layout.Length(11), m.addPaneButton("+ browse", NamespaceBrowserSpec("browse", m.env))),
		tui.Child(layout.Length(9), m.addPaneButton("+ jobs", JobViewerSpec("jobs", m.env))),
		tui.Child(layout.Length(12), m.addPaneButton("+ history", SessionViewerSpec("history", m.sessionDir))),
		tui.Child(layout.Length(8), m.helpButton()),
		tui.Child(layout.Length(9), m.themeButton()),
		tui.Child(layout.Length(8), m.quitButton()),
		// barFill (not tui.Text("", ...), which paints nothing at all
		// for an empty string — Text's Paint only iterates the string's
		// own runes) carries the same background past the last button,
		// so the strip reads as one continuous bar across the pane's
		// full width, not just up to "quit". Never itself focusable, so
		// it only ever needs the unfocused variant.
		tui.Child(layout.Fill(1), barFill(m.controlStripStyle(false))),
	).Key("control-strip")
}

// barStyle is the background-filled style for a pane's own title bar:
// theme.Border (a muted, structural color) unfocused, theme.Focus
// when focused — see flatFocusableWidget.Paint, which fills its whole
// cell with this before drawing text on top, that's what actually
// makes it read as a bar instead of colored text floating on the
// terminal's own background. Foreground is left at the terminal's own
// default (cell.Color's zero value) rather than an explicit theme
// color, so text stays legible regardless of exactly which accent the
// terminal renders theme.Border/Focus as.
func (m Model) barStyle(focused bool) cell.Style {
	bg := m.theme.Border
	if focused {
		bg = m.theme.Focus
	}
	return cell.Style{Bg: bg, Attr: cell.AttrBold}
}

// controlStripStyle is the control strip's own bar color — deliberately
// distinct from both barStyle/theme.Border (an unfocused pane title/
// border) and theme.Focus (a focused one), so the always-visible
// top-level toolbar reads as a different, more prominent layer of
// chrome than any pane title, focused or not. Originally used
// theme.Primary for the base, which fixed the Border collision but not
// a second one: in tui/style's own default themes Primary and Focus
// are literally the same RGB value (both are "the accent color"), so a
// focused pane's title bar came out visually identical to the always-
// visible strip above it (reported 2026-08-30). theme.Secondary is a
// genuinely distinct hue from both Border and Focus/Primary, closing
// that gap; the reverse-video attribute flip on focus is unrelated and
// stays regardless — it's what makes the strip's *own* focused/
// unfocused states distinguishable from each other, a separate
// concern from distinguishing the strip from a pane title.
func (m Model) controlStripStyle(focused bool) cell.Style {
	st := cell.Style{Bg: m.theme.Secondary, Attr: cell.AttrBold}
	if focused {
		st.Attr |= cell.AttrReverse
	}
	return st
}

// InitialFocusAdvances is how many synthetic Tab presses cmd/9sh's
// runTUI should feed a freshly constructed tui.App (via HandleInput,
// before Run ever reads real input) so the shell starts with keyboard
// focus on the first pane's actual content instead of, as tui.App's
// zero-value focusIdx would otherwise leave it, the control strip's
// first button — a real usability bug found by driving a real build
// under a pty: a few keystrokes typed immediately after launch went
// nowhere, since nothing was listening for them yet.
//
// tui ties Tab order strictly to document/paint order (see
// reconcile.go's collectFocusables) with no separate "start focus
// here" hook, and moving the control strip or a pane's title bar
// later in the tree to fix this would move it later on screen too —
// so this instead replays N real Tab keypresses through the same
// public path a user's keyboard would use. Assumes exactly one pane
// at startup (true of every current cmd/9sh call site: one title bar
// beyond the control strip's own buttons) — not meant as a general
// "N panes" formula.
func InitialFocusAdvances() int {
	return controlStripFocusables + 1 // + the first pane's own title bar
}

func (m Model) addPaneButton(label string, spec Spec) tui.Node {
	return flatFocusable("btn-"+label, " "+label+" ", ' ', m.controlStripStyle,
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return addPaneMsg{spec: spec}
		})
}

// themeButton flips between tui/style's default Dark/Light themes at
// runtime — closes the gap New's own doc comment used to note as a
// real, accepted limitation ("a user whose terminal theme changes
// while 9sh is running would need to restart it to pick up a new
// one"): this doesn't change what 9sh detects from $COLORFGBG, it lets
// the user override that detection live instead.
// helpButton toggles the built-in help screen (see help.go) — a
// control-strip button rather than a global hotkey like '?' on
// purpose: '?' and every other plausible mnemonic key is a real,
// legitimately typeable character inside a kyu-repl pane's input, a
// shell's command line, or a namespace path in the browser, so
// claiming it globally (the same way tui.App delivers every key to
// both Update and the focused widget at once, with no way to suppress
// the latter — see this file's own top doc comment on F1-F9 for the
// one deliberate exception) would silently eat that character
// whenever any such pane has focus. Once the modal is actually open,
// though, widget.Modal claims focus exclusively for its own content
// (see FocusScope), so Esc/'?'/'q' *are* safe to bind there — see
// helpWidget.HandleEvent.
func (m Model) helpButton() tui.Node {
	return flatFocusable("help-btn", " help ", ' ', m.controlStripStyle,
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return toggleHelpMsg{}
		})
}

func (m Model) themeButton() tui.Node {
	return flatFocusable("theme-btn", " theme ", ' ', m.controlStripStyle,
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return toggleThemeMsg{}
		})
}

func (m Model) quitButton() tui.Node {
	return flatFocusable("quit-btn", " quit ", ' ', m.controlStripStyle,
		func(e input.Event) tui.Msg {
			if !clicked(e) {
				return nil
			}
			return quitRequestedMsg{}
		})
}

// clicked reports whether e is the "activate" gesture for a button-like
// control: Enter/Space from the keyboard, or a plain (non-drag) left
// click — the same me.Button == input.MouseLeft && !me.Drag idiom
// widget.TextInput/TextArea/Table use for their own click handling.
func clicked(e input.Event) bool {
	switch ev := e.(type) {
	case input.KeyEvent:
		return ev.Key == input.KeyEnter || ev.Rune == ' '
	case input.MouseEvent:
		return ev.Button == input.MouseLeft && !ev.Drag
	}
	return false
}

func (m Model) paneNode(p *paneState, number int, canMinimize bool) tui.Node {
	id := p.id

	// A pane along a horizontal split's axis can't be minimized at all —
	// collapsing its *width* to one column would garble the title
	// sideways with nothing readable, unlike a vertical stack's Length(1)
	// row (see renderSplit's own doc comment for the full reasoning).
	// The chevron drops to a blank placeholder rather than showing a
	// (now non-functional) ▾/▸ — p.minimized's stored value is left
	// alone regardless, purely a display decision here.
	chevron := "  "
	if canMinimize {
		chevron = "▾ "
		if p.minimized {
			chevron = "▸ "
		}
	}
	label := chevron + paneLabel(p)
	if number >= 1 && number <= 9 {
		// Matches fKeyPaneNumber's own F1-F9 cap — a pane past the 9th
		// just doesn't get a jump hotkey or a label for one.
		label = fmt.Sprintf("[F%d] ", number) + label
	}
	if m.zoomedID == id {
		label += " [zoomed]"
	}
	if p.exited {
		label += " (exited)"
	}
	if p.awaitingSplitKind {
		// Overrides the normal hint entirely while awaiting the second
		// keypress of the two-step split flow — see splitKindKey for what
		// each letter means; anything else (including this same title
		// bar's own x/d/r/z/+/-, deliberately) cancels via cancelSplitMsg.
		label += "  split: s=shell k=kyu b=browse j=jobs h=history (else cancel)"
	} else {
		// Kept terse, not "(x close, d/r split, z zoom, +/- resize)":
		// once a title bar is one of several side by side after a
		// horizontal split, or carries an "[F#]" jump-hotkey prefix, the
		// available width per pane shrinks fast — see
		// TestSplitKeysOnTitleBar's 60-col/2-pane case, which is exactly
		// narrow enough to clip a longer hint.
		label += "  (x/d/r/z/+/-)"
	}
	// collapsed matches exactly the condition renderSplit uses to give
	// this pane's own outer slot Length(1) — a full border needs at
	// least 2 rows, and a minimized pane only ever gets 1, so these are
	// two genuinely different renderings (a flat single-row strip vs. a
	// bordered box), not one shrunk version of the other. The title
	// bar's own fill rune tracks this too: ' ' for the flat strip
	// (unchanged from before borders existed), '─' when it doubles as a
	// bordered pane's top border line.
	collapsed := canMinimize && p.minimized
	titleFill := ' '
	if !collapsed {
		titleFill = '─'
	}
	titleBar := flatFocusable(paneKey(id, "title"), label, titleFill,
		// focused (this exact widget) is OR'd with paneHasFocus(id) (any
		// widget in this pane) rather than replaced by it: paneHasFocus
		// reflects the *previous* render's FocusAware report (see its
		// own doc comment on the one-frame lag), while focused is this
		// same frame's real answer for the title row specifically — the
		// OR keeps the title row itself always accurate immediately, and
		// only leans on the one-frame-behind signal for its sibling
		// content widget.
		func(focused bool) cell.Style { return m.titleStyle(p, focused || m.paneHasFocus(id)) },
		// Every key here is scoped to the title bar specifically, never
		// a global hotkey, for the same reason minimize already was:
		// tui.App delivers every key to both Update and the focused
		// widget at once, with no way to suppress the latter, and a
		// Terminal pane's hosted shell needs all of these (and anything
		// else) for its own use. click/Enter/Space still toggle
		// minimize, checked last so none of the others shadow it.
		func(e input.Event) tui.Msg {
			if ke, ok := e.(input.KeyEvent); ok {
				if p.awaitingSplitKind {
					if kind, ok := splitKindKey(ke.Rune); ok {
						return splitPaneMsg{id: id, dir: p.awaitingSplitDir, spec: defaultSpecForKind(kind, m.env, m.sessionDir)}
					}
					return cancelSplitMsg{id: id}
				}
				switch ke.Rune {
				case 'x':
					return closePaneMsg{id: id}
				case 'd':
					return beginSplitMsg{id: id, dir: layout.Vertical} // split down, then pick a kind
				case 'r':
					return beginSplitMsg{id: id, dir: layout.Horizontal} // split right, then pick a kind
				case 'z':
					return toggleZoomMsg{id: id}
				case '+', '=':
					return resizePaneMsg{id: id, delta: 1}
				case '-':
					return resizePaneMsg{id: id, delta: -1}
				}
			}
			if !canMinimize || !clicked(e) {
				return nil
			}
			return toggleMinimizeMsg{id: id}
		})

	var content tui.Node
	switch p.kind {
	case KindKyuRepl:
		content = kyuReplNode(id, p.env)
	case KindNamespaceBrowser:
		content = browserNode(p)
	case KindJobViewer:
		content = jobViewerNode(p)
	case KindSessionViewer:
		content = sessionViewerNode(p)
	default: // KindShell
		content = widget.Terminal(widget.TerminalOptions{
			Command: p.command,
			OnExit:  func(err error) tui.Msg { return paneExitedMsg{id: id, err: err} },
			// A pane hosts a real shell — tab-completion has to work, so
			// Tab must reach it rather than being intercepted for focus
			// navigation. ReleaseKey is left at its default (Ctrl+\), the
			// way out to Tab-navigate title bars/buttons again.
			WantsRawTab: true,
		}).Key(paneKey(id, "term"))
	}

	if collapsed {
		return tui.Box(layout.Vertical,
			tui.Child(layout.Length(1), titleBar),
			tui.Child(layout.Fill(1), content),
		).Key(paneKey(id, "box"))
	}

	// Expanded: a full box-drawing frame (┌─ title ─┐ / │ content │ /
	// └────────┘), title text embedded in the top border line (titleBar
	// itself, filled with '─' above) rather than a separate row — see
	// this file's top doc comment on why per-pane framing was chosen
	// over a shared-grid-with-junctions design (tui has no post-paint
	// compositing hook to pick correct ┬┴├┤┼ glyphs at nested split
	// boundaries; each pane owning its complete, independent frame
	// needs nothing beyond ordinary Box composition). Reuses divider's
	// same rune-filled-rect widget for the corners and the plain
	// (non-interactive) sides/bottom — the same widget this package
	// already used for the inter-pane divider before every pane grew
	// its own border and that divider became redundant (removed from
	// renderSplit; see its own doc comment).
	borderStyle := cell.Style{Bg: m.theme.Border}
	corner := func(part string, r rune) tui.Node { return divider(paneKey(id, part), borderStyle, r) }
	return tui.Box(layout.Vertical,
		tui.Child(layout.Length(1), tui.Box(layout.Horizontal,
			tui.Child(layout.Length(1), corner("tl", '┌')),
			tui.Child(layout.Fill(1), titleBar),
			tui.Child(layout.Length(1), corner("tr", '┐')),
		).Key(paneKey(id, "topborder"))),
		tui.Child(layout.Fill(1), tui.Box(layout.Horizontal,
			tui.Child(layout.Length(1), divider(paneKey(id, "left"), borderStyle, '│')),
			tui.Child(layout.Fill(1), content),
			tui.Child(layout.Length(1), divider(paneKey(id, "right"), borderStyle, '│')),
		).Key(paneKey(id, "middle"))),
		tui.Child(layout.Length(1), tui.Box(layout.Horizontal,
			tui.Child(layout.Length(1), corner("bl", '└')),
			tui.Child(layout.Fill(1), divider(paneKey(id, "bottom"), borderStyle, '─')),
			tui.Child(layout.Length(1), corner("br", '┘')),
		).Key(paneKey(id, "botborder"))),
	).Key(paneKey(id, "box"))
}

func paneLabel(p *paneState) string {
	if p.kind == KindNamespaceBrowser {
		return p.title + ": " + p.browserPath
	}
	return p.title
}

func paneKey(id int, part string) string {
	return fmt.Sprintf("pane-%d-%s", id, part)
}

// splitKindKey maps a title bar's second split-flow keypress to the
// kind it requests — mnemonic first letters, matching the control
// strip's own "+ shell"/"+ kyu"/"+ browse"/"+ jobs"/"+ history" order.
// Any other rune isn't recognized (Update's caller falls back to
// cancelSplitMsg in that case, not this function's business).
func splitKindKey(r rune) (Kind, bool) {
	switch r {
	case 's':
		return KindShell, true
	case 'k':
		return KindKyuRepl, true
	case 'b':
		return KindNamespaceBrowser, true
	case 'j':
		return KindJobViewer, true
	case 'h':
		return KindSessionViewer, true
	}
	return 0, false
}

// defaultSpecForKind builds the Spec a split-created pane of kind gets
// — the same convenience constructors (ShellSpec etc.) and default
// titles the control strip's own "+" buttons use, so a split-created
// pane is indistinguishable from a top-level one apart from where it
// landed in the tree.
func defaultSpecForKind(kind Kind, env *eval.Env, sessionDir string) Spec {
	switch kind {
	case KindShell:
		return ShellSpec("shell")
	case KindNamespaceBrowser:
		return NamespaceBrowserSpec("browse", env)
	case KindJobViewer:
		return JobViewerSpec("jobs", env)
	case KindSessionViewer:
		return SessionViewerSpec("history", sessionDir)
	default:
		return KyuReplSpec("kyu", env)
	}
}

func (m Model) titleStyle(p *paneState, focused bool) cell.Style {
	if p.exited {
		return cell.Style{Bg: m.theme.Error, Attr: cell.AttrBold}
	}
	return m.barStyle(focused)
}

// SetFocusedKey implements tui.FocusAware (tui v0.5.0+): App.render()
// calls this immediately before View() with the Node.Key of whichever
// focusable widget currently holds keyboard focus (nil if none/unkeyed,
// or focus is inside an active FocusScope). Stored through focusedKey's
// shared pointer — see that field's own doc comment for why a plain
// value-receiver field write wouldn't stick.
//
// A non-string key (nil, or any future non-string Key value) clears it
// to "", never mistaken for a real pane key since paneKey never
// produces an empty string.
func (m Model) SetFocusedKey(key any) {
	if m.focusedKey == nil { // only a bare Model{} bypassing New would hit this
		return
	}
	s, _ := key.(string)
	*m.focusedKey = s
}

// paneHasFocus reports whether the most recently reported FocusAware
// key (see SetFocusedKey) belongs to pane id — true while focus is on
// any of that pane's own focusables (its content included), not only
// while its title bar specifically is the literal tab-focused widget.
// This is what lets a pane's title bar stay highlighted across a Tab
// press that moves focus from the title row onto the pane's own
// content, instead of flickering unfocused the instant you're no
// longer on the title row itself.
//
// Prefix-matches against paneKey(id, "") ("pane-<id>-"): safe against
// an id-prefix collision (pane 1 vs pane 10) since paneKey always
// inserts a literal "-" right after id, so "pane-1-" is never a prefix
// of "pane-10-...".
func (m Model) paneHasFocus(id int) bool {
	if m.focusedKey == nil {
		return false
	}
	return strings.HasPrefix(*m.focusedKey, paneKey(id, ""))
}
