package pane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/session"
)

func writeSessionHistory(t *testing.T, dir string, recs []session.Record) {
	t.Helper()
	histDir := filepath.Join(dir, "history")
	if err := os.MkdirAll(histDir, 0755); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	if err := os.WriteFile(filepath.Join(histDir, "2026-08-29.nrl"), b, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionViewerListsHistory(t *testing.T) {
	dir := t.TempDir()
	exit := 0
	writeSessionHistory(t, dir, []session.Record{
		{Host: "myhost", JobID: 1, Argv: []string{"echo", "hi"}, Exit: &exit, Kind: "subprocess"},
	})

	m := New(nil, dir, SessionViewerSpec("history", dir))
	id := m.panes[0].id

	msg := listSessionCmd(id, dir)()
	next, _ := m.Update(msg)
	m = next.(Model)

	if len(m.panes[0].sessionRows) != 1 {
		t.Fatalf("sessionRows = %v, want 1 row", m.panes[0].sessionRows)
	}
	row := m.panes[0].sessionRows[0]
	if !strings.Contains(row, "myhost") || !strings.Contains(row, "echo hi") || !strings.Contains(row, "exit:0") {
		t.Fatalf("row = %q, missing expected content", row)
	}
	if m.panes[0].sessionErr != "" {
		t.Fatalf("sessionErr = %q, want empty", m.panes[0].sessionErr)
	}
}

func TestSessionViewerEmptyDirIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	m := New(nil, dir, SessionViewerSpec("history", dir))
	id := m.panes[0].id

	msg := listSessionCmd(id, dir)()
	next, _ := m.Update(msg)
	m = next.(Model)

	if m.panes[0].sessionErr != "" {
		t.Fatalf("sessionErr = %q, want empty for a dir with no history yet", m.panes[0].sessionErr)
	}
	if len(m.panes[0].sessionRows) != 0 {
		t.Fatalf("sessionRows = %v, want none", m.panes[0].sessionRows)
	}
}

func TestSessionViewerEmptySessionDirIsReportedAsError(t *testing.T) {
	m := New(nil, "", SessionViewerSpec("history", ""))
	id := m.panes[0].id

	msg := listSessionCmd(id, "")()
	next, _ := m.Update(msg)
	m = next.(Model)

	if m.panes[0].sessionErr == "" {
		t.Fatal("expected an error for an empty session dir (session history unavailable at startup)")
	}
}
