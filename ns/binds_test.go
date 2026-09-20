package ns

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestBindsRecordsSpecAndCanonicalDisposition(t *testing.T) {
	n := New()
	ctx := context.Background()
	if err := n.BindFS(&memFS{name: "a", content: "A"}, "", "/boot", Replace); err != nil {
		t.Fatal(err)
	}
	if err := n.BindFSSpec(&memFS{name: "b", content: "B"}, "", "/work", Replace, `dir("/x")`); err != nil {
		t.Fatal(err)
	}
	if err := n.BindPath(ctx, []string{"/work"}, "/alias", Replace); err != nil {
		t.Fatal(err)
	}
	// Bound *before* /alias's first layer: union order is [/boot, /work],
	// but the canonical listing must still replay to that same order.
	if err := n.BindPath(ctx, []string{"/boot", "/work"}, "/u", Replace); err != nil {
		t.Fatal(err)
	}
	if err := n.BindPath(ctx, []string{"/boot"}, "/u", Before); err != nil {
		t.Fatal(err)
	}

	want := []Bind{
		{Dst: "/boot", Src: "", Disp: "replace"},
		{Dst: "/work", Src: `dir("/x")`, Disp: "replace"},
		{Dst: "/alias", Src: "/work", Disp: "replace"},
		{Dst: "/u", Src: "/boot", Disp: "replace"},
		{Dst: "/u", Src: "/boot", Disp: "after"},
		{Dst: "/u", Src: "/work", Disp: "after"},
	}
	if got := n.Binds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Binds() =\n%#v\nwant\n%#v", got, want)
	}

	wantText := "# bind <builtin>, /boot\n" +
		"bind dir(\"/x\"), /work\n" +
		"bind /work, /alias\n" +
		"bind /boot, /u\n" +
		"bind /boot, /u, after\n" +
		"bind /work, /u, after\n"
	if got := FormatBinds(n.Binds()); got != wantText {
		t.Fatalf("FormatBinds =\n%s\nwant\n%s", got, wantText)
	}
}

func TestBindsAfterUnbindAndReplace(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFSSpec(&memFS{name: "a"}, "", "/a", Replace, `dir("/a")`)
	n.BindFSSpec(&memFS{name: "b"}, "", "/b", Replace, `dir("/b")`)
	if err := n.Unbind("/a"); err != nil {
		t.Fatal(err)
	}
	// Replace drops the prior layer from the report too.
	n.BindFSSpec(&memFS{name: "c"}, "", "/b", Replace, `dir("/c")`)
	_ = ctx
	want := []Bind{{Dst: "/b", Src: `dir("/c")`, Disp: "replace"}}
	if got := n.Binds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Binds() = %#v, want %#v", got, want)
	}
}

func TestBindsEmptyNamespace(t *testing.T) {
	if got := New().Binds(); len(got) != 0 {
		t.Fatalf("Binds() = %#v, want empty", got)
	}
}

func TestBindsFSFiles(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFSSpec(&memFS{name: "a"}, "", "/work", Replace, `dir("/x")`)
	if err := n.BindFS(NewBindsFS(n), "", "/ns", Replace); err != nil {
		t.Fatal(err)
	}
	root, _ := n.Attach(ctx, "u", "")

	txt := mustWalk(t, ctx, root, "ns", "binds")
	if err := txt.Open(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got, want := readAll(t, ctx, txt), "bind dir(\"/x\"), /work\n# bind <builtin>, /ns\n"; got != want {
		t.Fatalf("binds = %q, want %q", got, want)
	}

	js := mustWalk(t, ctx, root, "ns", "binds.json")
	if err := js.Open(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var got []Bind
	if err := json.Unmarshal([]byte(readAll(t, ctx, js)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Dst != "/work" || got[1].Dst != "/ns" {
		t.Fatalf("binds.json = %#v", got)
	}
}

func TestBindsFSSnapshotAtOpenAndReadOnly(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(NewBindsFS(n), "", "/ns", Replace)
	root, _ := n.Attach(ctx, "u", "")

	f := mustWalk(t, ctx, root, "ns", "binds")
	if err := f.Open(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// A bind after Open must not change what this reader sees.
	n.BindFSSpec(&memFS{name: "a"}, "", "/late", Replace, `dir("/l")`)
	if got, want := readAll(t, ctx, f), "# bind <builtin>, /ns\n"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}

	w := mustWalk(t, ctx, root, "ns", "binds")
	if err := w.Open(ctx, 1); err == nil { // OWRITE
		t.Fatal("opening binds for write should fail")
	}
	if _, err := w.Write(ctx, 0, []byte("x")); err == nil {
		t.Fatal("writing binds should fail")
	}
	if _, err := mustWalk(t, ctx, root, "ns").Walk(ctx, "nope"); err == nil {
		t.Fatal("walking a missing name in /ns should fail")
	}
}

func TestResolveUnionLayersTreeAndBindPoint(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFSSpec(&memFS{name: "a", content: "A"}, "", "/u", Replace, `dir("/one")`)
	n.BindFSSpec(&memFS{name: "b", content: "B"}, "", "/u", After, `dir("/two")`)
	n.BindFS(&memFS{name: "c"}, "", "/n/h", Replace)

	cases := []struct {
		path string
		want Resolution
	}{
		{"/u/a", Resolution{Path: "/u/a", Kind: "layer", Dst: "/u", Src: `dir("/one")`, Layer: 0, Layers: 2, Inner: "/a"}},
		{"/u/b", Resolution{Path: "/u/b", Kind: "layer", Dst: "/u", Src: `dir("/two")`, Layer: 1, Layers: 2, Inner: "/b"}},
		{"/u", Resolution{Path: "/u", Kind: "bindpoint", Dst: "/u", Layer: -1, Layers: 2}},
		{"/n", Resolution{Path: "/n", Kind: "tree", Dst: "/n", Layer: -1}},
		{"/n/h/c", Resolution{Path: "/n/h/c", Kind: "layer", Dst: "/n/h", Layer: 0, Layers: 1, Inner: "/c"}},
		{"/", Resolution{Path: "/", Kind: "tree", Dst: "/", Layer: -1}},
	}
	for _, c := range cases {
		got, err := n.Resolve(ctx, c.path)
		if err != nil {
			t.Errorf("Resolve(%s): %v", c.path, err)
			continue
		}
		if got != c.want {
			t.Errorf("Resolve(%s) =\n%#v\nwant\n%#v", c.path, got, c.want)
		}
	}

	for _, missing := range []string{"/u/nope", "/nothing", "/u/a/deeper"} {
		if _, err := n.Resolve(ctx, missing); err == nil {
			t.Errorf("Resolve(%s) should fail", missing)
		}
	}
}

// Resolve must agree with what a real Walk does, including that it picks
// a layer by the first path element only.
func TestResolveAgreesWithWalk(t *testing.T) {
	n := New()
	ctx := context.Background()
	n.BindFS(&memFS{name: "same", content: "first"}, "", "/u", Replace)
	n.BindFS(&memFS{name: "same", content: "second"}, "", "/u", After)
	root, _ := n.Attach(ctx, "u", "")
	f := mustWalk(t, ctx, root, "u", "same")
	f.Open(ctx, 0)
	if got := readAll(t, ctx, f); got != "first" {
		t.Fatalf("walk served %q, test premise broken", got)
	}
	res, err := n.Resolve(ctx, "/u/same")
	if err != nil || res.Layer != 0 {
		t.Fatalf("Resolve = %#v, %v; want layer 0 (the one Walk served)", res, err)
	}
}
