package eval

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func run(t *testing.T, src string) value.Value {
	t.Helper()
	return runEnv(t, src, NewGlobalEnv(nil))
}

func runErr(t *testing.T, src string) error {
	t.Helper()
	return runEnvErr(t, src, NewGlobalEnv(nil))
}

// jobsEnv is a global env with /jobs bound, the same bootstrap
// cmd/9sh's main does — for tests exercising bind/background/live fields.
func jobsEnv(t *testing.T) *Env {
	t.Helper()
	namespace := ns.New()
	if err := namespace.BindFS(job.New(job.NewManager()), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrap bind /jobs: %v", err)
	}
	return NewGlobalEnv(namespace)
}

func runEnv(t *testing.T, src string, env *Env) value.Value {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	v, err := Eval(prog, env)
	if err != nil {
		t.Fatalf("src %q: eval error: %v", src, err)
	}
	return v
}

func runEnvErr(t *testing.T, src string, env *Env) error {
	t.Helper()
	p := parser.New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	_, err := Eval(prog, env)
	if err == nil {
		t.Fatalf("src %q: expected an eval error, got none", src)
	}
	return err
}

func TestArithmetic(t *testing.T) {
	if v := run(t, `1 + 2 * 3`); v.(value.Int) != 7 {
		t.Errorf("got %v, want 7", v)
	}
	if v := run(t, `(1 + 2) * 3`); v.(value.Int) != 9 {
		t.Errorf("got %v, want 9", v)
	}
	if v := run(t, `7 / 2`); v.(value.Int) != 3 {
		t.Errorf("got %v, want 3", v)
	}
	if v := run(t, `7.0 / 2`); v.(value.Float) != 3.5 {
		t.Errorf("got %v, want 3.5", v)
	}
	if v := run(t, `-5 + 2`); v.(value.Int) != -3 {
		t.Errorf("got %v, want -3", v)
	}
	if v := run(t, `"hello, " + "world"`); v.(value.String) != "hello, world" {
		t.Errorf("got %v, want %q", v, "hello, world")
	}
	if v := run(t, `"" + ""`); v.(value.String) != "" {
		t.Errorf("got %v, want empty string", v)
	}
}

func TestStringPlusNonStringStillErrors(t *testing.T) {
	// + concatenates String+String only — no implicit stringification
	// of other kinds, matching kyu's general preference for explicit
	// conversions over silently guessing what a mixed-type + should do.
	runErr(t, `"n = " + 5`)
}

func TestComparisonAndLogic(t *testing.T) {
	if v := run(t, `1 < 2 && 2 < 3`); v != value.Bool(true) {
		t.Errorf("got %v, want true", v)
	}
	if v := run(t, `1 > 2 || 3 == 3`); v != value.Bool(true) {
		t.Errorf("got %v, want true", v)
	}
	if v := run(t, `!(1 == 2)`); v != value.Bool(true) {
		t.Errorf("got %v, want true", v)
	}
}

func TestDefineAndAssign(t *testing.T) {
	if v := run(t, "x := 5\nx = x + 1\nx"); v.(value.Int) != 6 {
		t.Errorf("got %v, want 6", v)
	}
}

func TestAssignUndefinedErrors(t *testing.T) {
	runErr(t, `x = 5`)
}

func TestFieldAccessAndAssign(t *testing.T) {
	v := run(t, `j := { status: "pending" }
j.status = "running"
j.status`)
	if v.(value.String) != "running" {
		t.Errorf("got %v, want running", v)
	}
}

func TestIfElse(t *testing.T) {
	if v := run(t, `if 1 < 2 { "yes" } else { "no" }`); v.(value.String) != "yes" {
		t.Errorf("got %v, want yes", v)
	}
	if v := run(t, `if 1 > 2 { "yes" } else { "no" }`); v.(value.String) != "no" {
		t.Errorf("got %v, want no", v)
	}
}

func TestClosureCall(t *testing.T) {
	if v := run(t, `add := { |a, b| a + b }
add(2, 3)`); v.(value.Int) != 5 {
		t.Errorf("got %v, want 5", v)
	}
}

func TestWherePipeline(t *testing.T) {
	src := `jobs := [{name: "a", ok: true}, {name: "b", ok: false}, {name: "c", ok: true}]
jobs | where { |j| j.ok } | count()`
	if v := run(t, src); v.(value.Int) != 2 {
		t.Errorf("got %v, want 2", v)
	}
}

func TestSelectPipeline(t *testing.T) {
	src := `t := [{a: 1, b: 2}, {a: 3, b: 4}]
t | select("a")`
	v := run(t, src)
	lst := v.(*value.List)
	if len(lst.Elems) != 2 {
		t.Fatalf("want 2 rows, got %d", len(lst.Elems))
	}
	rec := lst.Elems[0].(*value.Record)
	if len(rec.Keys()) != 1 || rec.Keys()[0] != "a" {
		t.Errorf("want only field a, got %v", rec.Keys())
	}
}

func TestSortByFieldName(t *testing.T) {
	src := `t := [{n: 3}, {n: 1}, {n: 2}]
t | sort_by("n")`
	v := run(t, src)
	lst := v.(*value.List)
	want := []int64{1, 2, 3}
	for i, w := range want {
		rec := lst.Elems[i].(*value.Record)
		n, _ := rec.Get("n")
		if int64(n.(value.Int)) != w {
			t.Errorf("index %d: got %v, want %d", i, n, w)
		}
	}
}

func TestSortByClosure(t *testing.T) {
	src := `t := [{n: 3}, {n: 1}, {n: 2}]
t | sort_by({ |r| 0 - r.n })`
	v := run(t, src)
	lst := v.(*value.List)
	n, _ := lst.Elems[0].(*value.Record).Get("n")
	if int64(n.(value.Int)) != 3 {
		t.Errorf("want first=3 (descending via negated key), got %v", n)
	}
}

func TestGroupBy(t *testing.T) {
	src := `t := [{k: "a", v: 1}, {k: "b", v: 2}, {k: "a", v: 3}]
t | group_by("k")`
	v := run(t, src)
	lst := v.(*value.List)
	if len(lst.Elems) != 2 {
		t.Fatalf("want 2 groups, got %d", len(lst.Elems))
	}
	g0 := lst.Elems[0].(*value.Record)
	key, _ := g0.Get("key")
	if key.(value.String) != "a" {
		t.Errorf("want first group key 'a', got %v", key)
	}
	items, _ := g0.Get("items")
	if len(items.(*value.List).Elems) != 2 {
		t.Errorf("want group 'a' to have 2 items, got %d", len(items.(*value.List).Elems))
	}
}

func TestEachMutatesRecordsInPlace(t *testing.T) {
	src := `jobs := [{status: "pending"}, {status: "pending"}]
jobs | each { |j| j.status = "done" }
jobs | where { |j| j.status == "done" } | count()`
	if v := run(t, src); v.(value.Int) != 2 {
		t.Errorf("got %v, want 2 (records are reference types, each's mutation is visible via the original binding)", v)
	}
}

func TestTakeAndFirst(t *testing.T) {
	src := `t := [1, 2, 3, 4, 5]
t | take(2)`
	v := run(t, src)
	if len(v.(*value.List).Elems) != 2 {
		t.Errorf("want 2 elements, got %d", len(v.(*value.List).Elems))
	}
	if v := run(t, `[10, 20] | first`); v.(value.Int) != 10 {
		t.Errorf("got %v, want 10", v)
	}
	if v := run(t, `[] | first`); v.Kind() != "null" {
		t.Errorf("got %v, want null", v)
	}
}

func TestErrCheckAborts(t *testing.T) {
	err := runErr(t, `error("boom")?`)
	if err.Error() != "boom" {
		t.Errorf("got %v, want boom", err)
	}
}

func TestErrorValueIsFalsyInWhere(t *testing.T) {
	src := `t := [1, 2, 3]
t | where { |x| if x == 2 { error("nope") } else { true } }`
	v := run(t, src)
	lst := v.(*value.List)
	if len(lst.Elems) != 2 {
		t.Fatalf("want 2 (row 2 excluded via falsy ErrorVal, no abort), got %d: %v", len(lst.Elems), lst)
	}
}

func TestExternalCallCat(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not on PATH")
	}
	v := run(t, `"hello" | %cat`)
	b := v.(value.Bytes)
	if string(b) != "hello\n" {
		t.Errorf("got %q, want %q", string(b), "hello\n")
	}
}

func TestExternalCallBadCommand(t *testing.T) {
	v := run(t, `%this-command-does-not-exist-9sh`)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal for a missing command, got %#v", v)
	}
}

// TestPassthroughInheritsRealStdio locks in $cmd's whole reason for
// existing: unlike %cmd (always job-tracked, output buffered into a
// growBuf and only returned after the job finishes — see
// runExternalViaJob), $cmd connects the subprocess directly to 9sh's
// own stdout/stderr, with no job created at all.
func TestPassthroughInheritsRealStdio(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env, mgr := jobsEnvWithManager(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	v := runEnv(t, `$echo "hello-passthrough"`, env)
	os.Stdout = origStdout
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if string(captured) != "hello-passthrough\n" {
		t.Fatalf("stdout = %q, want %q", captured, "hello-passthrough\n")
	}
	if _, ok := v.(value.Null); !ok {
		t.Fatalf("want value.Null (nothing to capture), got %#v", v)
	}
	if len(mgr.List()) != 0 {
		t.Fatalf("$cmd must not create a job, got %d", len(mgr.List()))
	}
}

// TestPassthroughBlockedByEnv locks in cmd/9sh's TUI guard: SetPassthroughBlocked
// makes $cmd fail with an ErrorVal instead of touching os.Stdin/Stdout/
// Stderr at all -- see Env.SetPassthroughBlocked's doc comment for why
// the TUI needs this (a real subprocess sharing the terminal would race
// tui.App.Run's own raw-mode stdin reader).
func TestPassthroughBlockedByEnv(t *testing.T) {
	env := jobsEnv(t)
	env.SetPassthroughBlocked("not supported here")
	v := runEnv(t, `$echo "should not run"`, env)
	ev, ok := v.(value.ErrorVal)
	if !ok {
		t.Fatalf("want ErrorVal, got %#v", v)
	}
	if !strings.Contains(ev.Msg, "not supported here") {
		t.Fatalf("error = %q, want it to mention the blocked reason", ev.Msg)
	}
}

func TestPassthroughBadCommandIsErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `$this-command-does-not-exist-9sh`, env)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal for a missing command, got %#v", v)
	}
}

func skipUnlessOnPath(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}

func TestKyuNamespaceUnionExpr(t *testing.T) {
	v := run(t, `a := /local/bin
b := /n/host/bin
a + b`)
	u, ok := v.(value.NSUnion)
	if !ok {
		t.Fatalf("want NSUnion, got %T", v)
	}
	if len(u.Paths) != 2 || u.Paths[0] != "/local/bin" || u.Paths[1] != "/n/host/bin" {
		t.Fatalf("got %v", u)
	}
}

func TestBackgroundWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `%true &`)
}

func TestBindWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `bind /a, /b`)
}

func TestKyuBindGraftsExistingNamespacePath(t *testing.T) {
	env := jobsEnv(t)
	runEnv(t, `bind /jobs, /j2`, env)

	namespace := env.Namespace()
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "u", "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := walkAll(ctx, root, []string{"j2", "clone"}); err != nil {
		t.Fatalf("walk /j2/clone after bind: %v", err)
	}
}

func TestBackgroundJobLifecycle(t *testing.T) {
	skipUnlessOnPath(t, "true")
	env := jobsEnv(t)
	v := runEnv(t, `j := %true &
j | wait
j`, env)
	rec, ok := v.(*value.Record)
	if !ok {
		t.Fatalf("want a job record, got %T", v)
	}
	status, ok := rec.Get("status")
	if !ok {
		t.Fatal("job record missing status field")
	}
	st, ok := status.(*value.Record)
	if !ok {
		t.Fatalf("want status to decode as a record, got %#v", status)
	}
	state, _ := st.Get("state")
	if state.(value.String) != "done" {
		t.Fatalf("state = %v, want done", state)
	}
}

func TestBackgroundJobKillViaCtlField(t *testing.T) {
	skipUnlessOnPath(t, "sleep")
	env := jobsEnv(t)
	// "start" returns only once the process has actually been exec'd (or
	// failed to start) — see job.go's start(), which calls cmd.Start()
	// synchronously — so there's no race needing a retry/sleep here.
	runEnv(t, `j := %sleep 5 &`, env)
	runEnv(t, `j.ctl = "kill"`, env)
	v := runEnv(t, `j | wait
j.status`, env)
	st := v.(*value.Record)
	state, _ := st.Get("state")
	if state.(value.String) != "killed" {
		t.Fatalf("state = %v, want killed", state)
	}
}

func TestJobCtlFieldRejectsNonString(t *testing.T) {
	skipUnlessOnPath(t, "true")
	env := jobsEnv(t)
	runEnv(t, `j := %true &`, env)
	runEnvErr(t, `j.ctl = 5`, env)
}

func TestJobStatusFieldIsReadOnly(t *testing.T) {
	skipUnlessOnPath(t, "true")
	env := jobsEnv(t)
	runEnv(t, `j := %true &`, env)
	runEnvErr(t, `j.status = "done"`, env)
}

// jobsEnvWithManager is jobsEnv but also returns the *job.Manager, for
// tests that need to observe job creation directly (e.g. via List())
// rather than only through kyu-visible effects.
func jobsEnvWithManager(t *testing.T) (*Env, *job.Manager) {
	t.Helper()
	mgr := job.NewManager()
	namespace := ns.New()
	if err := namespace.BindFS(job.New(mgr), "", "/jobs", ns.Replace); err != nil {
		t.Fatalf("bootstrap bind /jobs: %v", err)
	}
	return NewGlobalEnv(namespace), mgr
}

func TestForegroundExternalCallRoutesThroughJobsWhenNamespacePresent(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env, mgr := jobsEnvWithManager(t)
	if len(mgr.List()) != 0 {
		t.Fatalf("expected 0 jobs before any %%cmd, got %d", len(mgr.List()))
	}
	v := runEnv(t, `%echo "hello job-routed"`, env)
	if string(v.(value.Bytes)) != "hello job-routed\n" {
		t.Fatalf("stdout = %q, want %q", v, "hello job-routed\n")
	}
	if len(mgr.List()) != 1 {
		t.Fatalf("expected 1 job after a foreground %%cmd, got %d", len(mgr.List()))
	}
	st := mgr.List()[0].Status()
	if st.State != job.StateDone {
		t.Fatalf("job state = %v, want done", st.State)
	}
}

// TestForegroundExternalCallInheritsProcessEnv locks in the os/exec
// default this repo's cmd/9sh relies on for -listen-unix's _9SH_UNIX_SOCK
// export (see cmd/9sh/main.go's bootstrap): a job whose kyu script never
// calls SetEnv leaves Cmd.Env nil, which os/exec defines as "inherit the
// current process's environment" -- so anything 9sh itself has in its own
// environment (a namespace socket path, PATH, etc.) reaches every %cmd
// job for free, with no per-job wiring.
func TestForegroundExternalCallInheritsProcessEnv(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	t.Setenv("NINESH_TEST_INHERITED_VAR", "inherited-value")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "echo -n $NINESH_TEST_INHERITED_VAR"`, env)
	if string(v.(value.Bytes)) != "inherited-value" {
		t.Fatalf("stdout = %q, want %q", v, "inherited-value")
	}
}

func TestForegroundExternalCallStdinRoutedThroughJob(t *testing.T) {
	skipUnlessOnPath(t, "cat")
	env := jobsEnv(t)
	v := runEnv(t, `"piped in" | %cat`, env)
	if string(v.(value.Bytes)) != "piped in\n" {
		t.Fatalf("stdout = %q, want %q", v, "piped in\n")
	}
}

func TestForegroundExternalCallBadCommandStillErrorVal(t *testing.T) {
	env := jobsEnv(t)
	v := run(t, `%this-command-does-not-exist-9sh`) // no namespace: direct-exec fallback path
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("direct-exec path: want ErrorVal, got %#v", v)
	}
	v2 := runEnv(t, `%this-command-does-not-exist-9sh`, env) // job-routed path
	if _, ok := v2.(value.ErrorVal); !ok {
		t.Fatalf("job-routed path: want ErrorVal, got %#v", v2)
	}
}

// TestForegroundExternalCallForwardsStderr locks in the fix for stderr
// silently vanishing on a bare foreground %cmd: job.go captures a
// subprocess's stderr into its own growBuf (same as stdout), so unlike
// the no-namespace direct-exec fallback (which wires cmd.Stderr =
// os.Stderr live), the job-routed path has to explicitly read it back
// and forward it after the job finishes -- see runExternalViaJob.
func TestForegroundExternalCallForwardsStderr(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	v := runEnv(t, `%sh "-c" "echo -n out-text; echo -n err-text 1>&2"`, env)
	os.Stderr = origStderr
	w.Close()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stderr: %v", err)
	}

	if string(v.(value.Bytes)) != "out-text" {
		t.Fatalf("stdout = %q, want %q", v, "out-text")
	}
	if string(captured) != "err-text" {
		t.Fatalf("stderr forwarded = %q, want %q", captured, "err-text")
	}
}

func TestForegroundExternalCallNonzeroExitIsNotAnError(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	v := runEnv(t, `%sh "-c" "exit 3"`, env)
	if _, ok := v.(value.ErrorVal); ok {
		t.Fatalf("a nonzero exit should be ordinary data, not ErrorVal, got %#v", v)
	}
	if _, ok := v.(value.Bytes); !ok {
		t.Fatalf("want value.Bytes, got %T", v)
	}
}

func TestHostBuiltin(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	v := run(t, `host()`)
	s, ok := v.(value.String)
	if !ok {
		t.Fatalf("host() = %#v, want a string", v)
	}
	if string(s) != want {
		t.Fatalf("host() = %q, want %q", s, want)
	}
}
