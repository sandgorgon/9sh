package eval

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/remote"
)

// builtins are kyu's pipe-stage functions. By convention (see
// evalPipeExpr) the piped-in value is appended as the final argument, so
// each signature below reads as "explicit args..., then the input".
var builtins = map[string]BuiltinFn{
	"where":    biWhere,
	"select":   biSelect,
	"sort_by":  biSortBy,
	"group_by": biGroupBy,
	"each":     biEach,
	"take":     biTake,
	"first":    biFirst,
	"count":    biCount,
	"error":    biError,
	"wait":     biWait,
	"dial":     biDial,
	"host":     biHost,
}

// biHost returns this machine's hostname — the design doc's own example
// of why per-host dotfiles overrides (hosts/<hostname>.ky) don't need a
// templating DSL: a conditional like `if host() == "laptop" { ... }` is
// just ordinary kyu, usable both there and inside common.ky for
// lightweight branches that don't warrant a whole separate host file.
func biHost(args []value.Value) (value.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("host: expected no arguments, got %d", len(args))
	}
	h, err := os.Hostname()
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("host: %v", err)}, nil
	}
	return value.String(h), nil
}

// biDial connects to a remote 9sh/9P peer over mutual TLS (see package
// remote's doc comment for the trust model) and returns a MountHandle —
// bind's job, not dial's, is to actually graft it into the namespace
// (`bind dial("host:port"), "/n/host"`), matching how namespace verbs stay
// keywords while everything else, dial included, is an ordinary function.
// A connection failure is an in-stream ErrorVal, not a hard Go error,
// matching runExternal's "a bad exec becomes a value flowing through the
// pipeline" convention — dialing a peer that's down or that refuses this
// identity is exactly that kind of ordinary, expected failure.
func biDial(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("dial: expected 1 argument (an address), got %d", len(args))
	}
	addr, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("dial: expected a string address, got %s", args[0].Kind())
	}
	conn, err := remote.Dial(context.Background(), string(addr))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("dial: %v", err)}, nil
	}
	return value.MountHandle{Addr: string(addr), FS: conn.FS()}, nil
}

func lastAsList(args []value.Value, fnName string) (*value.List, []value.Value, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("%s: missing input", fnName)
	}
	input := args[len(args)-1]
	rest := args[:len(args)-1]
	lst, ok := input.(*value.List)
	if !ok {
		return nil, nil, fmt.Errorf("%s: expected a list/table input, got %s", fnName, input.Kind())
	}
	return lst, rest, nil
}

func biWhere(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "where")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("where: expected 1 predicate argument, got %d", len(rest))
	}
	pred := rest[0]
	var out []value.Value
	for _, row := range lst.Elems {
		res, err := call(pred, []value.Value{row})
		if err != nil {
			return nil, err
		}
		if value.Truthy(res) {
			out = append(out, row)
		}
	}
	return value.NewList(out), nil
}

func biSelect(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "select")
	if err != nil {
		return nil, err
	}
	if len(rest) == 0 {
		return nil, fmt.Errorf("select: expected at least 1 field name argument")
	}
	fields := make([]string, len(rest))
	for i, a := range rest {
		s, ok := a.(value.String)
		if !ok {
			return nil, fmt.Errorf("select: field name argument %d must be a string, got %s", i, a.Kind())
		}
		fields[i] = string(s)
	}
	out := make([]value.Value, len(lst.Elems))
	for i, row := range lst.Elems {
		rec, ok := row.(*value.Record)
		if !ok {
			return nil, fmt.Errorf("select: row %d is a %s, not a record", i, row.Kind())
		}
		projected := value.NewRecord()
		for _, f := range fields {
			v, ok := rec.Get(f)
			if !ok {
				return nil, fmt.Errorf("select: row %d has no field %q", i, f)
			}
			projected.Set(f, v)
		}
		out[i] = projected
	}
	return value.NewList(out), nil
}

// keyFn resolves a sort_by/group_by key argument (a field-name String or a
// single-arg Closure/Builtin) into a per-row key extractor.
func keyFn(arg value.Value) (func(value.Value) (value.Value, error), error) {
	switch k := arg.(type) {
	case value.String:
		field := string(k)
		return func(row value.Value) (value.Value, error) {
			rec, ok := row.(*value.Record)
			if !ok {
				return nil, fmt.Errorf("cannot key by field %q on a %s", field, row.Kind())
			}
			v, ok := rec.Get(field)
			if !ok {
				return nil, fmt.Errorf("row has no field %q", field)
			}
			return v, nil
		}, nil
	case *ClosureVal, *Builtin:
		return func(row value.Value) (value.Value, error) {
			return call(k, []value.Value{row})
		}, nil
	default:
		return nil, fmt.Errorf("expected a field name (string) or a closure, got %s", arg.Kind())
	}
}

func biSortBy(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "sort_by")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("sort_by: expected 1 key argument, got %d", len(rest))
	}
	key, err := keyFn(rest[0])
	if err != nil {
		return nil, fmt.Errorf("sort_by: %w", err)
	}
	elems := append([]value.Value(nil), lst.Elems...)
	if err := sortValues(elems, key); err != nil {
		return nil, fmt.Errorf("sort_by: %w", err)
	}
	return value.NewList(elems), nil
}

// sortValues stable-sorts elems ascending by key, using value.Compare.
func sortValues(elems []value.Value, key func(value.Value) (value.Value, error)) error {
	type keyed struct {
		v   value.Value
		k   value.Value
		err error
	}
	items := make([]keyed, len(elems))
	for i, e := range elems {
		k, err := key(e)
		items[i] = keyed{v: e, k: k, err: err}
	}
	var firstErr error
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].err != nil || items[j].err != nil {
			if firstErr == nil {
				if items[i].err != nil {
					firstErr = items[i].err
				} else {
					firstErr = items[j].err
				}
			}
			return false
		}
		c, err := value.Compare(items[i].k, items[j].k)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return false
		}
		return c < 0
	})
	for i, it := range items {
		elems[i] = it.v
	}
	return firstErr
}

func biGroupBy(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "group_by")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("group_by: expected 1 key argument, got %d", len(rest))
	}
	key, err := keyFn(rest[0])
	if err != nil {
		return nil, fmt.Errorf("group_by: %w", err)
	}
	type group struct {
		key   value.Value
		items []value.Value
	}
	var groups []*group
	for _, row := range lst.Elems {
		k, err := key(row)
		if err != nil {
			return nil, fmt.Errorf("group_by: %w", err)
		}
		var g *group
		for _, existing := range groups {
			if value.Equal(existing.key, k) {
				g = existing
				break
			}
		}
		if g == nil {
			g = &group{key: k}
			groups = append(groups, g)
		}
		g.items = append(g.items, row)
	}
	out := make([]value.Value, len(groups))
	for i, g := range groups {
		rec := value.NewRecord()
		rec.Set("key", g.key)
		rec.Set("items", value.NewList(g.items))
		out[i] = rec
	}
	return value.NewList(out), nil
}

func biEach(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "each")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("each: expected 1 closure argument, got %d", len(rest))
	}
	fn := rest[0]
	out := make([]value.Value, len(lst.Elems))
	for i, row := range lst.Elems {
		v, err := call(fn, []value.Value{row})
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return value.NewList(out), nil
}

func biTake(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "take")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("take: expected 1 count argument, got %d", len(rest))
	}
	n, ok := rest[0].(value.Int)
	if !ok {
		return nil, fmt.Errorf("take: count argument must be an int, got %s", rest[0].Kind())
	}
	if n < 0 {
		return nil, fmt.Errorf("take: count must be >= 0, got %d", n)
	}
	end := min(int(n), len(lst.Elems))
	out := append([]value.Value(nil), lst.Elems[:end]...)
	return value.NewList(out), nil
}

func biFirst(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "first")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("first: expected no arguments besides input, got %d", len(rest))
	}
	if len(lst.Elems) == 0 {
		return value.Null{}, nil
	}
	return lst.Elems[0], nil
}

func biCount(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "count")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("count: expected no arguments besides input, got %d", len(rest))
	}
	return value.Int(len(lst.Elems)), nil
}

func biError(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("error: expected exactly 1 message argument, got %d", len(args))
	}
	s, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("error: message argument must be a string, got %s", args[0].Kind())
	}
	return value.ErrorVal{Msg: string(s)}, nil
}

// biWait implements `j | wait`. It's deliberately just a record-field
// read: a job record's "wait" field is live-backed by the namespace's
// wait file, whose ReadField blocks until the job is terminal (see
// kyu/eval/namespace.go's buildJobRecord) — `j | wait` and `j.wait` are
// two spellings of the same operation, not two mechanisms.
func biWait(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("wait: expected exactly 1 argument (the job), got %d", len(args))
	}
	rec, ok := args[0].(*value.Record)
	if !ok {
		return nil, fmt.Errorf("wait: expected a job record, got %s", args[0].Kind())
	}
	v, ok := rec.Get("wait")
	if !ok {
		return nil, fmt.Errorf("wait: record has no \"wait\" field")
	}
	return v, nil
}
