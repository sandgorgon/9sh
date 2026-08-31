package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
)

// Qid.Path is derived deterministically from file identity, not an
// incrementing counter, so repeated Walks to the same file (e.g. two
// clients both walking to job 3's "status") see the same Path — matching
// 9P's intent that Qid identifies the file, not the walk/fid instance.
const (
	qidRoot    = 0
	qidClone   = 1
	qidJobBase = 1000 // job id -> qidJobBase + id*20, plus a per-name offset below
)

var fileOffset = map[string]uint64{
	"ctl": 1, "status": 2, "events": 3, "wait": 4,
	"argv": 5, "env": 6, "stdin": 7, "stdout": 8, "stderr": 9, "cwd": 10,
}

var childNames = []string{"ctl", "status", "events", "wait", "argv", "env", "stdin", "stdout", "stderr", "cwd"}

// The *20 stride (not *10) leaves headroom past today's 10 offsets for a
// future field without another cross-cutting stride bump.
func jobDirPath(id int) uint64 { return qidJobBase + uint64(id)*20 }

const sysUser = "9sh"

// FS exposes a Manager's jobs as a server.FileSystem, per the design
// doc's job-control file protocol:
//
//	/clone                              write-then-open allocates a job
//	/<id>/{ctl,status,events,wait,argv,env,stdin,stdout,stderr}
//
// This FS's root is what a later `bind` (Phase 3) would mount at /jobs;
// for now, clients Attach to it directly and see its contents at "/".
type FS struct {
	mgr *Manager
}

func New(mgr *Manager) *FS { return &FS{mgr: mgr} }

func (fs *FS) Attach(ctx context.Context, uname, aname string) (server.File, error) {
	return &rootFile{fs: fs}, nil
}

// baseFile implements the parts of server.File that are identical across
// every non-directory /jobs file: not walkable, not creatable, no
// meaningful WStat/Remove. Embedders set qid/name/mode and provide
// Read/Write themselves.
type baseFile struct {
	qid  p9.Qid
	name string
	mode p9.Mode
}

func (f *baseFile) Qid() p9.Qid { return f.qid }
func (f *baseFile) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: f.qid, Mode: f.mode, Name: f.name, Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}
func (f *baseFile) WStat(ctx context.Context, st p9.Stat) error {
	return fmt.Errorf("jobfs: %s: cannot modify metadata", f.name)
}
func (f *baseFile) Walk(ctx context.Context, name string) (server.File, error) {
	return nil, fmt.Errorf("jobfs: %s: not a directory", f.name)
}
func (f *baseFile) Open(ctx context.Context, mode p9.Mode) error { return nil }
func (f *baseFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, fmt.Errorf("jobfs: %s: not a directory", f.name)
}
func (f *baseFile) Remove(ctx context.Context) error {
	return fmt.Errorf("jobfs: %s: cannot remove", f.name)
}
func (f *baseFile) Close() error { return nil }

// ---- /jobs (root) ----

type rootFile struct {
	fs *FS
}

func (f *rootFile) Qid() p9.Qid { return p9.Qid{Type: p9.QTDIR, Path: qidRoot} }
func (f *rootFile) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: f.Qid(), Mode: p9.DMDIR | 0755, Name: "/", Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}
func (f *rootFile) WStat(ctx context.Context, st p9.Stat) error {
	return errors.New("jobfs: cannot modify /jobs root")
}

func (f *rootFile) Walk(ctx context.Context, name string) (server.File, error) {
	if name == ".." {
		return f, nil
	}
	if name == "clone" {
		return &cloneFile{fs: f.fs}, nil
	}
	id, err := strconv.Atoi(name)
	if err != nil {
		return nil, fmt.Errorf("jobfs: %s: no such file", name)
	}
	if _, ok := f.fs.mgr.Get(id); !ok {
		return nil, fmt.Errorf("jobfs: %s: no such file", name)
	}
	return &jobDirFile{fs: f.fs, id: id}, nil
}

func (f *rootFile) Open(ctx context.Context, mode p9.Mode) error { return nil }
func (f *rootFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, errors.New("jobfs: cannot create in /jobs")
}

func (f *rootFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	jobs := f.fs.mgr.List()
	entries := make([]p9.Stat, 0, len(jobs)+1)
	entries = append(entries, p9.Stat{
		Qid: p9.Qid{Type: p9.QTFILE, Path: qidClone}, Mode: 0666, Name: "clone",
		Uid: sysUser, Gid: sysUser, Muid: sysUser,
	})
	for _, j := range jobs {
		entries = append(entries, p9.Stat{
			Qid: p9.Qid{Type: p9.QTDIR, Path: jobDirPath(j.ID)}, Mode: p9.DMDIR | 0755, Name: strconv.Itoa(j.ID),
			Uid: sysUser, Gid: sysUser, Muid: sysUser,
		})
	}
	return server.MarshalDir(entries, offset, p)
}

func (f *rootFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("jobfs: cannot write to /jobs root")
}
func (f *rootFile) Remove(ctx context.Context) error {
	return errors.New("jobfs: cannot remove /jobs root")
}
func (f *rootFile) Close() error { return nil }

// ---- /jobs/clone ----

// cloneFile allocates a new subprocess-kind job on Open, Plan-9's usual
// clone-device convention — read the id back, then walk to /<id>. Each
// Walk to "clone" produces a fresh cloneFile (see server.File's Walk
// contract), so concurrent opens each get their own independently
// allocated job; nothing is shared across them.
type cloneFile struct {
	fs *FS
	id int
}

func (f *cloneFile) Qid() p9.Qid { return p9.Qid{Type: p9.QTFILE, Path: qidClone} }
func (f *cloneFile) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: f.Qid(), Mode: 0666, Name: "clone", Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}
func (f *cloneFile) WStat(ctx context.Context, st p9.Stat) error {
	return errors.New("jobfs: clone: cannot modify metadata")
}
func (f *cloneFile) Walk(ctx context.Context, name string) (server.File, error) {
	return nil, errors.New("jobfs: clone: not a directory")
}
func (f *cloneFile) Open(ctx context.Context, mode p9.Mode) error {
	f.id = f.fs.mgr.AllocSubprocess().ID
	return nil
}
func (f *cloneFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, errors.New("jobfs: clone: not a directory")
}
func (f *cloneFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	if f.id == 0 {
		return 0, errors.New("jobfs: clone: not opened")
	}
	s := strconv.Itoa(f.id) + "\n"
	if offset >= int64(len(s)) {
		return 0, io.EOF
	}
	return copy(p, s[offset:]), nil
}
func (f *cloneFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("jobfs: clone: read-only")
}
func (f *cloneFile) Remove(ctx context.Context) error {
	return errors.New("jobfs: clone: cannot remove")
}
func (f *cloneFile) Close() error { return nil }

// ---- /jobs/<id> ----

type jobDirFile struct {
	fs *FS
	id int
}

func (f *jobDirFile) Qid() p9.Qid { return p9.Qid{Type: p9.QTDIR, Path: jobDirPath(f.id)} }
func (f *jobDirFile) Stat(ctx context.Context) (p9.Stat, error) {
	return p9.Stat{Qid: f.Qid(), Mode: p9.DMDIR | 0755, Name: strconv.Itoa(f.id), Uid: sysUser, Gid: sysUser, Muid: sysUser}, nil
}
func (f *jobDirFile) WStat(ctx context.Context, st p9.Stat) error {
	return fmt.Errorf("jobfs: %d: cannot modify metadata", f.id)
}

func (f *jobDirFile) Walk(ctx context.Context, name string) (server.File, error) {
	if name == ".." {
		return &rootFile{fs: f.fs}, nil
	}
	off, ok := fileOffset[name]
	if !ok {
		return nil, fmt.Errorf("jobfs: %s: no such file", name)
	}
	j, ok := f.fs.mgr.Get(f.id)
	if !ok {
		return nil, fmt.Errorf("jobfs: job %d no longer exists", f.id)
	}
	base := baseFile{qid: p9.Qid{Type: p9.QTFILE, Path: jobDirPath(f.id) + off}, name: name}
	switch name {
	case "ctl":
		base.mode = 0200
		return &ctlFile{baseFile: base, job: j}, nil
	case "status":
		base.mode = 0444
		return &statusFile{baseFile: base, job: j}, nil
	case "events":
		base.mode = 0444
		return &streamFile{baseFile: base, buf: j.events}, nil
	case "wait":
		base.mode = 0444
		return &waitFile{baseFile: base, job: j}, nil
	case "argv":
		base.mode = 0644
		return &argvFile{baseFile: base, job: j}, nil
	case "env":
		base.mode = 0644
		return &envFile{baseFile: base, job: j}, nil
	case "cwd":
		base.mode = 0644
		return &cwdFile{baseFile: base, job: j}, nil
	case "stdin":
		base.mode = 0200
		return &stdinFile{baseFile: base, job: j}, nil
	case "stdout":
		base.mode = 0444
		return &streamFile{baseFile: base, buf: j.stdout}, nil
	case "stderr":
		base.mode = 0444
		return &streamFile{baseFile: base, buf: j.stderr}, nil
	}
	return nil, fmt.Errorf("jobfs: %s: no such file", name)
}

func (f *jobDirFile) Open(ctx context.Context, mode p9.Mode) error { return nil }
func (f *jobDirFile) Create(ctx context.Context, name string, perm, mode p9.Mode) (server.File, error) {
	return nil, fmt.Errorf("jobfs: %d: cannot create", f.id)
}

func (f *jobDirFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	entries := make([]p9.Stat, len(childNames))
	for i, name := range childNames {
		mode := p9.Mode(0444)
		switch name {
		case "ctl", "stdin":
			mode = 0200
		case "argv", "env", "cwd":
			mode = 0644
		}
		entries[i] = p9.Stat{
			Qid:  p9.Qid{Type: p9.QTFILE, Path: jobDirPath(f.id) + fileOffset[name]},
			Mode: mode, Name: name, Uid: sysUser, Gid: sysUser, Muid: sysUser,
		}
	}
	return server.MarshalDir(entries, offset, p)
}

func (f *jobDirFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, fmt.Errorf("jobfs: %d: cannot write to a directory", f.id)
}
func (f *jobDirFile) Remove(ctx context.Context) error {
	return fmt.Errorf("jobfs: %d: cannot remove (jobs are cleaned up by kind/session policy, not rm, in this phase)", f.id)
}
func (f *jobDirFile) Close() error { return nil }

// ---- leaf files ----

type ctlFile struct {
	baseFile
	job *Job
}

func (f *ctlFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("jobfs: ctl: write-only")
}
func (f *ctlFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	cmd := strings.TrimSpace(string(p))
	if err := f.job.Ctl(cmd); err != nil {
		return 0, err
	}
	return len(p), nil
}

type statusFile struct {
	baseFile
	job *Job
}

// Read re-marshals the job's current status on every call — "a single
// live NRF record, re-read fresh each access" per the design doc.
func (f *statusFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	b, _ := json.Marshal(f.job.Status())
	b = append(b, '\n')
	if offset >= int64(len(b)) {
		return 0, io.EOF
	}
	return copy(p, b[offset:]), nil
}
func (f *statusFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("jobfs: status: read-only")
}

type waitFile struct {
	baseFile
	job *Job
}

// Read blocks until the job is terminal (immediately, if it already is),
// then returns its final status. Cancelable via ctx (Tflush).
func (f *waitFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	st, err := f.job.WaitFor(ctx)
	if err != nil {
		return 0, err
	}
	b, _ := json.Marshal(st)
	b = append(b, '\n')
	if offset >= int64(len(b)) {
		return 0, io.EOF
	}
	return copy(p, b[offset:]), nil
}
func (f *waitFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("jobfs: wait: read-only")
}

type argvFile struct {
	baseFile
	job *Job
}

func (f *argvFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	b := f.job.ArgvBytes()
	if offset >= int64(len(b)) {
		return 0, io.EOF
	}
	return copy(p, b[offset:]), nil
}

// Write replaces the whole argv (offset is ignored — this is a small
// configuration file written once before `ctl start`, not a growing
// stream), one argument per line.
func (f *argvFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if err := f.job.SetArgv(splitNonEmptyLines(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

type envFile struct {
	baseFile
	job *Job
}

func (f *envFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	b := f.job.EnvBytes()
	if offset >= int64(len(b)) {
		return 0, io.EOF
	}
	return copy(p, b[offset:]), nil
}
func (f *envFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if err := f.job.SetEnv(splitNonEmptyLines(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// cwdFile is a single-value config file, like argvFile/envFile but never
// line-split — a job's working directory is one path, not a list.
type cwdFile struct {
	baseFile
	job *Job
}

func (f *cwdFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	b := f.job.CwdBytes()
	if offset >= int64(len(b)) {
		return 0, io.EOF
	}
	return copy(p, b[offset:]), nil
}
func (f *cwdFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	if err := f.job.SetCwd(strings.TrimSpace(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func splitNonEmptyLines(p []byte) []string {
	s := strings.TrimRight(string(p), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

type stdinFile struct {
	baseFile
	job *Job
}

func (f *stdinFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, errors.New("jobfs: stdin: write-only")
}
func (f *stdinFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return f.job.writeStdin(p)
}
func (f *stdinFile) Close() error { return f.job.closeStdin() }

// streamFile is stdout/stderr/events: a growBuf exposed as-is.
type streamFile struct {
	baseFile
	buf *growBuf
}

func (f *streamFile) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	return f.buf.Read(ctx, offset, p)
}
func (f *streamFile) Write(ctx context.Context, offset int64, p []byte) (int, error) {
	return 0, fmt.Errorf("jobfs: %s: read-only", f.name)
}
