package eval

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
)

// runExternal evaluates a `%cmd arg...` external/legacy-binary call. in is
// the piped-in value (nil if %cmd is used bare, with no pipe input) and is
// rendered to bytes for the subprocess's stdin — see renderForExternal.
//
// A process that starts but exits non-zero is not an error here: exit
// codes are ordinary shell-level data (grep's "no match" convention, etc),
// so stdout is still returned. Only a failure to start the process at all
// (bad command name, no permission) becomes a value.ErrorVal — an
// in-stream failure, per kyu's error model, not a hard Go-level abort.
func runExternal(x *ast.ExternalCall, in value.Value, env *Env) (value.Value, error) {
	args := make([]string, len(x.Args))
	for i, a := range x.Args {
		v, err := evalExpr(a, env)
		if err != nil {
			return nil, err
		}
		s, err := argString(v)
		if err != nil {
			return nil, fmt.Errorf("%%%s: argument %d: %w", x.Name, i, err)
		}
		args[i] = s
	}

	cmd := exec.Command(x.Name, args...)
	if in != nil {
		cmd.Stdin = bytes.NewReader(renderForExternal(in))
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("%%%s: %v", x.Name, err)}, nil
	}
	_ = cmd.Wait() // non-zero exit is ordinary data, not a Go-level error here

	return value.Bytes(stdout.Bytes()), nil
}

func argString(v value.Value) (string, error) {
	switch x := v.(type) {
	case value.String:
		return string(x), nil
	case value.Path:
		return string(x), nil
	case value.Int, value.Float, value.Bool, value.Duration:
		return x.String(), nil
	case value.Bytes:
		return string(x), nil
	default:
		return "", fmt.Errorf("cannot use a %s as a command argument", v.Kind())
	}
}

// renderForExternal converts a piped-in kyu value to bytes for a legacy
// process's stdin. Bytes/String pass through raw; a Table (a List of
// Records) renders one tab-separated line per row; this is a deliberately
// simple placeholder — the real NRL/NRF wire format lands with the
// structured-record serialization work, not required for Phase 1.
func renderForExternal(v value.Value) []byte {
	switch x := v.(type) {
	case value.Bytes:
		return []byte(x)
	case value.String:
		return []byte(string(x) + "\n")
	case *value.List:
		var buf bytes.Buffer
		for _, row := range x.Elems {
			if rec, ok := row.(*value.Record); ok {
				for i, k := range rec.Keys() {
					if i > 0 {
						buf.WriteByte('\t')
					}
					fv, _ := rec.Get(k)
					buf.WriteString(fv.String())
				}
			} else {
				buf.WriteString(row.String())
			}
			buf.WriteByte('\n')
		}
		return buf.Bytes()
	default:
		return []byte(x.String() + "\n")
	}
}
