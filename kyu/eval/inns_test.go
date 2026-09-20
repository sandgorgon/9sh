package eval

import (
	"os"
	"strings"
	"testing"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/parser"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func inNSEnv(t *testing.T) (*Env, string) {
	t.Helper()
	env, dir := globEnv(t)
	if err := env.Namespace().BindFS(ns.NewBindsFS(env.Namespace()), "", "/ns", ns.Replace); err != nil {
		t.Fatal(err)
	}
	return env, dir
}

func bindDsts(t *testing.T, env *Env) string {
	t.Helper()
	var out []string
	for _, el := range runEnv(t, `binds()`, env).(*value.List).Elems {
		d, _ := el.(*value.Record).Get("dst")
		out = append(out, d.String())
	}
	return strings.Join(out, " ")
}

func TestInNSBindsDoNotLeak(t *testing.T) {
	env, dir := inNSEnv(t)
	other := t.TempDir()
	os.WriteFile(other+"/only-here.txt", []byte("inner"), 0644)
	before := bindDsts(t, env)

	v := runEnv(t, `in_ns {
		bind dir("`+other+`"), /scratch
		unbind /testdir
		bind dir("`+other+`"), /testdir
		[cat(/scratch/only-here.txt), cat(/testdir/only-here.txt), binds() | count]
	}`, env)
	got := v.(*value.List)
	if got.Elems[0] != value.String("inner") || got.Elems[1] != value.String("inner") {
		t.Fatalf("block should see its own binds, got %v", got)
	}
	if after := bindDsts(t, env); after != before {
		t.Fatalf("binds leaked out of in_ns:\nbefore: %s\nafter:  %s", before, after)
	}
	// The original /testdir is back, not the block's rebind.
	os.WriteFile(dir+"/orig.txt", []byte("outer"), 0644)
	if got := runEnv(t, `cat(/testdir/orig.txt)`, env); got != value.String("outer") {
		t.Fatalf("original /testdir not restored, cat = %#v", got)
	}
	if _, ok := runEnv(t, `cat(/scratch/only-here.txt)`, env).(value.ErrorVal); !ok {
		t.Error("/scratch should not exist after the block")
	}
}

func TestInNSSeesEverythingAlreadyBoundAndSharesWrites(t *testing.T) {
	env, dir := inNSEnv(t)
	os.WriteFile(dir+"/f.txt", []byte("v1"), 0644)
	v := runEnv(t, `in_ns {
		write(/testdir/f.txt, "v2")
		cat(/testdir/f.txt)
	}`, env)
	if v != value.String("v2") {
		t.Fatalf("block value = %#v", v)
	}
	// A bind is a view: the write reached the real directory.
	if got := readReal(t, dir+"/f.txt"); got != "v2" {
		t.Fatalf("write inside in_ns did not reach the shared directory: %q", got)
	}
}

func TestInNSRestoresOnErrorAndBreak(t *testing.T) {
	env, _ := inNSEnv(t)
	before := bindDsts(t, env)

	err := runEnvErr(t, `in_ns {
		bind /testdir, /temp
		unbind /never/bound
	}`, env)
	if err == nil {
		t.Fatal("expected the failing unbind to abort")
	}
	if after := bindDsts(t, env); after != before {
		t.Fatalf("namespace not restored after an error:\nbefore: %s\nafter:  %s", before, after)
	}

	runEnv(t, `while true {
		in_ns {
			bind /testdir, /temp2
			break
		}
	}`, env)
	if after := bindDsts(t, env); after != before {
		t.Fatalf("namespace not restored after break out of in_ns:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestInNSNestsAndSeesItsOwnNS(t *testing.T) {
	env, _ := inNSEnv(t)
	got := runEnv(t, `in_ns {
		bind /testdir, /outer
		in_ns {
			bind /testdir, /inner
			unbind /outer
		}
		binds() | count
	}`, env)
	// Inside the outer block after the inner one ends: /outer is still
	// bound, /inner is gone. Count = jobs, testdir, ns, outer.
	outerCount := runEnv(t, `binds() | count`, env)
	if got.(value.Int) != outerCount.(value.Int)+1 {
		t.Fatalf("outer block sees %v binds, original has %v; want exactly one more (/outer)", got, outerCount)
	}
	// /ns inside a block reports the block's namespace.
	txt := runEnv(t, `in_ns {
		bind /testdir, /marker
		cat(/ns/binds)
	}`, env).(value.String)
	if !strings.Contains(string(txt), "/marker") {
		t.Fatalf("/ns/binds inside in_ns should list the block's own binds:\n%s", txt)
	}
	if strings.Contains(string(runEnv(t, `cat(/ns/binds)`, env).(value.String)), "/marker") {
		t.Fatal("original /ns/binds shows the block's bind")
	}
}

func TestInNSJobsAreSharedWithTheOuterNamespace(t *testing.T) {
	skipUnlessOnPath(t, "echo")
	env, _ := jobsEnvWithManager(t)
	runEnv(t, `in_ns { %echo "from inside" }`, env)
	if n := runEnv(t, `ps() | count`, env); n != value.Int(1) {
		t.Fatalf("a job run inside in_ns should be visible to ps() afterwards, got %v", n)
	}
}

func TestInNSParses(t *testing.T) {
	p := parser.New(`in_ns { x := 1 }`)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatal(p.Errors())
	}
	if _, ok := prog.Stmts[0].(*ast.ExprStmt).X.(*ast.InNS); !ok {
		t.Fatalf("want InNS, got %#v", prog.Stmts[0])
	}
	bad := parser.New(`in_ns x`)
	bad.ParseProgram()
	if len(bad.Errors()) == 0 {
		t.Error("`in_ns` without a block should be a parse error")
	}
}
