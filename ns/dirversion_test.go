package ns

import (
	"context"
	"net"
	"testing"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/client"
	"github.com/sandgorgon/9p/server"
)

// verFS is memFS whose root directory read honors the connection's
// negotiated 9P version, like a real backend (dirfs) does — memFS's own
// root always encodes plain 9P2000, which would hide a namespace that
// decodes or re-encodes its layers' listings with the wrong version.
type verFS struct{ memFS }

func (v *verFS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	return &verRoot{memRoot: memRoot{m: &v.memFS}}, nil
}

type verRoot struct{ memRoot }

func (r *verRoot) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	entries := []p9.Stat{{Qid: p9.Qid{Type: p9.QTFILE, Path: 2}, Mode: 0644, Name: r.m.name}}
	return server.MarshalDirVersion(entries, offset, p, server.UnixFromContext(ctx))
}

// TestBindPointListingHonorsNegotiatedVersion reads a bind point's
// directory over a real 9P connection, once as plain 9P2000 and once as
// 9P2000.u. The .u case regressed as "trailing bytes after message": the
// namespace decoded each layer's listing as plain 9P2000 regardless of
// what the client negotiated.
func TestBindPointListingHonorsNegotiatedVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []client.Option
	}{
		{"plain 9P2000", nil},
		{"9P2000.u", []client.Option{client.WithUnixExtensions()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := New()
			ns.BindFS(&verFS{memFS{name: "leaf1", content: "a"}}, "", "/mix", Replace)
			ns.BindFS(&memFS{name: "inner", content: "b"}, "", "/mix/sub", Replace)

			cside, sside := net.Pipe()
			srv := &server.Server{FS: ns}
			go srv.ServeConn(sside)
			c, err := client.NewClient(cside, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Attach("u", ""); err != nil {
				t.Fatal(err)
			}

			f, err := c.OpenContext(context.Background(), "mix", p9.OREAD)
			if err != nil {
				t.Fatalf("open mix: %v", err)
			}
			defer f.Close()
			stats, err := f.ReadDirContext(context.Background())
			if err != nil {
				t.Fatalf("read bind-point directory: %v", err)
			}
			names := map[string]bool{}
			for _, s := range stats {
				names[s.Name] = true
			}
			if !names["leaf1"] || !names["sub"] {
				t.Errorf("listing = %v, want leaf1 (layer) and sub (tree child)", names)
			}
		})
	}
}
