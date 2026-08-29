package pane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	p9 "github.com/sandgorgon/9p"
	"github.com/sandgorgon/9p/server"
	"github.com/sandgorgon/tui/input"
	"github.com/sandgorgon/tui/style"
	"github.com/sandgorgon/tui/tui"
	"github.com/sandgorgon/tui/widget"

	"github.com/sandgorgon/9sh/job"
	"github.com/sandgorgon/9sh/ns"
)

// jobViewerNode renders a job-viewer pane: a flat, read-only list of
// every job visible in the namespace — /jobs (local) plus /jobs under
// every host currently bound at /n/<host> — the design doc's "a job/
// process viewer spanning /jobs + /n/<host>/jobs in one view". Refreshed
// on demand (Enter or 'r'), not polled: matching browser.go's own "real
// I/O belongs in a Cmd, not Update" discipline, and deliberately not
// introducing this codebase's first self-perpetuating timer/tick loop
// for what's meant to be a quick glance, not a live dashboard.
func jobViewerNode(p *paneState) tui.Node {
	id := p.id
	items := p.jobRows
	if p.jobErr != "" {
		items = []string{"error: " + p.jobErr}
	}
	if len(items) == 0 {
		items = []string{"(no jobs — press enter or r to refresh)"}
	}
	return widget.List(items, p.jobCursor, widget.ListOptions{Theme: style.DefaultDark()},
		func(e input.Event) tui.Msg {
			switch ev := e.(type) {
			case input.KeyEvent:
				switch {
				case ev.Key == input.KeyUp:
					return jobViewerMoveMsg{id: id, delta: -1}
				case ev.Key == input.KeyDown:
					return jobViewerMoveMsg{id: id, delta: 1}
				case ev.Key == input.KeyEnter, ev.Rune == 'r':
					return jobViewerRefreshRequestedMsg{id: id}
				}
			}
			return nil
		}).Key(paneKey(id, "jobviewer"))
}

type jobViewerMoveMsg struct {
	id    int
	delta int
}
type jobViewerRefreshRequestedMsg struct{ id int }
type jobsListedMsg struct {
	id   int
	rows []string
	err  error
}

// listJobsCmd is the job viewer's real I/O — a full namespace walk, so
// it belongs in a Cmd like listDirCmd, never directly in Update.
func listJobsCmd(id int, namespace *ns.Namespace) tui.Cmd {
	return func() tui.Msg {
		rows, err := listAllJobs(namespace)
		return jobsListedMsg{id: id, rows: rows, err: err}
	}
}

// listAllJobs walks /jobs, then /n/<host>/jobs for every host currently
// bound under /n (if any — /n not existing at all, the common case
// before any `dial`/`bind`, is not an error, just nothing extra to
// show), building one formatted row per job.
func listAllJobs(namespace *ns.Namespace) ([]string, error) {
	ctx := context.Background()
	root, err := namespace.Attach(ctx, "9sh", "")
	if err != nil {
		return nil, err
	}

	var rows []string
	if localRows, err := jobRowsUnder(ctx, root, []string{"jobs"}, "local"); err == nil {
		rows = append(rows, localRows...)
	}

	if nDir, err := walkAll(ctx, root, []string{"n"}); err == nil {
		if hosts, err := ns.ReadDirEntries(ctx, nDir); err == nil {
			sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
			for _, h := range hosts {
				if !h.Qid.IsDir() {
					continue
				}
				hostRows, err := jobRowsUnder(ctx, root, []string{"n", h.Name, "jobs"}, h.Name)
				if err != nil {
					rows = append(rows, fmt.Sprintf("%-10s error: %v", h.Name, err))
					continue
				}
				rows = append(rows, hostRows...)
			}
		}
	}
	return rows, nil
}

func jobRowsUnder(ctx context.Context, root server.File, parts []string, host string) ([]string, error) {
	dir, err := walkAll(ctx, root, parts)
	if err != nil {
		return nil, err
	}
	entries, err := ns.ReadDirEntries(ctx, dir)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.Qid.IsDir() {
			continue // skip "clone"
		}
		if n, err := strconv.Atoi(e.Name); err == nil {
			ids = append(ids, n)
		}
	}
	sort.Ints(ids)

	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		statusParts := append(append([]string{}, parts...), strconv.Itoa(id), "status")
		st, err := readJobStatus(ctx, root, statusParts)
		if err != nil {
			rows = append(rows, fmt.Sprintf("%-10s %d  error: %v", host, id, err))
			continue
		}
		rows = append(rows, formatJobRow(host, st))
	}
	return rows, nil
}

func readJobStatus(ctx context.Context, root server.File, parts []string) (job.Status, error) {
	f, err := walkAll(ctx, root, parts)
	if err != nil {
		return job.Status{}, err
	}
	if err := f.Open(ctx, p9.OREAD); err != nil {
		return job.Status{}, err
	}
	defer f.Close()
	b, err := readAllFile(ctx, f)
	if err != nil {
		return job.Status{}, err
	}
	var st job.Status
	if err := json.Unmarshal(b, &st); err != nil {
		return job.Status{}, err
	}
	return st, nil
}

func formatJobRow(host string, st job.Status) string {
	argv := strings.Join(st.Argv, " ")
	if argv == "" {
		argv = "(inproc)"
	}
	return fmt.Sprintf("%-10s %3d  %-10s %-8s %s", host, st.ID, st.Kind, st.State, argv)
}

// walkAll and readAllFile are this package's own copies of the same
// small helpers kyu/eval keeps for itself (see that package's doc
// comment on why: it stays namespace-protocol-only, with no
// dependency of its own on package job or package pane) — trivial
// enough that duplicating them beats introducing a shared-utility
// package for two functions.
func walkAll(ctx context.Context, root server.File, parts []string) (server.File, error) {
	f := root
	for _, part := range parts {
		var err error
		f, err = f.Walk(ctx, part)
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

func openFile(ctx context.Context, root server.File, mode p9.Mode, parts ...string) (server.File, error) {
	f, err := walkAll(ctx, root, parts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(parts, "/"), err)
	}
	if err := f.Open(ctx, mode); err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(parts, "/"), err)
	}
	return f, nil
}

func readAllFile(ctx context.Context, f server.File) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	var offset int64
	for {
		n, err := f.Read(ctx, offset, tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	return buf.Bytes(), nil
}
