package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

// TestExternalCallNamespaceOnlyPathIsGuardedErrorVal locks in
// checkNamespaceOnlyPath's whole reason for existing: /src/greeting.txt
// resolves in the namespace (dirfsEnv binds /src to realDir) but the
// literal string "/src/greeting.txt" isn't a real OS path, so a legacy
// %cat would otherwise get a confusing ENOENT from inside its own
// process instead of a clear kyu-level hint. Written as a direct bareword
// argument (%cat /src/greeting.txt) rather than through a variable --
// this exercises lexSlashOrPath's lastWasExternalName exemption
// (kyu/lexer) at the same time, so a regression in either fix shows up
// here.
func TestExternalCallNamespaceOnlyPathIsGuardedErrorVal(t *testing.T) {
	env, realDir := dirfsEnv(t)
	if err := os.WriteFile(filepath.Join(realDir, "greeting.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	v := runEnv(t, `%cat /src/greeting.txt`, env)
	ev, ok := v.(value.ErrorVal)
	if !ok {
		t.Fatalf("want ErrorVal, got %#v", v)
	}
	if !strings.Contains(ev.Msg, "namespace path") || !strings.Contains(ev.Msg, "checkout(") {
		t.Fatalf("error = %q, want it to mention the namespace-path mistake and checkout", ev.Msg)
	}
}

// TestExternalCallRealPathUnaffectedByGuardrail is the regression guard
// for the naive version of this check: a genuinely real absolute path
// must pass straight through, unchanged, even though it's typed
// identically (value.Path) to a namespace path.
func TestExternalCallRealPathUnaffectedByGuardrail(t *testing.T) {
	skipUnlessOnPath(t, "cat")
	env, _ := dirfsEnv(t)
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(realFile, []byte("real-content"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	v := runEnv(t, fmt.Sprintf("%%cat %s", realFile), env)
	b, ok := v.(value.Bytes)
	if !ok {
		t.Fatalf("want Bytes, got %#v", v)
	}
	if string(b) != "real-content" {
		t.Fatalf("stdout = %q, want %q", b, "real-content")
	}
}

// TestExternalCallOrdinaryBadPathUnaffectedByGuardrail is the other
// regression guard: a path that's neither real nor bound anywhere in
// the namespace (an ordinary typo) must fall through unchanged too --
// the external tool's own "no such file" failure, not this guardrail's
// message. A nonzero exit is ordinary data (runExternal's own
// contract), not an ErrorVal, so this must NOT be an ErrorVal at all.
func TestExternalCallOrdinaryBadPathUnaffectedByGuardrail(t *testing.T) {
	skipUnlessOnPath(t, "cat")
	env, _ := dirfsEnv(t)
	v := runEnv(t, `%cat /nowhere-9sh-guardrail-test`, env)
	if _, ok := v.(value.ErrorVal); ok {
		t.Fatalf("want ordinary Bytes (nonzero exit is not an error here), got ErrorVal %#v", v)
	}
}

// TestExternalCallGuardrailSkippedWithoutNamespace locks in that the
// guard only applies once a namespace is attached -- bare eval-package
// tests (no namespace) go through runExternalDirect, which has nothing
// to walk against.
func TestExternalCallGuardrailSkippedWithoutNamespace(t *testing.T) {
	skipUnlessOnPath(t, "cat")
	v := run(t, `%cat /nowhere-9sh-guardrail-test`) // no namespace: direct-exec fallback
	if _, ok := v.(value.ErrorVal); ok {
		t.Fatalf("want ordinary Bytes (guardrail skipped, nonzero exit is not an error here), got ErrorVal %#v", v)
	}
}
