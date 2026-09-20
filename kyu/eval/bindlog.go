package eval

import (
	"context"
	"encoding/json"
	"fmt"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biBindLog implements `bind_log()`: every successful bind and unbind so
// far, oldest first, as a Table of Records (seq, time, op, dst, src,
// disp) — the structured view of /ns/log.json, the way binds() is of
// /ns/binds.json. Where binds() reports what's bound now (with a
// canonical disposition), this keeps what was actually typed, including
// the before/after a later bind has since spliced around and every
// unbind. src is Null for an unbind and for a bootstrap bind; disp is
// Null for an unbind. The log is capped (see ns's maxLogEntries); the
// oldest entries drop first.
func biBindLog(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("bind_log: expected no arguments, got %d", len(args))
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("bind_log: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	f, err := openFile(ctx, root, p9.OREAD, "ns", "log.json")
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("bind_log: %v", err)}, nil
	}
	defer f.Close()
	b, err := readAllFile(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("bind_log: %w", err)
	}
	var doc struct {
		Entries []ns.LogEntry `json:"entries"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("bind_log: %w", err)
	}
	out := make([]value.Value, len(doc.Entries))
	for i, e := range doc.Entries {
		r := value.NewRecord()
		r.Set("seq", value.Int(e.Seq))
		r.Set("time", value.String(e.Time))
		r.Set("op", value.String(e.Op))
		r.Set("dst", value.Path(e.Dst))
		r.Set("src", nullIfEmpty(e.Src))
		r.Set("disp", nullIfEmpty(e.Disp))
		out[i] = r
	}
	return value.NewList(out), nil
}

func nullIfEmpty(s string) value.Value {
	if s == "" {
		return value.Null{}
	}
	return value.String(s)
}
