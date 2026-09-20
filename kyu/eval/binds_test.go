package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

func bindsEnv(t *testing.T) *Env {
	t.Helper()
	n := ns.New()
	if err := n.BindFS(ns.NewBindsFS(n), "", "/ns", ns.Replace); err != nil {
		t.Fatal(err)
	}
	return NewGlobalEnv(n)
}

func TestBindsReportsBootstrapAndUserBinds(t *testing.T) {
	dir := t.TempDir()
	env := bindsEnv(t)
	runEnv(t, `bind dir("`+dir+`"), /work`, env)
	runEnv(t, `bind /work, /alias`, env)
	runEnv(t, `bind /work, /alias, after`, env)

	lst, ok := runEnv(t, `binds()`, env).(*value.List)
	if !ok {
		t.Fatal("binds() should return a List")
	}
	type row struct{ dst, src, disp string }
	var got []row
	for _, e := range lst.Elems {
		r := e.(*value.Record)
		dst, _ := r.Get("dst")
		src, _ := r.Get("src")
		disp, _ := r.Get("disp")
		s := ""
		if str, ok := src.(value.String); ok {
			s = string(str)
		} else if _, ok := src.(value.Null); !ok {
			t.Fatalf("src should be String or Null, got %#v", src)
		}
		got = append(got, row{string(dst.(value.Path)), s, string(disp.(value.String))})
	}
	want := []row{
		{"/ns", "", "replace"},
		{"/work", `dir("` + dir + `")`, "replace"},
		{"/alias", "/work", "replace"},
		{"/alias", "/work", "after"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestBindsRejectsArguments(t *testing.T) {
	env := bindsEnv(t)
	if _, err := biBinds(env, []value.Value{value.Int(1)}); err == nil {
		t.Fatal("binds(1) should be an error")
	}
}

func TestBindsWithoutNSMountIsErrorValue(t *testing.T) {
	env := NewGlobalEnv(ns.New())
	v := runEnv(t, `binds()`, env)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal when /ns isn't bound, got %#v", v)
	}
}
