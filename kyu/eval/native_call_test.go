package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

// TestNativeCallExecutesLikePercentCmd confirms bareword syntax reaches
// the exact same execution path %cmd does -- same job-tracked
// runExternalViaJob, same value.Bytes stdout result.
func TestNativeCallExecutesLikePercentCmd(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	markNative(env, "sh")
	v := runEnvNative(t, `sh "-c" "echo hi"`, env)
	got, ok := v.(value.Bytes)
	if !ok {
		t.Fatalf("got %#v (%s), want value.Bytes", v, v.Kind())
	}
	if string(got) != "hi\n" {
		t.Errorf("stdout = %q, want %q", string(got), "hi\n")
	}
}

// TestNativeCallReceivesPipedInput confirms a native call composes into
// a pipe's right-hand side with real piped data reaching the process,
// not just parsing correctly (see kyu/parser's TestNativeCallAsPipeRHS
// for the parse-only half of this).
func TestNativeCallReceivesPipedInput(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	markNative(env, "sh")
	v := runEnvNative(t, `"hello from the pipe" | sh "-c" "cat"`, env)
	got, ok := v.(value.Bytes)
	if !ok {
		t.Fatalf("got %#v (%s), want value.Bytes", v, v.Kind())
	}
	// renderForExternal appends a trailing newline when rendering a
	// piped String for a subprocess's stdin -- unrelated to native-call
	// handling, just what any %cmd's piped input already does too.
	if string(got) != "hello from the pipe\n" {
		t.Errorf("stdout = %q, want %q", string(got), "hello from the pipe\n")
	}
}

// TestNativeCallInsideEachClosure confirms a native call runs correctly
// inside a closure body ({ |v| ... }, the each/where/etc. shape) -- see
// kyu/parser's TestNativeCallInsideClosure for the parse-only half.
func TestNativeCallInsideEachClosure(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env := jobsEnv(t)
	markNative(env, "sh")
	v := runEnvNative(t, `["a", "b"] | each { |v| sh "-c" "echo -n prefix-$0" v }`, env)
	list, ok := v.(*value.List)
	if !ok {
		t.Fatalf("got %#v (%s), want a List", v, v.Kind())
	}
	if len(list.Elems) != 2 {
		t.Fatalf("got %d elements, want 2", len(list.Elems))
	}
	want := []string{"prefix-a", "prefix-b"}
	for i, elem := range list.Elems {
		b, ok := elem.(value.Bytes)
		if !ok {
			t.Fatalf("element %d = %#v, want value.Bytes", i, elem)
		}
		if string(b) != want[i] {
			t.Errorf("element %d = %q, want %q", i, string(b), want[i])
		}
	}
}

// TestNativeCallNamespaceOnlyPathPassedThroughLiterally is 6f's actual
// functional payoff, updated for the post-9ed-v0.8.0 contract: a
// namespace-only Path argument (the literal string "/src/greeting.txt"
// isn't itself a real OS path, even though it maps to a real file
// through the /src bind -- see checkNamespaceOnlyPath's own doc
// comment) errors for an ordinary %cmd
// (TestExternalCallNamespaceOnlyPathIsGuardedErrorVal), but must reach a
// native program as its literal path text, untouched -- no checkout, no
// scratch copy. A real native program (9ed) dials 9sh's namespace socket
// itself and does its own Walk/fallback resolution; 9sh's job here is
// only to not get in the way by rewriting the argument first. Uses sh,
// not cat, as the "native" name: cat is already a real kyu builtin
// (kyu/eval/cat.go's biCat), and isNativeProgram's own precedence rule
// (an existing identifier always wins) would silently refuse to treat
// it as a native call at all -- exactly as intended, but wrong for this
// test's purpose.
//
// The namespace path is assigned to a variable first (see
// kyu/lexer's TestPathAsExternalCallFirstArg: a bareword Path after a
// non-Path argument hits a known, pre-existing, deliberately unfixed
// lexer gap shared with %cmd).
func TestNativeCallNamespaceOnlyPathPassedThroughLiterally(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env, _ := dirfsEnv(t)
	markNative(env, "sh")
	v := runEnvNative(t, "p := /src/greeting.txt\nsh \"-c\" \"echo -n $0\" p", env)
	got, ok := v.(value.Bytes)
	if !ok {
		t.Fatalf("got %#v (%s), want value.Bytes", v, v.Kind())
	}
	if string(got) != "/src/greeting.txt" {
		t.Errorf("argv[0] as seen by the native program = %q, want the literal namespace path %q (no checkout/materialize should happen for a native call)", string(got), "/src/greeting.txt")
	}
}
