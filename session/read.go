package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadRecent reads up to limit of the most recent history records from
// dir (the session repo root, e.g. ~/.config/9/session), newest first,
// reading however many of the most recent day-sharded files under
// history/ are needed to gather that many. dir having no history yet —
// a fresh install, or one where session history never got set up at
// all — is not an error, just an empty result: a viewer pane showing
// "no history yet" isn't a failure state.
func ReadRecent(dir string, limit int) ([]Record, error) {
	histDir := filepath.Join(dir, historyDirName)
	entries, err := os.ReadDir(histDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Filenames are YYYY-MM-DD.nrl (see dayShard), so a plain
	// descending lexical sort is also a descending chronological one.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })

	var out []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".nrl") {
			continue
		}
		recs, err := readRecordsFile(filepath.Join(histDir, e.Name()))
		if err != nil {
			return out, err // return whatever's already gathered alongside the error
		}
		// A day's file is oldest-first (append-only); walk it backwards
		// so the overall result stays newest-first across files too.
		for i := len(recs) - 1; i >= 0; i-- {
			out = append(out, recs[i])
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func readRecordsFile(path string) ([]Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recs []Record
	for line := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return recs, fmt.Errorf("%s: %w", path, err)
		}
		recs = append(recs, rec)
	}
	return recs, nil
}
