package ns

import (
	"context"
	"errors"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

var errReadOnly = errors.New("ns: read-only bind")

// roFile wraps a server.File so nothing reachable through it can be
// changed: every child Walk hands back is wrapped too, opening for
// write/truncate is refused, and create/remove/wstat/write fail. It sits
// on the *layer*, not the file server, so the same tree bound elsewhere
// without ro stays writable — the point of `bind /local, /ro, ro` is a
// safe view of something you also work in.
//
// Stat masks the write permission bits so `ls`/`stat` don't advertise
// what the bind won't allow.
type roFile struct{ server.File }

func (f roFile) Walk(ctx context.Context, name string) (server.File, error) {
	child, err := f.File.Walk(ctx, name)
	if err != nil {
		return nil, err
	}
	if _, already := child.(roFile); already {
		return child, nil
	}
	return roFile{child}, nil
}

func (f roFile) Stat(ctx context.Context) (p9.Stat, error) {
	st, err := f.File.Stat(ctx)
	st.Mode &^= 0222
	return st, err
}

func (f roFile) WStat(ctx context.Context, st p9.Stat) error { return errReadOnly }

func (f roFile) Open(ctx context.Context, mode p9.Mode) error {
	if acc := mode & 3; acc == p9.OWRITE || acc == p9.ORDWR || mode&(p9.OTRUNC|p9.ORCLOSE) != 0 {
		return errReadOnly
	}
	return f.File.Open(ctx, mode)
}

func (f roFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, errReadOnly
}

func (f roFile) Remove(ctx context.Context) error { return errReadOnly }

func (f roFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errReadOnly
}
