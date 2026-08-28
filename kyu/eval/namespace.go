package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func evalBindStmt(st *ast.BindStmt, env *Env) (value.Value, error) {
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("bind: no namespace attached to this environment")
	}
	srcVal, err := evalExpr(st.Src, env)
	if err != nil {
		return nil, err
	}
	srcPaths, err := pathsOf(srcVal)
	if err != nil {
		return nil, fmt.Errorf("bind: source: %w", err)
	}
	dstVal, err := evalExpr(st.Dst, env)
	if err != nil {
		return nil, err
	}
	dstPath, ok := dstVal.(value.Path)
	if !ok {
		return nil, fmt.Errorf("bind: destination must be a path, got %s", dstVal.Kind())
	}
	disp, err := parseDisposition(st.Disposition)
	if err != nil {
		return nil, err
	}
	if err := namespace.BindPath(context.Background(), srcPaths, string(dstPath), disp); err != nil {
		return nil, err
	}
	return value.Null{}, nil
}

// pathsOf converts a bind SRC value (a plain Path, or an NSUnion from a
// `a + b` namespace-union expression) into the ordered path list
// ns.Namespace.BindPath wants.
func pathsOf(v value.Value) ([]string, error) {
	switch x := v.(type) {
	case value.Path:
		return []string{string(x)}, nil
	case value.NSUnion:
		out := make([]string, len(x.Paths))
		for i, p := range x.Paths {
			out[i] = string(p)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected a path or a namespace-union expression, got %s", v.Kind())
	}
}

func parseDisposition(s string) (ns.Disposition, error) {
	switch s {
	case "replace":
		return ns.Replace, nil
	case "before":
		return ns.Before, nil
	case "after":
		return ns.After, nil
	default:
		return 0, fmt.Errorf("bind: unknown disposition %q", s)
	}
}

// evalBackground runs `%cmd args... &`: allocates a subprocess job under
// /jobs (via the namespace — the same File interface a real 9P client
// would use, just in-process; see ns.Namespace.Attach), starts it, and
// returns a live job record whose fields write through to the job's
// namespace files.
func evalBackground(x *ast.Background, env *Env) (value.Value, error) {
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("'&': no namespace attached to this environment (is /jobs bound?)")
	}
	ctx := context.Background()

	argv := make([]string, len(x.Call.Args)+1)
	argv[0] = x.Call.Name
	for i, a := range x.Call.Args {
		v, err := evalExpr(a, env)
		if err != nil {
			return nil, err
		}
		s, err := argString(v)
		if err != nil {
			return nil, fmt.Errorf("'&': argument %d: %w", i, err)
		}
		argv[i+1] = s
	}

	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	clone, err := openFile(ctx, root, p9.OREAD, "jobs", "clone")
	if err != nil {
		return nil, fmt.Errorf("'&': %w (is /jobs bound?)", err)
	}
	idBytes, err := readAllFile(ctx, clone)
	if err != nil {
		return nil, fmt.Errorf("'&': reading job id: %w", err)
	}
	clone.Close()
	id := strings.TrimSpace(string(idBytes))

	// There's no kyu syntax yet to feed a backgrounded job's stdin, so
	// close it immediately — matching "no input redirection" — rather
	// than leaving it open with nothing that will ever write to or close
	// it. Left open, os/exec's own internal stdin-forwarding goroutine
	// (spawned because Cmd.Stdin here isn't an *os.File) would block
	// Wait() forever regardless of whether the child even reads stdin;
	// see job/job_test.go's note on the same issue.
	stdinFile, err := openFile(ctx, root, p9.OWRITE, "jobs", id, "stdin")
	if err != nil {
		return nil, err
	}
	stdinFile.Close()

	argvFile, err := openFile(ctx, root, p9.OWRITE, "jobs", id, "argv")
	if err != nil {
		return nil, err
	}
	if _, err := argvFile.Write(ctx, 0, []byte(strings.Join(argv, "\n"))); err != nil {
		return nil, fmt.Errorf("'&': writing argv: %w", err)
	}
	argvFile.Close()

	ctlFile, err := openFile(ctx, root, p9.OWRITE, "jobs", id, "ctl")
	if err != nil {
		return nil, err
	}
	if _, err := ctlFile.Write(ctx, 0, []byte("start")); err != nil {
		return nil, fmt.Errorf("'&': starting job: %w", err)
	}
	ctlFile.Close()

	base, err := walkAll(ctx, root, []string{"jobs", id})
	if err != nil {
		return nil, err
	}
	return buildJobRecord(ctx, base)
}

func openFile(ctx context.Context, root server.File, mode p9.Mode, parts ...string) (server.File, error) {
	f, err := walkAll(ctx, root, parts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(parts, "/"), err)
	}
	if err := f.Open(ctx, mode); err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(parts, "/"), err)
	}
	return f, nil
}

func walkAll(ctx context.Context, root server.File, parts []string) (server.File, error) {
	f := root
	for _, part := range parts {
		var err error
		f, err = f.Walk(ctx, part)
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

// buildJobRecord constructs a job's kyu-visible record: five live-backed
// fields over the namespace files under base ("/jobs/<id>"). "wait" is
// deliberately just another backed field (its ReadField blocks until the
// job is terminal) rather than special syntax — `j | wait` desugars to
// the "wait" builtin, which is just rec.Get("wait"); see builtins.go.
func buildJobRecord(ctx context.Context, base server.File) (*value.Record, error) {
	rec := value.NewRecord()
	fields := []struct {
		name     string
		writable bool
		json     bool
	}{
		{"status", false, true},
		{"wait", false, true},
		{"ctl", true, false},
		{"argv", true, false},
		{"env", true, false},
	}
	for _, fld := range fields {
		f, err := openFile(ctx, base, p9.ORDWR, fld.name)
		if err != nil {
			return nil, err
		}
		if fld.json {
			rec.SetBacking(fld.name, &jsonField{ctx: ctx, file: f})
		} else {
			rec.SetBacking(fld.name, &textField{ctx: ctx, file: f, writable: fld.writable})
		}
	}
	return rec, nil
}

// textField backs a field with a namespace file's raw text content —
// ctl (write-only), argv/env (read-write, newline-joined, matching the
// job/fs.go's own file format).
type textField struct {
	ctx      context.Context
	file     server.File
	writable bool
}

func (f *textField) ReadField() (value.Value, error) {
	b, err := readAllFile(f.ctx, f.file)
	if err != nil {
		return nil, err
	}
	return value.String(strings.TrimRight(string(b), "\n")), nil
}

func (f *textField) WriteField(v value.Value) error {
	if !f.writable {
		return fmt.Errorf("field is read-only")
	}
	s, ok := v.(value.String)
	if !ok {
		return fmt.Errorf("expected a string, got %s", v.Kind())
	}
	_, err := f.file.Write(f.ctx, 0, []byte(s))
	return err
}

// jsonField backs a field with a namespace file whose content is one
// JSON object (status, wait) — job/fs.go's placeholder for the design
// doc's NRF/NRL format — decoded into a nested kyu Record so
// `j.status.state` reads naturally instead of forcing callers to parse
// a raw string.
type jsonField struct {
	ctx  context.Context
	file server.File
}

func (f *jsonField) ReadField() (value.Value, error) {
	b, err := readAllFile(f.ctx, f.file)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return jsonToKyu(raw), nil
}

func (f *jsonField) WriteField(value.Value) error {
	return fmt.Errorf("field is read-only")
}

// readAllFile reads a server.File to completion. For a blocking file
// (job/fs.go's wait), the first Read call blocks exactly as intended —
// this is a plain sequential reader, not ReadAt, so it never hits the
// short-read-means-EOF trap documented in job/growbuf.go.
func readAllFile(ctx context.Context, f server.File) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	var offset int64
	for {
		n, err := f.Read(ctx, offset, tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	return buf.Bytes(), nil
}

// jsonToKyu converts a generic json.Unmarshal(&any) result into a kyu
// Value. JSON numbers decode to float64 regardless of source syntax, so
// an integral value becomes an Int (matching what a human reading
// `"pid": 1234` expects), everything else a Float. Object keys are
// sorted for a deterministic field order — there's no original-source
// ordering left to recover once decoded into map[string]any.
func jsonToKyu(v any) value.Value {
	switch x := v.(type) {
	case nil:
		return value.Null{}
	case bool:
		return value.Bool(x)
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			return value.Int(int64(x))
		}
		return value.Float(x)
	case string:
		return value.String(x)
	case []any:
		elems := make([]value.Value, len(x))
		for i, e := range x {
			elems[i] = jsonToKyu(e)
		}
		return value.NewList(elems)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rec := value.NewRecord()
		for _, k := range keys {
			rec.Set(k, jsonToKyu(x[k]))
		}
		return rec
	default:
		return value.Null{}
	}
}
