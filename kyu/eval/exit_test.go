package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestExitAndQuitUnwindAsExitSignal(t *testing.T) {
	for _, src := range []string{
		`exit()`,
		`quit()`,
		// From inside a loop, a branch, a closure, and a pipeline stage:
		// nothing along the way may swallow it or turn it into a
		// different error.
		`while true { exit() }`,
		`if true { quit() }`,
		`f := { exit() }
f()`,
		`[1, 2] | where({ |x| exit() })`,
	} {
		env := NewGlobalEnv(nil)
		if err := runEnvErr(t, src, env); !IsExit(err) {
			t.Errorf("%q: err = %v, want an ExitSignal", src, err)
		}
	}
}

func TestExitStopsBeforeTheRest(t *testing.T) {
	env := NewGlobalEnv(nil)
	runEnvErr(t, "x := 1\nexit()\nx := 2", env)
	if v, _ := env.Get("x"); v != value.Int(1) {
		t.Fatalf("x = %v, want 1 (nothing after exit() may run)", v)
	}
}

func TestExitStatusAndReason(t *testing.T) {
	for src, want := range map[string]ExitSignal{
		`exit()`:            {},
		`exit(0)`:           {},
		`exit(3)`:           {Code: 3},
		`quit(255)`:         {Code: 255},
		`exit("")`:          {},
		`exit("disk full")`: {Code: 1, Reason: "disk full"},
	} {
		sig, ok := AsExit(runEnvErr(t, src, NewGlobalEnv(nil)))
		if !ok || sig != want {
			t.Errorf("%s = %+v (exit %v), want %+v", src, sig, ok, want)
		}
	}
}

func TestExitRejectsBadArguments(t *testing.T) {
	// 256 would wrap to 0 as a Unix status -- a failure reading as
	// success -- so out-of-range is an error, not an exit.
	for _, src := range []string{`exit(256)`, `exit(-1)`, `exit(true)`, `exit(1, "x")`} {
		err := runEnvErr(t, src, NewGlobalEnv(nil))
		if err == nil || IsExit(err) {
			t.Errorf("%s: err = %v, want an argument error, not an exit", src, err)
		}
	}
}

// TestExitInSourcedFileEndsOnlyThatFile: exit() stops the file being
// sourced, and the caller carries on.
func TestExitInSourcedFileEndsOnlyThatFile(t *testing.T) {
	dir := t.TempDir()
	writeKy(t, dir, "a.ky", "a := 1\nexit()\na := 2\n")
	env := sourceEnv(t, dir)
	v := runEnv(t, "source(/src/a.ky)\nafter := true\na", env)
	if v != value.Int(1) {
		t.Fatalf("a = %v, want 1 (the sourced file must stop at exit())", v)
	}
	if after, _ := env.Get("after"); after != value.Bool(true) {
		t.Fatal("the caller must keep running after a sourced file calls exit()")
	}
}

// TestExitInBackgroundJobEndsJobQuietly: a background job that calls
// exit() just ends — no error text on its stderr.
func TestExitInBackgroundJobEndsJobQuietly(t *testing.T) {
	env := jobsEnv(t)
	v := runEnv(t, `j := { exit(); "unreached" } &
j | wait
[j.stdout, j.stderr]`, env)
	for i, out := range v.(*value.List).Elems {
		if b, ok := out.(value.Bytes); !ok || len(b) != 0 {
			t.Errorf("%s = %#v, want empty", []string{"stdout", "stderr"}[i], out)
		}
	}
}
