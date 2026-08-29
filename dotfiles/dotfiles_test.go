package dotfiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sandgorgon/9sh/kyu/eval"
	"github.com/sandgorgon/9sh/kyu/value"
)

// withConfigDir points Dir() at a fresh temp dir for the duration of the
// test by overriding $HOME (Dir is ~/.config/9/ns).
func withConfigDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".config", "9", "ns")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingDirIsNoop(t *testing.T) {
	withConfigDir(t)
	env := eval.NewGlobalEnv(nil)
	Load(env) // must not panic or error out
	if _, ok := env.Get("x"); ok {
		t.Fatal("expected no bindings from a nonexistent dotfiles dir")
	}
}

func TestLoadCommonKy(t *testing.T) {
	dir := withConfigDir(t)
	writeFile(t, filepath.Join(dir, "common.ky"), `greeting := "hello from common"`)

	env := eval.NewGlobalEnv(nil)
	Load(env)

	v, ok := env.Get("greeting")
	if !ok {
		t.Fatal("expected common.ky's binding to be visible after Load")
	}
	if v.(value.String) != "hello from common" {
		t.Fatalf("greeting = %v", v)
	}
}

func TestLoadHostSpecificKy(t *testing.T) {
	dir := withConfigDir(t)
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	writeFile(t, filepath.Join(dir, "hosts", host+".ky"), `only_here := true`)

	env := eval.NewGlobalEnv(nil)
	Load(env)

	v, ok := env.Get("only_here")
	if !ok {
		t.Fatal("expected the host-specific file's binding to be visible after Load")
	}
	if v != value.Bool(true) {
		t.Fatalf("only_here = %v", v)
	}
}

func TestLoadOrderHostOverridesCommon(t *testing.T) {
	dir := withConfigDir(t)
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	writeFile(t, filepath.Join(dir, "common.ky"), `x := "common"`)
	writeFile(t, filepath.Join(dir, "hosts", host+".ky"), `x := "host"`)

	env := eval.NewGlobalEnv(nil)
	Load(env)

	v, _ := env.Get("x")
	if v.(value.String) != "host" {
		t.Fatalf("x = %v, want the host-specific file to win (loaded after common.ky)", v)
	}
}

func TestLoadBrokenCommonDoesNotBlockHostFile(t *testing.T) {
	dir := withConfigDir(t)
	host, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	writeFile(t, filepath.Join(dir, "common.ky"), `this is not valid kyu {{{`)
	writeFile(t, filepath.Join(dir, "hosts", host+".ky"), `still_loads := true`)

	env := eval.NewGlobalEnv(nil)
	Load(env) // a broken common.ky must not panic or prevent hosts/*.ky from loading

	v, ok := env.Get("still_loads")
	if !ok {
		t.Fatal("expected the host-specific file to load despite common.ky being broken")
	}
	if v != value.Bool(true) {
		t.Fatalf("still_loads = %v", v)
	}
}

func TestLoadUsesHostBuiltin(t *testing.T) {
	dir := withConfigDir(t)
	writeFile(t, filepath.Join(dir, "common.ky"), `matches := host() == host()`)

	env := eval.NewGlobalEnv(nil)
	Load(env)

	v, ok := env.Get("matches")
	if !ok || v != value.Bool(true) {
		t.Fatalf("matches = %v, ok = %v", v, ok)
	}
}
