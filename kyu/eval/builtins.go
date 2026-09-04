package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/sandgorgon/9p/examples/dirfs"

	"github.com/sandgorgon/9sh/kyu/token"
	"github.com/sandgorgon/9sh/kyu/value"
	"github.com/sandgorgon/9sh/remote"
)

// builtins are kyu's pipe-stage functions. By convention (see
// evalPipeExpr) the piped-in value is appended as the final argument, so
// each signature below reads as "explicit args..., then the input".
var builtins = map[string]BuiltinFn{
	"where":     biWhere,
	"select":    biSelect,
	"sort_by":   biSortBy,
	"group_by":  biGroupBy,
	"each":      biEach,
	"take":      biTake,
	"first":     biFirst,
	"count":     biCount,
	"error":     biError,
	"wait":      biWait,
	"dial":      biDial,
	"dir":       biDir,
	"host":      biHost,
	"join_path": biJoinPath,
	"path":      biPath,
	"help":      biHelp,
	"last":      biLast,
	"skip":      biSkip,
	"reverse":   biReverse,
	"uniq":      biUniq,
	"flatten":   biFlatten,
	"join":      biJoin,
	"sum":       biSum,
	"min":       biMin,
	"max":       biMax,
	"avg":       biAvg,
	"any":       biAny,
	"all":       biAll,
	"to_json":   biToJSON,
	"from_json": biFromJSON,
	"split":     biSplit,
	"trim":      biTrim,
	"replace":   biReplace,
	"contains":  biContains,
	"format":    biFormat,
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

// biDir wraps an arbitrary host directory into a MountHandle, the local
// sibling to biDial: dial reaches a remote/already-listening namespace,
// dir reaches a real OS directory that isn't already walkable in the
// current namespace (the launch directory alone gets that for free via
// /local's own startup bind — see main.go's bootstrap). Same shape as
// dial in every other respect: `bind dir("/u/some/long/path"), /std/x`,
// and a bad path is an ordinary in-stream ErrorVal, not a hard Go error.
//
// Requires an absolute path, deliberately: kyu's namespace has no notion
// of "current directory" (see the README's "Coming from bash/zsh"
// section), so there's no sensible base a relative host path could
// resolve against without inventing one — reject it instead of guessing.
func biDir(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("dir: expected 1 argument (a host path), got %d", len(args))
	}
	path, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("dir: expected a string path, got %s", args[0].Kind())
	}
	if !strings.HasPrefix(string(path), "/") {
		return nil, fmt.Errorf("dir: path must be absolute, got %q", string(path))
	}
	fs, err := dirfs.New(string(path))
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("dir: %v", err)}, nil
	}
	return value.MountHandle{Addr: string(path), FS: fs}, nil
}

// biPath converts a String to a Path — the explicit escape hatch for
// dynamically-built path text (format(...), split/join, getenv(...))
// that would otherwise have no way to reach bind/checkout/stat/
// join_path's base, all of which require an actual Path and hard-
// reject a String (see dial/dir's own doc comments for why the reverse
// direction is just as strict). An explicit, visible conversion, never
// implicit coercion — matching how dir()/dial() are themselves the
// only sanctioned way to cross the Path/String boundary elsewhere in
// kyu, never a silent guess based on what a value looks like.
//
// Requires an absolute string, same as dir(): kyu has no namespace cwd
// to resolve a relative one against (see the README's "Coming from
// bash/zsh" section), and every Path literal the lexer itself can ever
// produce is absolute by construction — a Path token only starts on a
// leading "/" — so path(str) shouldn't be able to manufacture a kind
// of Path value nothing else in the language can.
func biPath(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("path: expected 1 argument (a string), got %d", len(args))
	}
	s, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("path: expected a string, got %s", args[0].Kind())
	}
	if !strings.HasPrefix(string(s), "/") {
		return nil, fmt.Errorf("path: must be absolute, got %q", string(s))
	}
	return value.Path(s), nil
}

// biJoinPath builds a Path from a base Path plus string segments — the
// answer to "typing a full namespace path every time is a chore" that
// doesn't reintroduce a namespace-relative cwd (kyu deliberately has
// none — see the README's "Coming from bash/zsh" section): the base
// still has to be a real, fully-qualified Path (a literal, or anything
// else already typed Path — a bind alias stored in a variable, say), so
// this only ever shortens repetition of an already-explicit root, never
// resolves against implicit state.
//
// Uses "path", not "path/filepath": namespace paths are always "/"
// -separated regardless of host OS, unlike a real filesystem path (9sh
// ships darwin binaries too). path.Join also lexically cleans the
// result (collapsing "." and ".."), same as it would for any other
// Path literal you might have typed by hand — that can't escape past
// the namespace root, so it's not treated as a special case here.
func biJoinPath(args []value.Value) (value.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("join_path: expected a base path and zero or more string segments, got 0 arguments")
	}
	base, ok := args[0].(value.Path)
	if !ok {
		return nil, fmt.Errorf("join_path: first argument must be a path, got %s", args[0].Kind())
	}
	segs := make([]string, len(args))
	segs[0] = string(base)
	for i, a := range args[1:] {
		s, ok := a.(value.String)
		if !ok {
			return nil, fmt.Errorf("join_path: segment %d must be a string, got %s", i+1, a.Kind())
		}
		segs[i+1] = string(s)
	}
	return value.Path(path.Join(segs...)), nil
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

func biLast(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "last")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("last: expected no arguments besides input, got %d", len(rest))
	}
	if len(lst.Elems) == 0 {
		return value.Null{}, nil
	}
	return lst.Elems[len(lst.Elems)-1], nil
}

func biSkip(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "skip")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("skip: expected 1 count argument, got %d", len(rest))
	}
	n, ok := rest[0].(value.Int)
	if !ok {
		return nil, fmt.Errorf("skip: count argument must be an int, got %s", rest[0].Kind())
	}
	if n < 0 {
		return nil, fmt.Errorf("skip: count must be >= 0, got %d", n)
	}
	start := min(int(n), len(lst.Elems))
	out := append([]value.Value(nil), lst.Elems[start:]...)
	return value.NewList(out), nil
}

func biReverse(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "reverse")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("reverse: expected no arguments besides input, got %d", len(rest))
	}
	out := make([]value.Value, len(lst.Elems))
	for i, e := range lst.Elems {
		out[len(out)-1-i] = e
	}
	return value.NewList(out), nil
}

// biUniq drops duplicates (value.Equal), keeping first-occurrence order
// — O(n²), the same style group_by already uses for its own key
// deduplication, not a new complexity tradeoff.
func biUniq(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "uniq")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("uniq: expected no arguments besides input, got %d", len(rest))
	}
	var out []value.Value
	for _, e := range lst.Elems {
		dup := false
		for _, seen := range out {
			if value.Equal(seen, e) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, e)
		}
	}
	return value.NewList(out), nil
}

// biFlatten splices one level of nested Lists into the outer list;
// anything else passes through unchanged. Not recursive — a List
// nested two levels deep still comes out one level deep, matching a
// typical shell's "flatten" rather than a full deep-flatten.
func biFlatten(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "flatten")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("flatten: expected no arguments besides input, got %d", len(rest))
	}
	var out []value.Value
	for _, e := range lst.Elems {
		if inner, ok := e.(*value.List); ok {
			out = append(out, inner.Elems...)
		} else {
			out = append(out, e)
		}
	}
	return value.NewList(out), nil
}

// biJoin stringifies every element via .String() rather than requiring
// value.String specifically — a list of Path/Int/whatever is just as
// joinable as a list of strings, matching how renderForExternal already
// leans on .String() broadly elsewhere in this package.
func biJoin(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "join")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("join: expected 1 separator argument, got %d", len(rest))
	}
	sep, ok := rest[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("join: separator argument must be a string, got %s", rest[0].Kind())
	}
	parts := make([]string, len(lst.Elems))
	for i, e := range lst.Elems {
		parts[i] = e.String()
	}
	return value.String(strings.Join(parts, string(sep))), nil
}

// biSum folds evalArith(PLUS, ...) over the list, so it promotes
// Int->Float exactly the way kyu's own `+` operator already does — not
// a separate numeric-coercion rule invented just for sum/avg.
func biSum(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "sum")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("sum: expected no arguments besides input, got %d", len(rest))
	}
	var acc value.Value = value.Int(0)
	for i, e := range lst.Elems {
		acc, err = evalArith(token.PLUS, acc, e)
		if err != nil {
			return nil, fmt.Errorf("sum: element %d: %w", i, err)
		}
	}
	return acc, nil
}

func biMin(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "min")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("min: expected no arguments besides input, got %d", len(rest))
	}
	if len(lst.Elems) == 0 {
		return value.Null{}, nil
	}
	best := lst.Elems[0]
	for _, e := range lst.Elems[1:] {
		c, err := value.Compare(e, best)
		if err != nil {
			return nil, fmt.Errorf("min: %w", err)
		}
		if c < 0 {
			best = e
		}
	}
	return best, nil
}

func biMax(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "max")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("max: expected no arguments besides input, got %d", len(rest))
	}
	if len(lst.Elems) == 0 {
		return value.Null{}, nil
	}
	best := lst.Elems[0]
	for _, e := range lst.Elems[1:] {
		c, err := value.Compare(e, best)
		if err != nil {
			return nil, fmt.Errorf("max: %w", err)
		}
		if c > 0 {
			best = e
		}
	}
	return best, nil
}

// biAvg always returns a Float, even for an all-Int list (1+2)/2 should
// be 1.5, not 1 via integer division) — errors on an empty list rather
// than silently returning 0, which could be mistaken for a real average.
func biAvg(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "avg")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("avg: expected no arguments besides input, got %d", len(rest))
	}
	if len(lst.Elems) == 0 {
		return nil, fmt.Errorf("avg: empty list")
	}
	var acc value.Value = value.Int(0)
	for i, e := range lst.Elems {
		acc, err = evalArith(token.PLUS, acc, e)
		if err != nil {
			return nil, fmt.Errorf("avg: element %d: %w", i, err)
		}
	}
	sum, ok := toFloat(acc)
	if !ok {
		return nil, fmt.Errorf("avg: cannot convert sum to float")
	}
	return value.Float(sum / float64(len(lst.Elems))), nil
}

func biAny(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "any")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("any: expected 1 predicate argument, got %d", len(rest))
	}
	pred := rest[0]
	for _, row := range lst.Elems {
		res, err := call(pred, []value.Value{row})
		if err != nil {
			return nil, err
		}
		if value.Truthy(res) {
			return value.Bool(true), nil
		}
	}
	return value.Bool(false), nil
}

// biAll is vacuously true for an empty list, the standard convention
// (there's no element that fails the predicate).
func biAll(args []value.Value) (value.Value, error) {
	lst, rest, err := lastAsList(args, "all")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("all: expected 1 predicate argument, got %d", len(rest))
	}
	pred := rest[0]
	for _, row := range lst.Elems {
		res, err := call(pred, []value.Value{row})
		if err != nil {
			return nil, err
		}
		if !value.Truthy(res) {
			return value.Bool(false), nil
		}
	}
	return value.Bool(true), nil
}

func biToJSON(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("to_json: expected exactly 1 argument, got %d", len(args))
	}
	j, err := kyuToJSON(args[0])
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("to_json: %v", err)}, nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("to_json: %v", err)}, nil
	}
	return value.String(b), nil
}

func biFromJSON(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("from_json: expected exactly 1 argument, got %d", len(args))
	}
	s, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("from_json: expected a string, got %s", args[0].Kind())
	}
	var raw any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return value.ErrorVal{Msg: fmt.Sprintf("from_json: %v", err)}, nil
	}
	return jsonToKyu(raw), nil
}

// lastAsString mirrors lastAsList, for the string builtins below.
func lastAsString(args []value.Value, fnName string) (value.String, []value.Value, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("%s: missing input", fnName)
	}
	input := args[len(args)-1]
	rest := args[:len(args)-1]
	s, ok := input.(value.String)
	if !ok {
		return "", nil, fmt.Errorf("%s: expected a string input, got %s", fnName, input.Kind())
	}
	return s, rest, nil
}

func biSplit(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "split")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("split: expected 1 separator argument, got %d", len(rest))
	}
	sep, ok := rest[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("split: separator argument must be a string, got %s", rest[0].Kind())
	}
	parts := strings.Split(string(s), string(sep))
	out := make([]value.Value, len(parts))
	for i, p := range parts {
		out[i] = value.String(p)
	}
	return value.NewList(out), nil
}

func biTrim(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "trim")
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("trim: expected no arguments besides input, got %d", len(rest))
	}
	return value.String(strings.TrimSpace(string(s))), nil
}

func biReplace(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "replace")
	if err != nil {
		return nil, err
	}
	if len(rest) != 2 {
		return nil, fmt.Errorf("replace: expected 2 arguments (old, new), got %d", len(rest))
	}
	oldS, ok := rest[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("replace: old argument must be a string, got %s", rest[0].Kind())
	}
	newS, ok := rest[1].(value.String)
	if !ok {
		return nil, fmt.Errorf("replace: new argument must be a string, got %s", rest[1].Kind())
	}
	return value.String(strings.ReplaceAll(string(s), string(oldS), string(newS))), nil
}

// biFormat implements format(template, values...): each "{}" in
// template is replaced, in order, by the next value's .String() —
// simple positional interpolation rather than new string-literal
// syntax (no lexer/parser changes needed at all), consistent with how
// this session has generally preferred a builtin over new grammar
// where the two give the same capability. template's placeholder count
// must exactly match len(values) — a mismatch is a hard error, the
// same "strict argument count" stance select's field-name check
// already takes, to catch a wrong-count typo rather than silently
// truncate/leave placeholders unfilled.
//
// values is also where a pipe's appended input value lands (e.g.
// `name | format("hello {}")`, matching every other builtin's
// "explicit args..., then the input" convention) -- there's no special
// casing for it here since a single trailing positional value is
// exactly what values already handles.
func biFormat(args []value.Value) (value.Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("format: expected a template argument")
	}
	tmpl, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("format: template argument must be a string, got %s", args[0].Kind())
	}
	values := args[1:]
	parts := strings.Split(string(tmpl), "{}")
	if len(parts)-1 != len(values) {
		return nil, fmt.Errorf("format: template has %d placeholder(s), got %d value(s)", len(parts)-1, len(values))
	}
	var b strings.Builder
	for i, p := range parts {
		b.WriteString(p)
		if i < len(values) {
			b.WriteString(values[i].String())
		}
	}
	return value.String(b.String()), nil
}

func biContains(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "contains")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("contains: expected 1 needle argument, got %d", len(rest))
	}
	needle, ok := rest[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("contains: needle argument must be a string, got %s", rest[0].Kind())
	}
	return value.Bool(strings.Contains(string(s), string(needle))), nil
}
