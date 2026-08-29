package pane

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/tui"
)

func skipUnlessOnPath(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

// ---- pure Model.Update/View logic — no tui.App needed ----

func TestNewSeedsPanes(t *testing.T) {
	m := New(nil, "", ShellSpec("a"), ShellSpec("b"))
	if len(m.panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(m.panes))
	}
	if m.panes[0].id == m.panes[1].id {
		t.Fatal("panes got the same id")
	}
}

func TestToggleMinimizeFlipsState(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	if m.panes[0].minimized {
		t.Fatal("new pane should start expanded")
	}
	next, _ := m.Update(toggleMinimizeMsg{id: id})
	m = next.(Model)
	if !m.panes[0].minimized {
		t.Fatal("first toggle should minimize")
	}
	next, _ = m.Update(toggleMinimizeMsg{id: id})
	m = next.(Model)
	if m.panes[0].minimized {
		t.Fatal("second toggle should restore")
	}
}

func TestToggleMinimizeUnknownIDIsNoOp(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, cmd := m.Update(toggleMinimizeMsg{id: 999})
	if cmd != nil {
		t.Fatal("unknown id should not produce a Cmd")
	}
	m2 := next.(Model)
	if m2.panes[0].minimized {
		t.Fatal("unknown id toggle should not affect any real pane")
	}
}

func TestAddPaneMsgAppendsPane(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	next, _ := m.Update(addPaneMsg{spec: ShellSpec("b")})
	m = next.(Model)
	if len(m.panes) != 2 {
		t.Fatalf("got %d panes, want 2", len(m.panes))
	}
	if m.panes[1].title != "b" {
		t.Fatalf("new pane title = %q, want b", m.panes[1].title)
	}
}

func TestPaneExitedMsgMarksExited(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	id := m.panes[0].id
	next, _ := m.Update(paneExitedMsg{id: id})
	m = next.(Model)
	if !m.panes[0].exited {
		t.Fatal("pane should be marked exited")
	}
}

func TestQuitRequestedProducesQuitCmd(t *testing.T) {
	m := New(nil, "", ShellSpec("a"))
	_, cmd := m.Update(quitRequestedMsg{})
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd")
	}
	if _, ok := cmd().(tui.QuitMsg); !ok {
		t.Fatalf("Cmd produced %T, want tui.QuitMsg", cmd())
	}
}

func TestClickedRecognizesEnterSpaceAndLeftClick(t *testing.T) {
	cases := []struct {
		name string
		e    input.Event
		want bool
	}{
		{"enter", input.KeyEvent{Key: input.KeyEnter}, true},
		{"space", input.KeyEvent{Rune: ' '}, true},
		{"other key", input.KeyEvent{Rune: 'x'}, false},
		{"left click", input.MouseEvent{Button: input.MouseLeft}, true},
		{"drag", input.MouseEvent{Button: input.MouseLeft, Drag: true}, false},
		{"release", input.MouseEvent{Button: input.MouseRelease}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clicked(c.e); got != c.want {
				t.Errorf("clicked(%v) = %v, want %v", c.e, got, c.want)
			}
		})
	}
}

// ---- integration: real tui.App + widget.Terminal, the part with
// actual keying-correctness risk (see model.go's package doc) ----

func TestMinimizeKeepsProcessAliveAndStatePreserved(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	cmd := exec.Command("sh", "-c", "echo READY; read x; echo GOT:$x")
	m := New(nil, "", Spec{Title: "test", Command: cmd})
	id := m.panes[0].id

	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	waitForText(t, app, "READY", 3*time.Second)

	app.Dispatch(toggleMinimizeMsg{id: id})
	forceRenders(app, 3)
	if strings.Contains(app.Buffer().String(), "READY") {
		t.Fatal("minimized pane should not occupy screen space")
	}

	app.Dispatch(toggleMinimizeMsg{id: id})
	waitForText(t, app, "READY", 3*time.Second)

	if strings.Contains(app.Buffer().String(), "failed to start") {
		t.Fatal("pane was disposed and recreated across minimize/restore — the running process was killed")
	}
}

func TestExitedPaneShowsIndicatorAfterEvent(t *testing.T) {
	skipUnlessOnPath(t, "true")
	cmd := exec.Command("true")
	m := New(nil, "", Spec{Title: "test", Command: cmd})

	app := tui.NewApp(m, 40, 10)
	defer app.Close()

	// OnExit fires opportunistically from HandleEvent (see
	// widget.Terminal's doc comment) — repeated no-op renders are
	// enough to eventually observe the exited state once the process
	// has actually exited and a render happens to run after that.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		app.Dispatch(struct{}{})
		if strings.Contains(app.Buffer().String(), "exited") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("exited indicator never appeared:\n%s", app.Buffer().String())
}

func forceRenders(app *tui.App, n int) {
	for range n {
		app.Dispatch(struct{}{})
	}
}

func waitForText(t *testing.T, app *tui.App, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		app.Dispatch(struct{}{})
		if strings.Contains(app.Buffer().String(), substr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in buffer:\n%s", substr, app.Buffer().String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
