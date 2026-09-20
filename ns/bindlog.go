package ns

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// maxLogEntries bounds the history a Namespace keeps. Binds are rare
// and hand-typed, so this is generous, but a script that rebinds in a
// loop shouldn't grow the shell without limit; the oldest entries go
// first and the text form says how many were dropped.
const maxLogEntries = 1000

// LogEntry is one successful bind or unbind, as the caller gave it —
// unlike Bind (the current state, canonicalized), this keeps the
// original disposition, which is the one thing state can't recover.
type LogEntry struct {
	Seq  uint64 `json:"seq"`
	Time string `json:"time"` // RFC3339, UTC
	Op   string `json:"op"`   // "bind" or "unbind"
	Dst  string `json:"dst"`
	// Src is the bind's source expression — "/a + /b" for a union
	// source, `dial("h:1")` for a mount — or "" for an unbind and for a
	// Go-bootstrap bind with no kyu spelling.
	Src  string `json:"src"`
	Disp string `json:"disp"` // as passed to bind; "" for an unbind
	RO   bool   `json:"ro"`   // bound with the ro flag
}

type bindLog struct {
	mu      sync.Mutex
	entries []LogEntry
	dropped uint64
	now     func() time.Time // nil = time.Now; tests replace it
}

func (l *bindLog) record(op, dst, src string, disp Disposition, ro bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	e := LogEntry{
		Seq:  l.dropped + uint64(len(l.entries)) + 1,
		Time: now().UTC().Format(time.RFC3339),
		Op:   op,
		Dst:  "/" + strings.Join(splitPath(dst), "/"),
		Src:  src,
		RO:   ro,
	}
	if op == "bind" {
		e.Disp = disp.String()
	}
	l.entries = append(l.entries, e)
	if len(l.entries) > maxLogEntries {
		drop := len(l.entries) - maxLogEntries
		l.entries = append([]LogEntry(nil), l.entries[drop:]...)
		l.dropped += uint64(drop)
	}
}

// Log returns every recorded bind and unbind, oldest first, and how many
// older entries were dropped to stay under the cap. Only operations that
// succeeded appear: a failed bind changed nothing.
func (ns *Namespace) Log() (entries []LogEntry, dropped uint64) {
	ns.log.mu.Lock()
	defer ns.log.mu.Unlock()
	return append([]LogEntry(nil), ns.log.entries...), ns.log.dropped
}

// FormatLog renders a log as replayable kyu, one statement per entry
// with the time as a trailing comment, so `source(/ns/log)` re-runs the
// history with each bind's original disposition. Entries with no kyu
// spelling (bootstrap binds) are whole-line comments, like FormatBinds.
func FormatLog(entries []LogEntry, dropped uint64) string {
	var b strings.Builder
	if dropped > 0 {
		fmt.Fprintf(&b, "# %d earlier entries dropped\n", dropped)
	}
	for _, e := range entries {
		switch {
		case e.Op == "unbind":
			fmt.Fprintf(&b, "unbind %s", e.Dst)
		case e.Src == "":
			fmt.Fprintf(&b, "# bind <builtin>, %s%s", e.Dst, bindSuffix(e.Disp, e.RO))
		default:
			fmt.Fprintf(&b, "bind %s, %s%s", e.Src, e.Dst, bindSuffix(e.Disp, e.RO))
		}
		fmt.Fprintf(&b, "  # %s\n", e.Time)
	}
	return b.String()
}

// bindSuffix is the optional ", after" / ", ro" tail of a bind statement:
// replace is the default disposition and is never spelled out.
func bindSuffix(disp string, ro bool) string {
	var s string
	if disp != "" && disp != "replace" {
		s += ", " + disp
	}
	if ro {
		s += ", ro"
	}
	return s
}
