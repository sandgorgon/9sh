package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

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

	// A dial(addr) result is an unbound remote filesystem root, not a path
	// already reachable in the local namespace — it goes through BindFS
	// (the same Go-bootstrap-shaped graft /jobs and /local use), not
	// BindPath, which only ever resolves srcPaths by walking the
	// namespace that already exists.
	if mh, ok := srcVal.(value.MountHandle); ok {
		fs, ok := mh.FS.(server.FileSystem)
		if !ok {
			return nil, fmt.Errorf("bind: mount handle for %s has no usable filesystem", mh.Addr)
		}
		if err := namespace.BindFSOpts(fs, "", string(dstPath), disp, ns.BindOpts{Spec: mh.Spec, ReadOnly: st.ReadOnly}); err != nil {
			return nil, err
		}
		return value.Null{}, nil
	}

	srcPaths, err := pathsOf(srcVal)
	if err != nil {
		return nil, fmt.Errorf("bind: source: %w", err)
	}
	if err := namespace.BindPathOpts(context.Background(), srcPaths, string(dstPath), disp, ns.BindOpts{ReadOnly: st.ReadOnly}); err != nil {
		return nil, err
	}
	return value.Null{}, nil
}

// evalUnbindStmt implements `unbind DST`, the inverse of bind — see
// ast.UnbindStmt's doc comment on why it's a statement, not a builtin.
func evalUnbindStmt(st *ast.UnbindStmt, env *Env) (value.Value, error) {
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("unbind: no namespace attached to this environment")
	}
	dstVal, err := evalExpr(st.Dst, env)
	if err != nil {
		return nil, err
	}
	dstPath, ok := dstVal.(value.Path)
	if !ok {
		return nil, fmt.Errorf("unbind: destination must be a path, got %s", dstVal.Kind())
	}
	if err := namespace.Unbind(string(dstPath)); err != nil {
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

// evalBackground runs `expr &` (see ast.Background's own doc comment):
// an *ast.ExternalCall becomes a subprocess job (this function, below,
// unchanged from before this dispatch existed); anything else becomes
// an in-process job (evalBackgroundInproc). `&pty` (x.Pty) is rejected
// up front for the in-process case — there's no OS process to attach a
// pty to, the same reason stop/resume/signal/priority already reject an
// in-process job in job.go's Ctl.
func evalBackground(x *ast.Background, env *Env) (value.Value, error) {
	call, ok := x.Expr.(*ast.ExternalCall)
	if !ok {
		if x.Pty {
			return nil, fmt.Errorf("'&pty': not supported for in-process jobs (no OS process to attach a pty to)")
		}
		return evalBackgroundInproc(x.Expr, env)
	}
	return evalBackgroundSubprocess(call, env, x.Pty)
}

// evalBackgroundSubprocess runs `%cmd args... &`: allocates a
// subprocess job under /jobs (via the namespace — the same File
// interface a real 9P client would use, just in-process; see
// ns.Namespace.Attach), starts it, and returns a live job record whose
// fields write through to the job's namespace files.
//
// Rejected outright for a fullscreen program (see isFullscreenProgram,
// fullscreen.go): there's no real screen to hand a backgrounded process
// -- it isn't in the foreground -- so running it via /jobs like an
// ordinary command would just start it with no controlling terminal at
// all, not something a user backgrounding vim/ssh/etc. actually wants.
// call.NameExpr (the %(expr) form -- see ast.ExternalCall's own doc
// comment) is resolved first, before even that check, the same order
// runExternal uses for the foreground case.
//
// pty, when true (kyu's `&pty` — see ast.Background.Pty), writes `ctl
// pty` before `ctl start` below, opting the job into job/job.go's real
// pseudo-terminal instead of plain pipes (merged stdout/stderr,
// process-group signal/kill, a working `ctl resize`) -- see SetPty's
// own doc comment for the full behavior change.
func evalBackgroundSubprocess(call *ast.ExternalCall, env *Env, pty bool) (value.Value, error) {
	name, err := externalCallName(call, env)
	if err != nil {
		return nil, err
	}
	if isFullscreenProgram(env, name) {
		return nil, fmt.Errorf("'&': %s needs a live terminal, can't run in the background", name)
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("'&': no namespace attached to this environment (is /jobs bound?)")
	}
	ctx := context.Background()
	jobRoot := env.JobRoot()

	argv := make([]string, len(call.Args)+1)
	argv[0] = name
	for i, a := range call.Args {
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
	clone, err := openFile(ctx, root, p9.OREAD, jobPath(jobRoot, "clone")...)
	if err != nil {
		return nil, fmt.Errorf("'&': %w (is %s bound?)", err, jobPathStr(jobRoot))
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
	stdinFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "stdin")...)
	if err != nil {
		return nil, err
	}
	stdinFile.Close()

	argvFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "argv")...)
	if err != nil {
		return nil, err
	}
	if _, err := argvFile.Write(ctx, 0, []byte(strings.Join(argv, "\n"))); err != nil {
		return nil, fmt.Errorf("'&': writing argv: %w", err)
	}
	argvFile.Close()

	// A prior cd(...) overrides the job's default cwd (its own process's
	// os.Getwd(), see job.New) — only written when set, matching argv's
	// "small config file written once before ctl start" shape.
	if cwd := env.Cwd(); cwd != "" {
		cwdFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "cwd")...)
		if err != nil {
			return nil, err
		}
		if _, err := cwdFile.Write(ctx, 0, []byte(cwd)); err != nil {
			return nil, fmt.Errorf("'&': writing cwd: %w", err)
		}
		cwdFile.Close()
	}

	// See runExternalViaJob's identical block: the job protocol's "env"
	// file is already fully wired, this just populates it from /env's
	// current contents before start.
	if envVars, err := envSlice(ctx, namespace); err != nil {
		return nil, fmt.Errorf("'&': reading /env: %w", err)
	} else if envVars != nil {
		envFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "env")...)
		if err != nil {
			return nil, err
		}
		if _, err := envFile.Write(ctx, 0, []byte(strings.Join(envVars, "\n"))); err != nil {
			return nil, fmt.Errorf("'&': writing env: %w", err)
		}
		envFile.Close()
	}

	tsStart := time.Now()
	ctlFile, err := openFile(ctx, root, p9.OWRITE, jobPath(jobRoot, id, "ctl")...)
	if err != nil {
		return nil, err
	}
	if pty {
		// Must land before "start": SetPty only accepts a still-pending
		// job (see job.go's own guard), matching every other pending-only
		// config write (argv/cwd/env) already made above.
		if _, err := ctlFile.Write(ctx, 0, []byte("pty")); err != nil {
			return nil, fmt.Errorf("'&pty': opting into a pty: %w", err)
		}
	}
	if _, err := ctlFile.Write(ctx, 0, []byte("start")); err != nil {
		return nil, fmt.Errorf("'&': starting job: %w", err)
	}
	ctlFile.Close()

	if host, ok := isProxyJobRoot(jobRoot); ok {
		if rec := env.ProxyRecorder(); rec != nil {
			// Fire-and-forget: evalBackground never waits for this job, so
			// there's no terminal status in hand here to record yet, only
			// once it eventually finishes — mirroring job.Job.notifyFinished's
			// own `go fn(...)` shape for local jobs, whose caller has
			// likewise already moved on by the time this can matter.
			go recordProxyJobAsync(namespace, jobRoot, id, host, append([]string(nil), argv...), tsStart, rec)
		}
	}

	base, err := walkAll(ctx, root, jobPath(jobRoot, id))
	if err != nil {
		return nil, err
	}
	return buildJobRecord(ctx, base)
}

// evalBackgroundInproc runs `expr &` for any expr that isn't an
// *ast.ExternalCall: an in-process job (job.KindInproc), not a
// subprocess. Local only -- rejected via isProxyJobRoot the same way a
// fullscreen program is rejected in evalBackgroundSubprocess, since
// sending a live Go closure across a 9P wire to a different 9sh process
// isn't something this implements (that would be a distributed-
// evaluation feature, not this one).
//
// expr runs against a brand-new root Env (NewGlobalEnv), not env
// itself, seeded with a snapshot of every name env.Names() reports --
// deliberately not the live env: Env.vars has no locking, and most of
// Env's other process-wide fields are documented safe only because no
// two evaluate() calls ever overlap (see e.g. SetFullscreenHandler's
// doc comment), an invariant a genuinely concurrent background job
// breaks by construction. The snapshot copies bindings, not values: a
// captured *value.Record/*value.List (or another closure) still aliases
// the original, same as an ordinary Go closure capturing a shared
// slice/map would -- this doesn't newly introduce that class of race,
// just doesn't try to solve it either.
//
// A bare `{ ... }` closure literal immediately backgrounded (the
// canonical `{ 6 * 7 } &` case) is auto-invoked with zero arguments --
// evaluating a *ast.Closure node on its own just produces a ClosureVal,
// it doesn't run the body, and "background this closure value, unrun"
// isn't a useful job (its whole result would always just be
// "<closure>"). Anything else (e.g. `f(21) &`, already a call
// expression) evaluates normally -- it doesn't need this, since
// evaluating a call expression already runs it.
//
// The result value has nowhere structured to go in the job protocol
// (Status has no slot for an arbitrary kyu value, only success/failure
// -- see job.Status's own fields), so it's serialized the same way a
// foreground %cmd's captured stdout already becomes value.Bytes:
// written as text to the job's stdout, read back via `j.wait` then
// `j.stdout`, exactly mirroring evalBackgroundSubprocess's own
// "stdout/stderr are the return channel" convention. A kyu-level error
// (ErrorVal, or an ordinary Go error from evalExpr) is written to
// stderr and otherwise treated like a nonzero exit -- not a job
// failure -- so only real cancellation (a killed job, ctx.Err()) ever
// produces StateFailed; see evalWhile/callClosure for where that
// cancellation is actually checked.
func evalBackgroundInproc(expr ast.Expr, env *Env) (value.Value, error) {
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("'&': no namespace attached to this environment (is /jobs bound?)")
	}
	jobRoot := env.JobRoot()
	if host, ok := isProxyJobRoot(jobRoot); ok {
		return nil, fmt.Errorf("'&': can't background kyu code on a remote host (%s) -- only external commands (%%cmd) can run there", host)
	}
	mgr := env.LocalJobManager()
	if mgr == nil {
		return nil, fmt.Errorf("'&': no local job manager configured (is this environment fully bootstrapped?)")
	}

	// NewGlobalEnv, not a bare NewEnv(nil): builtins like cd/checkout
	// close over the specific Env they were Defined on (see
	// NewGlobalEnv's own doc comment) -- this is what makes cd() inside
	// a backgrounded closure call SetCwd on the job's own fresh root,
	// not on env's root, which matters even though this whole snapshot
	// already exists to avoid the background job touching env's
	// mutable state.
	freshRoot := NewGlobalEnv(namespace)
	jobEnv := NewEnv(freshRoot)
	for _, name := range env.Names() {
		if _, isBuiltin := freshRoot.Get(name); isBuiltin {
			// Already bound by NewGlobalEnv above, correctly closing
			// over freshRoot -- copying env's own (foreground-bound)
			// version over it would silently break that rebinding, e.g.
			// making a backgrounded cd() mutate the wrong Env.
			continue
		}
		if v, ok := env.Get(name); ok {
			jobEnv.Define(name, v)
		}
	}
	if cwd := env.Cwd(); cwd != "" {
		freshRoot.SetCwd(cwd)
	}

	j := mgr.AllocInproc(func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		// nil cancel func: this job is cancelled via job.go's own
		// Ctl("kill") calling ctx's cancel function directly, never
		// through Env -- see SetCancelContext's own doc comment.
		jobEnv.SetCancelContext(ctx, nil)
		var result value.Value
		var err error
		if closureLit, ok := expr.(*ast.Closure); ok {
			result, err = callClosure(&ClosureVal{Node: closureLit, Env: jobEnv}, nil)
		} else {
			result, err = evalExpr(expr, jobEnv)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			io.WriteString(stderr, err.Error())
			return nil
		}
		if result.Kind() != "null" {
			text := result.String()
			if b, ok := result.(value.Bytes); ok {
				text = string(b)
			}
			io.WriteString(stdout, text)
		}
		return nil
	})
	if err := j.Ctl("start"); err != nil {
		return nil, fmt.Errorf("'&': starting job: %w", err)
	}

	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	base, err := walkAll(ctx, root, jobPath(jobRoot, strconv.Itoa(j.ID)))
	if err != nil {
		return nil, err
	}
	return buildJobRecord(ctx, base)
}

// isProxyJobRoot reports whether jobRoot points at a remote peer's /jobs
// tree — `@host{}`'s desugaring (see evalAtHost) — rather than the local
// /jobs, and if so returns a label for the remote: the bare host name for
// the common /n/<host> mount, the mount path otherwise (`@/mnt/ci{}`).
// evalBackground and runExternalViaJob don't otherwise know or care that @
// exists; this is the one place either asks, purely to decide whether a
// local-side proxy linking record is even applicable.
func isProxyJobRoot(jobRoot []string) (host string, ok bool) {
	// The local job root is exactly ["jobs"]; anything else ends in
	// "jobs" under some mount point.
	if len(jobRoot) < 2 || jobRoot[len(jobRoot)-1] != "jobs" {
		return "", false
	}
	mount := jobRoot[:len(jobRoot)-1]
	if len(mount) == 2 && mount[0] == "n" {
		return mount[1], true
	}
	return "/" + strings.Join(mount, "/"), true
}

// recordProxyJobAsync waits for a backgrounded proxy job to finish, then
// reports it through record. It opens its own, independent fid onto the
// job's wait file (rather than reusing anything the caller's live job
// record already holds) — that file blocks until the job is terminal,
// exactly the semantics buildJobRecord's own "wait" field already relies
// on, just read here through a second, unrelated handle so this
// goroutine can't race whatever the kyu caller does with its own live
// record. Any error along the way (the connection dropping, e.g.) just
// means no linking record gets appended — the remote peer's own history
// already has the authoritative entry regardless.
func recordProxyJobAsync(namespace *ns.Namespace, jobRoot []string, id, host string, argv []string, tsStart time.Time, record ProxyRecorderFunc) {
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return
	}
	waitFile, err := openFile(ctx, root, p9.OREAD, jobPath(jobRoot, id, "wait")...)
	if err != nil {
		return
	}
	defer waitFile.Close()
	waitBytes, err := readAllFile(ctx, waitFile)
	if err != nil {
		return
	}
	var st jobWaitStatus
	if err := json.Unmarshal(waitBytes, &st); err != nil {
		return
	}
	remoteID, _ := strconv.Atoi(id)
	record(host, remoteID, argv, tsStart, time.Now(), st.ExitCode, st.Signal)
}

// jobPath appends parts to a copy of jobRoot — never mutating or aliasing
// jobRoot's backing array, since it's shared across every job created in
// the same scope (Env.JobRoot returns the same slice to every caller).
func jobPath(jobRoot []string, parts ...string) []string {
	out := make([]string, 0, len(jobRoot)+len(parts))
	out = append(out, jobRoot...)
	out = append(out, parts...)
	return out
}

func jobPathStr(jobRoot []string) string {
	return "/" + strings.Join(jobRoot, "/")
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

// buildJobRecord constructs a job's kyu-visible record: live-backed fields
// over the namespace files under base ("/jobs/<id>"). "wait" is
// deliberately just another backed field (its ReadField blocks until the
// job is terminal) rather than special syntax — `j | wait` desugars to
// the "wait" builtin, which is just rec.Get("wait"); see builtins.go.
// "stdout"/"stderr" are read-only and kept as raw value.Bytes (not
// text-decoded/trimmed like argv/env) — the same shape a foreground %cmd
// already returns, so `(%cmd "x" &) | wait` then `.stdout` matches
// runExternalDirect/runExternalViaJob's own foreground return value
// exactly, just reached via the live record instead of blocking inline.
func buildJobRecord(ctx context.Context, base server.File) (*value.Record, error) {
	rec := value.NewRecord()
	const (
		kindJSON = iota
		kindText
		kindBytes
	)
	fields := []struct {
		name     string
		writable bool
		readable bool // kindText only; false only for ctl (write-only namespace file)
		blocking bool // kindJSON only; true only for wait (Read blocks until terminal)
		kind     int
	}{
		{"status", false, true, false, kindJSON},
		{"wait", false, true, true, kindJSON},
		{"ctl", true, false, false, kindText},
		{"argv", true, true, false, kindText},
		{"env", true, true, false, kindText},
		{"cwd", true, true, false, kindText},
		{"stdout", false, true, false, kindBytes},
		{"stderr", false, true, false, kindBytes},
	}
	for _, fld := range fields {
		f, err := openFile(ctx, base, p9.ORDWR, fld.name)
		if err != nil {
			return nil, err
		}
		switch fld.kind {
		case kindJSON:
			rec.SetBacking(fld.name, &jsonField{ctx: ctx, file: f, blocking: fld.blocking})
		case kindBytes:
			rec.SetBacking(fld.name, &bytesField{ctx: ctx, file: f})
		default:
			rec.SetBacking(fld.name, &textField{ctx: ctx, file: f, writable: fld.writable, readable: fld.readable})
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
	readable bool
}

func (f *textField) ReadField() (value.Value, error) {
	if !f.readable {
		return nil, fmt.Errorf("field is write-only")
	}
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

// DisplayField implements value.FieldDisplay: an unreadable field (ctl)
// shows a plain placeholder in a record dump rather than the namespace
// file's own "write-only" error — the failure is already known statically
// (readable is false), so there's no need to actually hit the file just to
// render it. A readable field renders exactly like Record's default
// (Get + String) would, so status/argv/env's display is unchanged.
func (f *textField) DisplayField() string {
	if !f.readable {
		return "<write-only>"
	}
	v, err := f.ReadField()
	if err != nil {
		return "error: " + err.Error()
	}
	return v.String()
}

// bytesField backs a field with a namespace file's raw content, undecoded
// and untrimmed — stdout/stderr, where trimming a trailing newline or
// forcing UTF-8 (textField's behavior) would corrupt binary output.
// Read-only: writing to a job's own stdout/stderr isn't a sensible
// operation.
type bytesField struct {
	ctx  context.Context
	file server.File
}

func (f *bytesField) ReadField() (value.Value, error) {
	b, err := readAllFile(f.ctx, f.file)
	if err != nil {
		return nil, err
	}
	return value.Bytes(b), nil
}

func (f *bytesField) WriteField(value.Value) error {
	return fmt.Errorf("field is read-only")
}

// jsonField backs a field with a namespace file whose content is one
// JSON object (status, wait) — job/fs.go's placeholder for the design
// doc's NRF/NRL format — decoded into a nested kyu Record so
// `j.status.state` reads naturally instead of forcing callers to parse
// a raw string.
type jsonField struct {
	ctx      context.Context
	file     server.File
	blocking bool // true for "wait": ReadField blocks until the job is terminal
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

// DisplayField implements value.FieldDisplay: a blocking field (wait)
// shows a plain placeholder in a record dump instead of actually blocking
// until the job finishes — printing the record as a whole is a glance at
// its current shape, not an implicit `| wait`. Explicit `.wait`/`j | wait`
// access still goes through ReadField and blocks exactly as designed. A
// non-blocking field (status) renders exactly like Record's default
// (Get + String) would, so its display is unchanged.
func (f *jsonField) DisplayField() string {
	if f.blocking {
		return "<blocks until done>"
	}
	v, err := f.ReadField()
	if err != nil {
		return "error: " + err.Error()
	}
	return v.String()
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

// kyuToJSON converts a kyu Value into a JSON-marshalable Go value — the
// reverse of jsonToKyu, for to_json(). A Record's field order is not
// preserved (Go's encoding/json sorts map keys when marshaling a
// map[string]any) — the same accepted simplification jsonToKyu's own
// decode direction already makes for the opposite case, not a new one.
// A live-backed field is read via Get (so to_json(jobRecord) reflects
// current status, argv, etc.), the same as printing or explicit field
// access already does. Kinds with no natural JSON representation
// (ErrorVal, Ref, NSUnion, MountHandle) are a reported error, not a
// silent null.
func kyuToJSON(v value.Value) (any, error) {
	switch x := v.(type) {
	case value.Null:
		return nil, nil
	case value.Bool:
		return bool(x), nil
	case value.Int:
		return int64(x), nil
	case value.Float:
		return float64(x), nil
	case value.String:
		return string(x), nil
	case value.Bytes:
		return []byte(x), nil // encoding/json base64-encodes a []byte automatically
	case value.Path:
		return string(x), nil
	case value.Duration:
		return x.String(), nil
	case *value.Record:
		keys := x.Keys()
		out := make(map[string]any, len(keys))
		for _, k := range keys {
			fv, _ := x.Get(k)
			jv, err := kyuToJSON(fv)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", k, err)
			}
			out[k] = jv
		}
		return out, nil
	case *value.List:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			jv, err := kyuToJSON(e)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			out[i] = jv
		}
		return out, nil
	default:
		return nil, fmt.Errorf("cannot convert a %s to JSON", v.Kind())
	}
}
