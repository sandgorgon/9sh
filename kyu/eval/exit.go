package eval

import (
	"errors"
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// ExitSignal is the sentinel error exit()/quit() return to unwind out of
// whatever is running — the same "ordinary Go error propagates up until
// something catches it" mechanism breakSignal uses for a loop, caught
// one level further out: by whatever started this run of kyu. source()
// and the startup files (config.Load/dotfiles.Load) stop just that file;
// cmd/9sh stops a script or ends the plain -repl loop; replui quits the
// TUI; a background `&` job just ends. Exported so those callers outside
// this package can recognize it with IsExit/AsExit.
//
// Code and Reason only mean something to a script run as
// `9sh script.ky`, where they become the process's exit status and a
// line on stderr — see biExit. Everywhere else they're carried along
// and ignored, so a file behaves the same whether it's run as a script
// or sourced.
type ExitSignal struct {
	Code   int
	Reason string
}

func (ExitSignal) Error() string { return "exit" }

// IsExit reports whether err is (or wraps) an ExitSignal.
func IsExit(err error) bool {
	_, ok := AsExit(err)
	return ok
}

// AsExit returns the ExitSignal err is (or wraps), if any.
func AsExit(err error) (ExitSignal, bool) {
	var sig ExitSignal
	ok := errors.As(err, &sig)
	return sig, ok
}

// biExit implements both `exit()` and `quit()`, which are the same
// thing under two names:
//
//   - exit() — success, status 0.
//   - exit(n) — status n, for Unix callers that test numbers. Limited to
//     0..255, what a Unix exit status can hold; anything else is an
//     error rather than letting the OS wrap it (256 would read as 0,
//     a failure passing as success).
//   - exit("reason") — Plan 9's exits() convention: an empty string is
//     success, anything else is a failure whose string is the reason.
//     A Unix status can't carry a string, so it becomes status 1 with
//     the reason printed to stderr.
func biExit(name string, args []value.Value) (value.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("%s: expected at most 1 argument, got %d", name, len(args))
	}
	if len(args) == 0 {
		return nil, ExitSignal{}
	}
	switch a := args[0].(type) {
	case value.Int:
		if a < 0 || a > 255 {
			return nil, fmt.Errorf("%s: status %d out of range (0 to 255)", name, a)
		}
		return nil, ExitSignal{Code: int(a)}
	case value.String:
		if a == "" {
			return nil, ExitSignal{}
		}
		return nil, ExitSignal{Code: 1, Reason: string(a)}
	}
	return nil, fmt.Errorf("%s: expected an int status or a string reason, got %s", name, args[0].Kind())
}
