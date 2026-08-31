package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
)

// runExternal evaluates a `%cmd arg...` external/legacy-binary call. in is
// the piped-in value (nil if %cmd is used bare, with no pipe input) and is
// rendered to bytes for the subprocess's stdin — see renderForExternal.
//
// When a namespace is attached (see Env.Namespace), this routes through
// /jobs the same way `&`-backgrounded jobs do (evalBackground), just
// waiting for the result instead of returning immediately — matching the
// design doc's "every command, foreground or background, is treated as a
// job" so session history (package session, hooked to job.Manager's
// OnFinish) captures ordinary foreground commands, not only backgrounded
// ones, with no separate logging path. Without a namespace (mainly bare
// eval package tests that don't need job tracking), it falls back to a
// direct os/exec call — the original Phase 1 implementation, unchanged —
// rather than making a namespace suddenly mandatory for basic %cmd use.
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

	if env.Namespace() != nil {
		return runExternalViaJob(env, x.Name, args, in)
	}
	return runExternalDirect(x.Name, args, in)
}

func runExternalDirect(name string, args []string, in value.Value) (value.Value, error) {
	cmd := exec.Command(name, args...)
	if in != nil {
		cmd.Stdin = bytes.NewReader(renderForExternal(in))
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("%%%s: %v", name, err)}, nil
	}
	_ = cmd.Wait() // non-zero exit is ordinary data, not a Go-level error here

	return value.Bytes(stdout.Bytes()), nil
}

// jobWaitStatus is the subset of job.Status this function (and
// namespace.go's recordProxyJobAsync) needs from the wait file's JSON —
// a local, minimal decode rather than pulling in job.Status itself (eval
// doesn't otherwise depend on package job; only on the namespace files
// any 9P client would see).
type jobWaitStatus struct {
	State    string `json:"state"`
	Err      string `json:"error"`
	ExitCode *int   `json:"exit_code"`
	Signal   string `json:"signal"`
}

// runExternalViaJob is runExternalDirect's job-tracked equivalent: the
// same clone/argv/stdin/ctl-start sequence evalBackground uses, but
// blocks on wait before returning instead of handing back a live
// record. A job that fails to start (job.StateFailed — its status
// carries the exec.Start error, since the ctl "start" Write itself
// never fails for this case, only the job's terminal status does)
// becomes a value.ErrorVal, matching runExternalDirect's
// exec.Start()-failure handling exactly, so kyu code can't tell which
// path ran from a bad-command-name failure alone.
func runExternalViaJob(env *Env, name string, args []string, in value.Value) (value.Value, error) {
	namespace := env.Namespace()
	jobRoot := env.JobRoot()
	tsStart := time.Now()
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	clone, err := openFile(ctx, root, p9.OREAD, jobPath(jobRoot, "clone")...)
	if err != nil {
		return nil, fmt.Errorf("%%%s: %w (is %s bound?)", name, err, jobPathStr(jobRoot))
	}
	idBytes, err := readAllFile(ctx, clone)
	if err != nil {
		return nil, fmt.Errorf("%%%s: reading job id: %w", name, err)
	}
	clone.Close()
	id := strings.TrimSpace(string(idBytes))

	argv := append([]string{name}, args...)
	argvFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "argv")...)
	if err != nil {
		return nil, err
	}
	if _, err := argvFile.Write(ctx, 0, []byte(strings.Join(argv, "\n"))); err != nil {
		return nil, fmt.Errorf("%%%s: writing argv: %w", name, err)
	}
	argvFile.Close()

	ctlFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "ctl")...)
	if err != nil {
		return nil, err
	}
	if _, err := ctlFile.Write(ctx, 0, []byte("start")); err != nil {
		return nil, fmt.Errorf("%%%s: starting job: %w", name, err)
	}
	ctlFile.Close()

	// stdin is written only after start, not before: it's a real
	// io.Pipe() (synchronous, unbuffered), and nothing reads from it
	// until the process is actually running and os/exec's internal
	// stdin-forwarding goroutine begins draining it — a write attempted
	// before ctl start would deadlock waiting for a reader that doesn't
	// exist yet.
	stdinFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "stdin")...)
	if err != nil {
		return nil, err
	}
	if in != nil {
		if _, err := stdinFile.Write(ctx, 0, renderForExternal(in)); err != nil {
			return nil, fmt.Errorf("%%%s: writing stdin: %w", name, err)
		}
	}
	stdinFile.Close() // EOF regardless of whether anything was written — see job/job_test.go's note

	waitFile, err := openFile(ctx, root, p9.OREAD, jobPath(jobRoot, id, "wait")...)
	if err != nil {
		return nil, err
	}
	waitBytes, err := readAllFile(ctx, waitFile)
	waitFile.Close()
	if err != nil {
		return nil, fmt.Errorf("%%%s: waiting: %w", name, err)
	}
	var st jobWaitStatus
	if err := json.Unmarshal(waitBytes, &st); err != nil {
		return nil, fmt.Errorf("%%%s: decoding job status: %w", name, err)
	}

	// Unlike evalBackground (which never waits, so has no terminal status
	// in hand at all), runExternalViaJob already blocked on wait above —
	// record synchronously here with the real result, whatever it was,
	// rather than spawning a second goroutine to re-derive what's already
	// known.
	if host, ok := isProxyJobRoot(jobRoot); ok {
		if rec := env.ProxyRecorder(); rec != nil {
			remoteID, _ := strconv.Atoi(id)
			rec(host, remoteID, argv, tsStart, time.Now(), st.ExitCode, st.Signal)
		}
	}

	if st.State == "failed" {
		return value.ErrorVal{Msg: fmt.Sprintf("%%%s: %s", name, st.Err)}, nil
	}

	stdoutFile, err := openFile(ctx, root, p9.OREAD, jobPath(jobRoot, id, "stdout")...)
	if err != nil {
		return nil, err
	}
	out, err := readAllFile(ctx, stdoutFile)
	stdoutFile.Close()
	if err != nil {
		return nil, fmt.Errorf("%%%s: reading stdout: %w", name, err)
	}

	// The job's stderr is captured into its own growBuf (job.go), same as
	// stdout, rather than inherited live from the terminal — so unlike
	// runExternalDirect (which wires cmd.Stderr = os.Stderr and gets it for
	// free), it has to be explicitly read back and forwarded here. Without
	// this, stderr silently vanishes for the common bare-%cmd case: nothing
	// else reads or returns it, and there's no job handle in the caller's
	// hands to fetch it from afterward.
	stderrFile, err := openFile(ctx, root, p9.OREAD, jobPath(jobRoot, id, "stderr")...)
	if err != nil {
		return nil, err
	}
	errOut, err := readAllFile(ctx, stderrFile)
	stderrFile.Close()
	if err != nil {
		return nil, fmt.Errorf("%%%s: reading stderr: %w", name, err)
	}
	os.Stderr.Write(errOut)

	return value.Bytes(out), nil
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
