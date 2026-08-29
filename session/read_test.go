package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeHistoryFile(t *testing.T, dir, name string, recs []Record) {
	t.Helper()
	histDir := filepath.Join(dir, historyDirName)
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
	if err := os.WriteFile(filepath.Join(histDir, name), b, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReadRecentMissingDirIsEmptyNotError(t *testing.T) {
	recs, err := ReadRecent(t.TempDir(), 10)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 0 {
		t.Fatalf("recs = %v, want none", recs)
	}
}

func TestReadRecentOrdersNewestFirstAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "2026-08-27.nrl", []Record{
		{JobID: 1, Argv: []string{"first"}},
		{JobID: 2, Argv: []string{"second"}},
	})
	writeHistoryFile(t, dir, "2026-08-28.nrl", []Record{
		{JobID: 3, Argv: []string{"third"}},
		{JobID: 4, Argv: []string{"fourth"}},
	})

	recs, err := ReadRecent(dir, 10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	wantOrder := []int{4, 3, 2, 1}
	if len(recs) != len(wantOrder) {
		t.Fatalf("got %d records, want %d", len(recs), len(wantOrder))
	}
	for i, want := range wantOrder {
		if recs[i].JobID != want {
			t.Fatalf("recs[%d].JobID = %d, want %d (full: %v)", i, recs[i].JobID, want, recs)
		}
	}
}

func TestReadRecentRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "2026-08-28.nrl", []Record{
		{JobID: 1}, {JobID: 2}, {JobID: 3}, {JobID: 4}, {JobID: 5},
	})
	recs, err := ReadRecent(dir, 2)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("recs = %v, want 2", recs)
	}
	if recs[0].JobID != 5 || recs[1].JobID != 4 {
		t.Fatalf("recs = %v, want newest two (5, 4)", recs)
	}
}

func TestReadRecentDecodesFields(t *testing.T) {
	dir := t.TempDir()
	exit := 0
	rec := Record{
		TSStart: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
		TSEnd:   time.Date(2026, 8, 28, 10, 0, 1, 0, time.UTC),
		Host:    "myhost",
		JobID:   42,
		Cwd:     "/tmp",
		Argv:    []string{"echo", "hi"},
		Exit:    &exit,
		Kind:    "subprocess",
	}
	writeHistoryFile(t, dir, "2026-08-28.nrl", []Record{rec})

	recs, err := ReadRecent(dir, 10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("recs = %v, want 1", recs)
	}
	got := recs[0]
	if got.Host != "myhost" || got.JobID != 42 || got.Cwd != "/tmp" || got.Kind != "subprocess" {
		t.Fatalf("got = %+v, want fields to match %+v", got, rec)
	}
	if got.Exit == nil || *got.Exit != 0 {
		t.Fatalf("Exit = %v, want pointer to 0", got.Exit)
	}
}
