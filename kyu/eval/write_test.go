package eval

import (
	"os"
	"testing"
	"time"

	"github.com/sandgorgon/9sh/kyu/value"
)

func readReal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteCreatesAndReplaces(t *testing.T) {
	env, dir := globEnv(t)
	runEnv(t, `write(/testdir/a.txt, "hello")`, env)
	if got := readReal(t, dir+"/a.txt"); got != "hello" {
		t.Fatalf("after create = %q", got)
	}
	runEnv(t, `write(/testdir/a.txt, "hi")`, env) // shorter: must truncate
	if got := readReal(t, dir+"/a.txt"); got != "hi" {
		t.Fatalf("after replace = %q, want %q", got, "hi")
	}
	if got := runEnv(t, `cat(/testdir/a.txt)`, env); got != value.String("hi") {
		t.Fatalf("cat = %#v", got)
	}
}

func TestAppendCreatesAndAdds(t *testing.T) {
	env, dir := globEnv(t)
	runEnv(t, `append(/testdir/log.txt, "one\n")`, env)
	runEnv(t, `append(/testdir/log.txt, "two\n")`, env)
	if got := readReal(t, dir+"/log.txt"); got != "one\ntwo\n" {
		t.Fatalf("append result = %q", got)
	}
}

func TestWriteErrorsAreErrorVals(t *testing.T) {
	env, dir := globEnv(t)
	os.Mkdir(dir+"/d", 0755)
	for _, src := range []string{
		`write(/testdir/d, "x")`,           // directory
		`write(/testdir/nodir/f.txt, "x")`, // missing parent
		`write(/, "x")`,                    // root
		`append(/testdir/d, "x")`,          // directory
		`write(/jobs/1/status, "x")`,       // read-only synthetic file
	} {
		if _, ok := runEnv(t, src, env).(value.ErrorVal); !ok {
			t.Errorf("%s: want ErrorVal", src)
		}
	}
}

func TestWriteArgumentChecks(t *testing.T) {
	env, _ := globEnv(t)
	for _, args := range [][]value.Value{
		{value.Path("/testdir/a")},
		{value.String("/testdir/a"), value.String("x")}, // path required
		{value.Path("/testdir/a"), value.Int(1)},        // string required
	} {
		if _, err := biWrite(env, args); err == nil {
			t.Errorf("write(%v) should be an error", args)
		}
	}
}

func TestWriteReachesJobCtl(t *testing.T) {
	skipUnlessOnPath(t, "sleep")
	env, _ := jobsEnvWithManager(t)
	runEnv(t, `%sleep "30" &`, env)
	jobs := runEnv(t, `ps()`, env).(*value.List)
	id, _ := jobs.Elems[0].(*value.Record).Get("id")
	if v := runEnv(t, `write(/jobs/`+id.String()+`/ctl, "kill")`, env); v != (value.Null{}) {
		t.Fatalf("write to ctl = %#v", v)
	}
	// The kill lands asynchronously; wait for a terminal state rather than
	// asserting on the instant after the write.
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, _ := runEnv(t, `ps()`, env).(*value.List).Elems[0].(*value.Record).Get("state")
		if state.String() != "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s after kill", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
