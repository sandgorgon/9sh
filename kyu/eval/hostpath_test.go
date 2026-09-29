package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestHostPathResolvesDirAndPathBinds(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := bindsEnv(t)
	runEnv(t, `bind dir("`+dir+`"), /work`, env)
	runEnv(t, `bind /work, /alias`, env)

	for _, c := range []struct{ expr, want string }{
		{`host_path(/work/sub/f.txt)`, filepath.Join(dir, "sub", "f.txt")},
		{`host_path(/work/sub)`, filepath.Join(dir, "sub")},
		{`host_path(/work)`, dir}, // the bind point itself
		{`host_path(/alias/sub/f.txt)`, filepath.Join(dir, "sub", "f.txt")},
	} {
		got, ok := runEnv(t, c.expr, env).(value.String)
		if !ok || string(got) != c.want {
			t.Errorf("%s = %#v, want %q", c.expr, got, c.want)
		}
	}
}

func TestHostPathErrorsWithoutHostDir(t *testing.T) {
	env := bindsEnv(t)
	for _, c := range []struct{ expr, sub string }{
		{`host_path(/ns/binds)`, "no host path"}, // built-in filesystem
		{`host_path(/nowhere)`, "host_path:"},    // unresolvable
	} {
		e, ok := runEnv(t, c.expr, env).(value.ErrorVal)
		if !ok || !strings.Contains(e.Msg, c.sub) {
			t.Errorf("%s = %#v, want ErrorVal containing %q", c.expr, e, c.sub)
		}
	}
}

func TestHostPathRejectsBadArguments(t *testing.T) {
	env := bindsEnv(t)
	for _, src := range []string{`host_path()`, `host_path("/x")`, `host_path(/a, /b)`} {
		runEnvErr(t, src, env) // fails the test itself if there is no error
	}
}
