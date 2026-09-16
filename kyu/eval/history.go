package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biHistory implements `history()`: the current session's REPL recall
// history (Up/Down, Ctrl-R) as a Table (index, text), pipeable like
// vars()/ps() — index is what history_delete(index) takes. TUI-only:
// the plain -repl/script entry points are a bare line reader with no
// recall history at all, so this hard-errors there rather than
// silently returning an empty list, matching source_config's own "the
// concept doesn't apply in this environment" convention.
func biHistory(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("history: expected no arguments, got %d", len(args))
	}
	access := env.HistoryAccess()
	if access == nil || access.List == nil {
		return nil, fmt.Errorf("history: not available in this environment")
	}
	entries := access.List()
	elems := make([]value.Value, len(entries))
	for i, text := range entries {
		rec := value.NewRecord()
		rec.Set("index", value.Int(i))
		rec.Set("text", value.String(text))
		elems[i] = rec
	}
	return value.NewList(elems), nil
}

// biHistoryDelete implements `history_delete(index)`: removes one entry
// by the index history() reported it at — unset()'s own "reports
// whether one existed" convention, not an ErrorVal, since an
// out-of-range index is closer to unset("nonexistent") than a real
// failure.
func biHistoryDelete(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("history_delete: expected 1 argument (an index), got %d", len(args))
	}
	idx, ok := args[0].(value.Int)
	if !ok {
		return nil, fmt.Errorf("history_delete: expected an int index, got %s", args[0].Kind())
	}
	access := env.HistoryAccess()
	if access == nil || access.Delete == nil {
		return nil, fmt.Errorf("history_delete: not available in this environment")
	}
	return value.Bool(access.Delete(int(idx))), nil
}

// biHistoryClear implements `history_clear()`: removes every entry —
// Ctrl+L's own transcript-clear deliberately leaves history untouched
// (see replui/help.go), so this is the explicit, separate way to clear
// history too.
func biHistoryClear(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("history_clear: expected no arguments, got %d", len(args))
	}
	access := env.HistoryAccess()
	if access == nil || access.Clear == nil {
		return nil, fmt.Errorf("history_clear: not available in this environment")
	}
	access.Clear()
	return value.Null{}, nil
}
