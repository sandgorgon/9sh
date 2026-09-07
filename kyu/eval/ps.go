package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	p9 "github.com/sandgorgon/9p"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/ns"
)

// biPs implements `ps()`: every job visible at /jobs, as a Table of
// typed Records — the ls/stat of /jobs, in the same sense ls/stat are
// the structured, no-checkout-needed answer for an ordinary namespace
// directory. Everything here was already reachable by hand
// (ls("/jobs") | each { |j| cat(j.path + "/status") | from_json }) —
// promoted to a builtin for the same reason ls/stat themselves were:
// a daily-driver pattern common enough to deserve a name.
//
// Decodes each job's status file via job.Status's JSON shape directly —
// the same type pane/jobviewer.go already decodes into for the TUI's
// own jobs pane — rather than reinventing a parallel struct; job.Status
// is /jobs' one real producer today. ps() still only ever reaches job
// state through ordinary 9P reads over /jobs, never a direct Manager
// reference, so it works the same for a remote @host{}'s /jobs as the
// local one.
//
// A job that finishes and is removed between the directory listing and
// its own status read is silently skipped, not an error — the same
// race any "list then read" sequence over a live namespace has, and
// not worth failing the whole call over.
func biPs(env *Env, args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("ps: expected no arguments, got %d", len(args))
	}
	namespace := env.Namespace()
	if namespace == nil {
		return nil, fmt.Errorf("ps: no namespace attached to this environment")
	}
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}
	jobsDir, err := walkAll(ctx, root, splitPath("/jobs"))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("ps: %v", err)}, nil
	}
	entries, err := ns.ReadDirEntries(ctx, jobsDir)
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	var statuses []job.Status
	for _, ent := range entries {
		if ent.Name == "clone" {
			continue // write-only allocation trigger, not a job
		}
		f, err := openFile(ctx, jobsDir, p9.OREAD, ent.Name, "status")
		if err != nil {
			continue
		}
		b, err := readAllFile(ctx, f)
		f.Close()
		if err != nil {
			continue
		}
		var st job.Status
		if err := json.Unmarshal(b, &st); err != nil {
			continue
		}
		statuses = append(statuses, st)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })

	out := make([]value.Value, len(statuses))
	for i, st := range statuses {
		out[i] = jobStatusRecord(st)
	}
	return value.NewList(out), nil
}

// jobStatusRecord builds the Record shape ps() returns from one
// job.Status. exit_code/finished_at are Null until the job actually
// reaches a terminal state — Status leaves them as Go zero values
// (nil/zero time) until then, not meaningful numbers to report as 0.
func jobStatusRecord(st job.Status) *value.Record {
	r := value.NewRecord()
	r.Set("id", value.Int(st.ID))
	r.Set("kind", value.String(st.Kind))
	r.Set("state", value.String(st.State))
	argv := make([]value.Value, len(st.Argv))
	for i, a := range st.Argv {
		argv[i] = value.String(a)
	}
	r.Set("argv", value.NewList(argv))
	r.Set("pid", value.Int(st.Pid))
	if st.ExitCode != nil {
		r.Set("exit_code", value.Int(*st.ExitCode))
	} else {
		r.Set("exit_code", value.Null{})
	}
	r.Set("signal", value.String(st.Signal))
	r.Set("error", value.String(st.Err))
	r.Set("detached", value.Bool(st.Detached))
	r.Set("cwd", value.String(st.Cwd))
	r.Set("started_at", value.Int(st.StartedAt.Unix()))
	if st.FinishedAt.IsZero() {
		r.Set("finished_at", value.Null{})
	} else {
		r.Set("finished_at", value.Int(st.FinishedAt.Unix()))
	}
	return r
}
