package eval

import (
	"strings"
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

func TestBindsRejectsBadArguments(t *testing.T) {
	env := bindsEnv(t)
	if _, err := biBinds(env, []value.Value{value.Int(1)}); err == nil {
		t.Fatal("binds(1) should be an error")
	}
	if _, err := biBinds(env, []value.Value{value.Path("/a"), value.Path("/b")}); err == nil {
		t.Fatal("binds(/a, /b) should be an error")
	}
}

func TestBindsPathFilterIsAtOrUnderBySegment(t *testing.T) {
	dir := t.TempDir()
	env := bindsEnv(t)
	for _, dst := range []string{"/n", "/n/h1", "/n/h2/deep", "/nfs", "/other"} {
		runEnv(t, `bind dir("`+dir+`"), `+dst, env)
	}
	dsts := func(src string) []string {
		var out []string
		for _, el := range runEnv(t, src, env).(*value.List).Elems {
			d, _ := el.(*value.Record).Get("dst")
			out = append(out, string(d.(value.Path)))
		}
		return out
	}
	if got := strings.Join(dsts(`binds(/n)`), " "); got != "/n /n/h1 /n/h2/deep" {
		t.Errorf("binds(/n) = %q (must include /n and its children, exclude /nfs)", got)
	}
	if got := strings.Join(dsts(`binds(/n/h1)`), " "); got != "/n/h1" {
		t.Errorf("binds(/n/h1) = %q", got)
	}
	if got := dsts(`binds(/nothing)`); len(got) != 0 {
		t.Errorf("binds(/nothing) = %v, want empty", got)
	}
	if all, filtered := len(dsts(`binds()`)), len(dsts(`binds(/)`)); all != filtered {
		t.Errorf("binds(/) returned %d layers, binds() %d — root should match everything", filtered, all)
	}
}

func TestBindsWithoutNSMountIsErrorValue(t *testing.T) {
	env := NewGlobalEnv(ns.New())
	v := runEnv(t, `binds()`, env)
	if _, ok := v.(value.ErrorVal); !ok {
		t.Fatalf("want ErrorVal when /ns isn't bound, got %#v", v)
	}
}
