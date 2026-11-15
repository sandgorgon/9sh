package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestWrapNotAvailableWithoutHook(t *testing.T) {
	env := NewGlobalEnv(nil)
	if err := runEnvErr(t, `wrap()`, env); err == nil {
		t.Fatalf("expected a hard error, got none")
	}
}

func TestWrapGetsAndSetsThroughHook(t *testing.T) {
	env := NewGlobalEnv(nil)
	on := true
	env.SetWrapAccess(&WrapAccess{Get: func() bool { return on }, Set: func(v bool) { on = v }})
	if got := runEnv(t, `wrap()`, env); got != value.Bool(true) {
		t.Fatalf("wrap() = %v, want true", got)
	}
	if got := runEnv(t, `wrap(false)`, env); got.Kind() != "null" {
		t.Fatalf("wrap(false) = %v, want null", got)
	}
	if on {
		t.Fatal("wrap(false) didn't reach the hook")
	}
}

func TestWrapRejectsBadArguments(t *testing.T) {
	env := NewGlobalEnv(nil)
	env.SetWrapAccess(&WrapAccess{Get: func() bool { return true }, Set: func(bool) {}})
	for _, src := range []string{`wrap("no")`, `wrap(1)`, `wrap(true, false)`} {
		if err := runEnvErr(t, src, env); err == nil {
			t.Errorf("%s: expected an error, got none", src)
		}
	}
}
