package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sandgorgon/9p/examples/dirfs"

	"github.com/sandgorgon/9sh/ns"
)

// dirfsEnv binds a real dirfs-backed temp directory at /src, the primary
// real-world checkout scenario (a legacy tool editing an actual file).
// It returns the env and the temp directory's real path for direct
// assertions against the underlying filesystem.
func dirfsEnv(t *testing.T) (*Env, string) {
	t.Helper()
	dir := t.TempDir()
	fs, err := dirfs.New(dir)
	if err != nil {
		t.Fatalf("dirfs.New: %v", err)
	}
	namespace := ns.New()
	if err := namespace.BindFS(fs, "", "/src", ns.Replace); err != nil {
		t.Fatalf("bind /src: %v", err)
	}
	return NewGlobalEnv(namespace), dir
}

func TestCheckoutSingleFileWriteBack(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env, realDir := dirfsEnv(t)
	if err := os.WriteFile(filepath.Join(realDir, "greeting.txt"), []byte("original"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	runEnv(t, `checkout(/src/greeting.txt, { |p| `+
		`%sh "-c" "echo -n newcontent > \"$1\"" "sh" p })`, env)

	got, err := os.ReadFile(filepath.Join(realDir, "greeting.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "newcontent" {
		t.Fatalf("real file content = %q, want %q", got, "newcontent")
	}
}

func TestCheckoutNoOpLeavesFileUnchanged(t *testing.T) {
	env, realDir := dirfsEnv(t)
	if err := os.WriteFile(filepath.Join(realDir, "f.txt"), []byte("stays"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	runEnv(t, `checkout(/src/f.txt, { |p| p })`, env)

	got, err := os.ReadFile(filepath.Join(realDir, "f.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "stays" {
		t.Fatalf("content = %q, want unchanged 'stays'", got)
	}
}

func TestCheckoutReturnsClosureResult(t *testing.T) {
	env, realDir := dirfsEnv(t)
	os.WriteFile(filepath.Join(realDir, "f.txt"), []byte("x"), 0644)

	v := runEnv(t, `checkout(/src/f.txt, { |p| "closure-result" })`, env)
	if v.Kind() != "string" || v.String() != "closure-result" {
		t.Fatalf("checkout's return value = %#v, want pass-through of the closure's result", v)
	}
}

func TestCheckoutDirectoryWriteBackModifiedFile(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env, realDir := dirfsEnv(t)
	if err := os.Mkdir(filepath.Join(realDir, "proj"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "proj", "a.txt"), []byte("aaa"), 0644); err != nil {
		t.Fatalf("seed a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "proj", "b.txt"), []byte("bbb"), 0644); err != nil {
		t.Fatalf("seed b.txt: %v", err)
	}

	runEnv(t, `checkout(/src/proj, { |dir|
  target := dir
  %sh "-c" "echo -n changed > \"$1/a.txt\"" "sh" target
})`, env)

	gotA, _ := os.ReadFile(filepath.Join(realDir, "proj", "a.txt"))
	if string(gotA) != "changed" {
		t.Fatalf("a.txt = %q, want changed", gotA)
	}
	gotB, _ := os.ReadFile(filepath.Join(realDir, "proj", "b.txt"))
	if string(gotB) != "bbb" {
		t.Fatalf("b.txt = %q, want unchanged bbb (only a.txt was touched)", gotB)
	}
}

func TestCheckoutDirectoryWriteBackNewFile(t *testing.T) {
	skipUnlessOnPath(t, "sh")
	env, realDir := dirfsEnv(t)
	if err := os.Mkdir(filepath.Join(realDir, "proj"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	runEnv(t, `checkout(/src/proj, { |dir|
  %sh "-c" "echo -n fresh > \"$1/new.txt\"" "sh" dir
})`, env)

	got, err := os.ReadFile(filepath.Join(realDir, "proj", "new.txt"))
	if err != nil {
		t.Fatalf("new file wasn't written back: %v", err)
	}
	if string(got) != "fresh" {
		t.Fatalf("new.txt = %q, want fresh", got)
	}
}

func TestCheckoutWithoutNamespaceErrors(t *testing.T) {
	runErr(t, `checkout(/src/f, { |p| p })`)
}

func TestCheckoutRejectsNonPathFirstArg(t *testing.T) {
	env, _ := dirfsEnv(t)
	runEnvErr(t, `checkout("not-a-path", { |p| p })`, env)
}

func TestCheckoutRejectsNonClosureSecondArg(t *testing.T) {
	env, _ := dirfsEnv(t)
	runEnvErr(t, `checkout(/src, 5)`, env)
}

func TestCheckoutErrorsOnMissingSource(t *testing.T) {
	env, _ := dirfsEnv(t)
	runEnvErr(t, `checkout(/src/does-not-exist, { |p| p })`, env)
}

func TestCheckoutPropagatesClosureError(t *testing.T) {
	env, realDir := dirfsEnv(t)
	os.WriteFile(filepath.Join(realDir, "f.txt"), []byte("x"), 0644)
	runEnvErr(t, `checkout(/src/f.txt, { |p| error("boom")? })`, env)
}
