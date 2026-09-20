package eval

import (
	"fmt"
	"regexp"

	"github.com/sandgorgon/9sh/kyu/value"
)

// maxRangeLen bounds range(): it materializes a List, so an accidental
// range(1000000000) would otherwise eat all memory before failing.
const maxRangeLen = 1_000_000

// biRange implements range(n), range(start, stop) and range(start, stop,
// step): a List of Ints, half-open like slicing (range(3) is [0, 1, 2]).
// A negative step counts down; a zero step is an error, as is a result
// longer than maxRangeLen. It takes no piped input.
func biRange(args []value.Value) (value.Value, error) {
	if len(args) < 1 || len(args) > 3 {
		return nil, fmt.Errorf("range: expected 1 to 3 arguments (stop | start, stop [, step]), got %d", len(args))
	}
	n := make([]int64, len(args))
	for i, a := range args {
		v, ok := a.(value.Int)
		if !ok {
			return nil, fmt.Errorf("range: argument %d must be an int, got %s", i+1, a.Kind())
		}
		n[i] = int64(v)
	}
	var start, stop, step int64 = 0, 0, 1
	switch len(n) {
	case 1:
		stop = n[0]
	case 2:
		start, stop = n[0], n[1]
	case 3:
		start, stop, step = n[0], n[1], n[2]
	}
	if step == 0 {
		return nil, fmt.Errorf("range: step cannot be 0")
	}
	var out []value.Value
	for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
		if len(out) >= maxRangeLen {
			return nil, fmt.Errorf("range: more than %d elements", maxRangeLen)
		}
		out = append(out, value.Int(i))
	}
	return value.NewList(out), nil
}

// biZip implements `xs | zip(ys)`: a List of two-element Lists pairing
// elements by position, stopping at the shorter input. Like every
// builtin the piped value arrives last, so xs is args[1] and ys args[0];
// pairs come out as [x, y], matching how the pipe reads.
func biZip(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("zip: expected 2 lists (the piped input and one argument), got %d arguments", len(args))
	}
	b, ok := args[0].(*value.List)
	if !ok {
		return nil, fmt.Errorf("zip: expected a list, got %s", args[0].Kind())
	}
	a, ok := args[1].(*value.List)
	if !ok {
		return nil, fmt.Errorf("zip: expected a list, got %s", args[1].Kind())
	}
	n := min(len(a.Elems), len(b.Elems))
	out := make([]value.Value, n)
	for i := 0; i < n; i++ {
		out[i] = value.NewList([]value.Value{a.Elems[i], b.Elems[i]})
	}
	return value.NewList(out), nil
}

// recordArg is the shared front end for keys/values: exactly one Record.
func recordArg(fn string, args []value.Value) (*value.Record, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s: expected 1 record argument (the piped input), got %d", fn, len(args))
	}
	r, ok := args[0].(*value.Record)
	if !ok {
		return nil, fmt.Errorf("%s: expected a record, got %s", fn, args[0].Kind())
	}
	return r, nil
}

// biKeys implements `record | keys`: the field names, in the record's
// own order, as a List of Strings. Never touches a field's value, so it's
// safe on a live-backed record (a job's blocking "wait" isn't read).
func biKeys(args []value.Value) (value.Value, error) {
	r, err := recordArg("keys", args)
	if err != nil {
		return nil, err
	}
	names := r.Keys()
	out := make([]value.Value, len(names))
	for i, k := range names {
		out[i] = value.String(k)
	}
	return value.NewList(out), nil
}

// biValues implements `record | values`: the field values in key order.
// A live-backed field is read fresh, the same as record.field or
// to_json() — so values() of a job record blocks on its "wait" field and
// fails on its write-only "ctl"; use keys and get_field to pick fields.
func biValues(args []value.Value) (value.Value, error) {
	r, err := recordArg("values", args)
	if err != nil {
		return nil, err
	}
	names := r.Keys()
	out := make([]value.Value, len(names))
	for i, k := range names {
		v, _ := r.Get(k)
		out[i] = v
	}
	return value.NewList(out), nil
}

// compileRegex compiles a kyu String pattern. Go's regexp is RE2:
// linear-time, so a hostile pattern or input can't hang the shell, at
// the cost of no backreferences or lookaround. A bad pattern is a hard
// error like any malformed call, not an in-stream value.
func compileRegex(fn string, v value.Value) (*regexp.Regexp, error) {
	s, ok := v.(value.String)
	if !ok {
		return nil, fmt.Errorf("%s: pattern must be a string, got %s", fn, v.Kind())
	}
	re, err := regexp.Compile(string(s))
	if err != nil {
		return nil, fmt.Errorf("%s: bad pattern: %v", fn, err)
	}
	return re, nil
}

// biMatch implements `s | match(pattern)`: whether pattern matches
// anywhere in s (anchor with ^ and $ for a whole-string test).
func biMatch(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "match")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("match: expected 1 pattern argument, got %d", len(rest))
	}
	re, err := compileRegex("match", rest[0])
	if err != nil {
		return nil, err
	}
	return value.Bool(re.MatchString(string(s))), nil
}

// biCapture implements `s | capture(pattern)`: the first match as a List
// of Strings — the whole match, then each parenthesized group — or Null
// when nothing matches. A group that didn't take part in the match (an
// unused alternative, an optional group) is Null in its slot.
func biCapture(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "capture")
	if err != nil {
		return nil, err
	}
	if len(rest) != 1 {
		return nil, fmt.Errorf("capture: expected 1 pattern argument, got %d", len(rest))
	}
	re, err := compileRegex("capture", rest[0])
	if err != nil {
		return nil, err
	}
	idx := re.FindStringSubmatchIndex(string(s))
	if idx == nil {
		return value.Null{}, nil
	}
	out := make([]value.Value, len(idx)/2)
	for i := range out {
		if idx[2*i] < 0 {
			out[i] = value.Null{}
			continue
		}
		out[i] = value.String(string(s)[idx[2*i]:idx[2*i+1]])
	}
	return value.NewList(out), nil
}

// biReplaceRe implements `s | replace_re(pattern, repl)`: every match
// replaced, with $1/${name} in repl expanded to that group (Go's
// Regexp.ReplaceAllString). replace() stays the literal, no-regex form.
func biReplaceRe(args []value.Value) (value.Value, error) {
	s, rest, err := lastAsString(args, "replace_re")
	if err != nil {
		return nil, err
	}
	if len(rest) != 2 {
		return nil, fmt.Errorf("replace_re: expected 2 arguments (pattern, replacement), got %d", len(rest))
	}
	re, err := compileRegex("replace_re", rest[0])
	if err != nil {
		return nil, err
	}
	repl, ok := rest[1].(value.String)
	if !ok {
		return nil, fmt.Errorf("replace_re: replacement must be a string, got %s", rest[1].Kind())
	}
	return value.String(re.ReplaceAllString(string(s), string(repl))), nil
}
