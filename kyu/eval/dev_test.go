package eval

import (
	"os"
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// ls/stat's dev, which_bind's dev and binds()'s dev are one number: the
// id of the layer serving the file, so an entry can be matched to its bind.
func TestDevTiesLsStatWhichBindAndBindsTogether(t *testing.T) {
	env, dir := globEnv(t)
	if err := env.Namespace().BindFS(ns.NewBindsFS(env.Namespace()), "", "/ns", ns.Replace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/f.txt", []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	intOf := func(src, field string) int64 {
		t.Helper()
		v := runEnv(t, src, env)
		rec, ok := v.(*value.Record)
		if !ok {
			t.Fatalf("%s = %#v, want a Record", src, v)
		}
		f, _ := rec.Get(field)
		n, ok := f.(value.Int)
		if !ok {
			t.Fatalf("%s .%s = %#v, want an Int", src, field, f)
		}
		return int64(n)
	}

	viaStat := intOf(`stat(/testdir/f.txt)`, "dev")
	viaWhich := intOf(`which_bind(/testdir/f.txt)`, "dev")
	if viaStat == 0 || viaStat != viaWhich {
		t.Fatalf("stat dev = %d, which_bind dev = %d, want equal and nonzero", viaStat, viaWhich)
	}

	ls := runEnv(t, `ls("/testdir/*")`, env).(*value.List)
	if len(ls.Elems) != 1 {
		t.Fatalf("ls returned %d entries, want 1", len(ls.Elems))
	}
	if d, _ := ls.Elems[0].(*value.Record).Get("dev"); int64(d.(value.Int)) != viaStat {
		t.Errorf("ls dev = %v, want %d", d, viaStat)
	}

	binds := runEnv(t, `binds(/testdir)`, env).(*value.List)
	if d, _ := binds.Elems[0].(*value.Record).Get("dev"); int64(d.(value.Int)) != viaStat {
		t.Errorf("binds() dev for /testdir = %v, want %d", d, viaStat)
	}

	// A purely synthetic directory has no serving layer.
	if d := intOf(`stat(/)`, "dev"); d != 0 {
		t.Errorf("stat(/) dev = %d, want 0", d)
	}
}
