// Package value implements kyu's runtime data model: the tagged union
// Null|Bool|Int|Float|String|Bytes|Path|Duration|Ref|Record|List, where
// Table is just "a List whose elements are Records" rather than a distinct
// wrapper type.
package value

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Value is any kyu runtime value.
type Value interface {
	Kind() string
	String() string
}

type Null struct{}

func (Null) Kind() string   { return "null" }
func (Null) String() string { return "null" }

type Bool bool

func (Bool) Kind() string     { return "bool" }
func (b Bool) String() string { return strconv.FormatBool(bool(b)) }

type Int int64

func (Int) Kind() string     { return "int" }
func (i Int) String() string { return strconv.FormatInt(int64(i), 10) }

type Float float64

func (Float) Kind() string     { return "float" }
func (f Float) String() string { return strconv.FormatFloat(float64(f), 'g', -1, 64) }

type String string

func (String) Kind() string     { return "string" }
func (s String) String() string { return string(s) }

type Bytes []byte

func (Bytes) Kind() string     { return "bytes" }
func (b Bytes) String() string { return fmt.Sprintf("<%d bytes>", len(b)) }

// Path is its own scalar type, not just a string — e.g. `/local/bin`.
type Path string

func (Path) Kind() string     { return "path" }
func (p Path) String() string { return string(p) }

type Duration time.Duration

func (Duration) Kind() string     { return "duration" }
func (d Duration) String() string { return time.Duration(d).String() }

// Ref is a live namespace reference descriptor: Path (with optional Host)
// identifies a namespace node; Hint names the node type (e.g. "job", "file").
// Not yet live-backed in Phase 1 — that lands with the /jobs namespace.
type Ref struct {
	Path string
	Host string
	Hint string
}

func (Ref) Kind() string { return "ref" }
func (r Ref) String() string {
	if r.Host != "" {
		return fmt.Sprintf("/n/%s%s", r.Host, r.Path)
	}
	return r.Path
}

// ErrorVal is an in-stream error: kyu's model is that a failure (a bad
// external-command exec, a failed predicate) becomes a value flowing
// through the pipeline rather than aborting it outright. It is falsy
// (see Truthy), so `where` naturally drops error rows without special
// casing; the `?` postfix operator is what escalates one to a hard abort.
type ErrorVal struct {
	Msg string
}

func (ErrorVal) Kind() string     { return "error" }
func (e ErrorVal) String() string { return "error: " + e.Msg }

// Record is an ordered field map. Field order is preserved for stable
// printing/serialization; lookups are O(1) via the index map.
type Record struct {
	keys  []string
	vals  map[string]Value
	index map[string]int
}

func NewRecord() *Record {
	return &Record{vals: map[string]Value{}, index: map[string]int{}}
}

func RecordFrom(fields []RecordField) *Record {
	r := NewRecord()
	for _, f := range fields {
		r.Set(f.Name, f.Value)
	}
	return r
}

type RecordField struct {
	Name  string
	Value Value
}

func (r *Record) Kind() string { return "record" }

func (r *Record) String() string {
	var b strings.Builder
	b.WriteString("{")
	for i, k := range r.keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(r.vals[k].String())
	}
	b.WriteString("}")
	return b.String()
}

// Get returns the field's value and whether it was present.
func (r *Record) Get(name string) (Value, bool) {
	v, ok := r.vals[name]
	return v, ok
}

// Set adds or overwrites a field, preserving original insertion order on overwrite.
func (r *Record) Set(name string, v Value) {
	if _, ok := r.index[name]; !ok {
		r.index[name] = len(r.keys)
		r.keys = append(r.keys, name)
	}
	r.vals[name] = v
}

// Keys returns field names in insertion order.
func (r *Record) Keys() []string {
	out := make([]string, len(r.keys))
	copy(out, r.keys)
	return out
}

// Clone returns a shallow copy (field values are not deep-copied).
func (r *Record) Clone() *Record {
	c := NewRecord()
	for _, k := range r.keys {
		c.Set(k, r.vals[k])
	}
	return c
}

// List is an ordered sequence of values. A Table is a List whose elements
// are all *Record — that's a usage convention, not a distinct Go type.
type List struct {
	Elems []Value
}

func NewList(elems []Value) *List { return &List{Elems: elems} }

func (*List) Kind() string { return "list" }

func (l *List) String() string {
	var b strings.Builder
	b.WriteString("[")
	for i, e := range l.Elems {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(e.String())
	}
	b.WriteString("]")
	return b.String()
}

// IsTable reports whether every element is a *Record — the convention that
// distinguishes a Table from an arbitrary List.
func (l *List) IsTable() bool {
	for _, e := range l.Elems {
		if _, ok := e.(*Record); !ok {
			return false
		}
	}
	return true
}

// Truthy defines kyu's truthiness for `if`/`where` conditions: only false
// and null are falsy; zero values (0, "", empty list) are truthy, matching
// the intuition that a present-but-zero record field is not "missing".
func Truthy(v Value) bool {
	switch x := v.(type) {
	case Null:
		return false
	case Bool:
		return bool(x)
	case ErrorVal:
		return false
	default:
		return true
	}
}

// Equal reports value equality for `==`/`!=` and for group-by keys.
func Equal(a, b Value) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	switch av := a.(type) {
	case Null:
		return true
	case Bool:
		return av == b.(Bool)
	case Int:
		return av == b.(Int)
	case Float:
		return av == b.(Float)
	case String:
		return av == b.(String)
	case Path:
		return av == b.(Path)
	case Duration:
		return av == b.(Duration)
	case Bytes:
		return string(av) == string(b.(Bytes))
	case Ref:
		return av == b.(Ref)
	case ErrorVal:
		return av == b.(ErrorVal)
	case *Record:
		bv := b.(*Record)
		if len(av.keys) != len(bv.keys) {
			return false
		}
		for _, k := range av.keys {
			bvv, ok := bv.Get(k)
			if !ok || !Equal(av.vals[k], bvv) {
				return false
			}
		}
		return true
	case *List:
		bv := b.(*List)
		if len(av.Elems) != len(bv.Elems) {
			return false
		}
		for i := range av.Elems {
			if !Equal(av.Elems[i], bv.Elems[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// Compare orders two values for `<`/`>`/`<=`/`>=`/sort-by. Returns an error
// for kinds with no natural order or a kind mismatch.
func Compare(a, b Value) (int, error) {
	switch av := a.(type) {
	case Int:
		switch bv := b.(type) {
		case Int:
			return cmp(int64(av), int64(bv)), nil
		case Float:
			return cmp(float64(av), float64(bv)), nil
		}
	case Float:
		switch bv := b.(type) {
		case Int:
			return cmp(float64(av), float64(bv)), nil
		case Float:
			return cmp(float64(av), float64(bv)), nil
		}
	case String:
		if bv, ok := b.(String); ok {
			return cmp(string(av), string(bv)), nil
		}
	case Path:
		if bv, ok := b.(Path); ok {
			return cmp(string(av), string(bv)), nil
		}
	case Duration:
		if bv, ok := b.(Duration); ok {
			return cmp(int64(av), int64(bv)), nil
		}
	}
	return 0, fmt.Errorf("cannot compare %s and %s", a.Kind(), b.Kind())
}

type ordered interface {
	~int64 | ~float64 | ~string
}

func cmp[T ordered](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
