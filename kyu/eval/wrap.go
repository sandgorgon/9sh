package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// biWrap implements `wrap()` / `wrap(on)`: reports or sets whether the
// REPL wraps long lines (Alt+Z's toggle), for when Alt+Z doesn't reach
// 9sh at all — a terminal or desktop shortcut that claims it first
// (macOS Option typing a character, the NVIDIA overlay on Windows).
// Display only, like Alt+Z: it never changes a result. TUI-only, the
// same "not available in this environment" hard error history() uses,
// since the plain -repl/script entry points draw no screen to wrap.
func biWrap(env *Env, args []value.Value) (value.Value, error) {
	access := env.WrapAccess()
	if access == nil || access.Get == nil || access.Set == nil {
		return nil, fmt.Errorf("wrap: not available in this environment")
	}
	switch len(args) {
	case 0:
		return value.Bool(access.Get()), nil
	case 1:
		on, ok := args[0].(value.Bool)
		if !ok {
			return nil, fmt.Errorf("wrap: expected a bool, got %s", args[0].Kind())
		}
		access.Set(bool(on))
		return value.Null{}, nil
	}
	return nil, fmt.Errorf("wrap: expected 0 or 1 arguments, got %d", len(args))
}
