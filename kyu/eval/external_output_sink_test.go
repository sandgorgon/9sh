package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

// TestForegroundCommandStderrGoesToRegisteredSink is the regression test
// for the real bug fixed alongside Env.SetExternalOutputSink: a
// foreground %cmd's stderr used to be written straight to os.Stderr
// unconditionally in runExternalViaJob, corrupting replui's TUI (which
// owns the real terminal via its own diffed renderer) whenever a
// command wrote to stderr. With a sink registered, that content must go
// to the sink instead.
func TestForegroundCommandStderrGoesToRegisteredSink(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	var got []byte
	env.SetExternalOutputSink(func(stderr []byte) { got = stderr })
	defer env.SetExternalOutputSink(nil)

	runEnv(t, `%sh "-c" "echo to stderr >&2"`, env)

	if string(got) != "to stderr\n" {
		t.Errorf("sink got %q, want %q", got, "to stderr\n")
	}
}

// TestForegroundCommandStdoutUnaffectedBySink confirms the sink only
// intercepts stderr -- stdout keeps flowing through the ordinary
// value.Bytes return path (see ExternalOutputSinkFunc's own doc comment
// for why stdout was never routed through the sink at all).
func TestForegroundCommandStdoutUnaffectedBySink(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	env.SetExternalOutputSink(func(stderr []byte) {})
	defer env.SetExternalOutputSink(nil)

	v := runEnv(t, `%sh "-c" "echo to stdout"`, env)
	got, ok := v.(value.Bytes)
	if !ok {
		t.Fatalf("got %#v, want value.Bytes", v)
	}
	if string(got) != "to stdout\n" {
		t.Errorf("stdout = %q, want %q", got, "to stdout\n")
	}
}
