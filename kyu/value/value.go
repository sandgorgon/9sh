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

// FieldBacking makes a record field live-backed by something outside the
// record itself — a namespace file, per kyu's core idea that a record and
// namespace state are the same concept: reading job.status re-reads the
// backing fresh, writing job.ctl = "stop" writes through. The value
// package only defines the hook; kyu/eval supplies the concrete
// namespace-file implementation, keeping this package free of any
// dependency on the namespace/9P machinery.
type FieldBacking interface {
	ReadField() (Value, error)
	WriteField(Value) error
}

// FieldDisplay is an optional refinement of FieldBacking: a backing that
// implements it controls how it's shown when the *containing record* is
// stringified (Record.String()), without necessarily performing the real
// read ReadField does. Explicit field access (rec.Get, kyu's `.field`)
// always goes through ReadField unchanged — this only affects a record's
// own aggregate display, for a field whose real read blocks (a job's
// "wait", which waits for the job to finish) or fails by design (a
// write-only "ctl") — printing the record shouldn't hang or spam an
// expected failure just to render a summary.
type FieldDisplay interface {
	DisplayField() string
}

// NSUnion is a namespace-union expression's value — `ns := /a + /b` — an
// ordered list of namespace paths to search in order, as `bind`'s source.
// It's kyu-level syntax sugar over ns.Namespace.BindPath's multi-source
// form, not a namespace concept of its own: nothing resolves a union
// until it's actually bound somewhere.
type NSUnion struct {
	Paths []Path
}

func (NSUnion) Kind() string { return "nsunion" }
func (u NSUnion) String() string {
	var b strings.Builder
	for i, p := range u.Paths {
		if i > 0 {
			b.WriteString(" + ")
		}
		b.WriteString(string(p))
	}
	return b.String()
}

// MountHandle is what dial(addr) returns: an unbound remote filesystem
// root, ready for `bind` to graft into the local namespace (bind detects
// this kind and routes to ns.Namespace.BindFS instead of BindPath — see
// kyu/eval/namespace.go's evalBindStmt). FS is `any` (concretely a
// server.FileSystem, from package remote) so this package keeps its
// documented independence from the namespace/9P machinery; only kyu/eval
// ever type-asserts it back out.
type MountHandle struct {
	Addr string
	FS   any
}

func (MountHandle) Kind() string     { return "mount" }
func (m MountHandle) String() string { return fmt.Sprintf("<mount %s>", m.Addr) }

// Record is an ordered field map. Field order is preserved for stable
// printing/serialization; lookups are O(1) via the index map. A field may
// also be live-backed (see FieldBacking) instead of holding a plain value.
type Record struct {
	keys    []string
	vals    map[string]Value
	index   map[string]int
	backing map[string]FieldBacking
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
		if d, ok := r.backing[k].(FieldDisplay); ok {
			b.WriteString(d.DisplayField())
		} else {
			v, _ := r.Get(k)
			b.WriteString(v.String())
		}
	}
	b.WriteString("}")
	return b.String()
}

// Get returns the field's value and whether it was present. A live-backed
// field is re-read fresh on every call; a failed read surfaces as an
// ErrorVal (see the type's own doc comment on kyu's in-stream error
// model), not a Go-level error — Get always succeeds once the field
// exists.
func (r *Record) Get(name string) (Value, bool) {
	if b, ok := r.backing[name]; ok {
		v, err := b.ReadField()
		if err != nil {
			return ErrorVal{Msg: err.Error()}, true
		}
		return v, true
	}
	v, ok := r.vals[name]
	return v, ok
}

// Set adds or overwrites a plain (non-backed) field, preserving original
// insertion order on overwrite. Setting a name that's currently
// live-backed replaces the backing with a plain value — used by internal
// construction paths (record literals, select/group-by output) that
// never target an already-backed record; assignment from kyu source
// (`rec.field = val`) goes through SetField instead, which writes through
// a backing rather than silently detaching it.
func (r *Record) Set(name string, v Value) {
	if _, ok := r.index[name]; !ok {
		r.index[name] = len(r.keys)
		r.keys = append(r.keys, name)
	}
	delete(r.backing, name)
	r.vals[name] = v
}

// SetField assigns to a field the way kyu's `rec.field = val` does: a
// live-backed field writes through (and reports a write failure as a real
// error, since an assignment statement can't silently drop it the way a
// falsy read can); anything else is a plain Set.
func (r *Record) SetField(name string, v Value) error {
	if b, ok := r.backing[name]; ok {
		return b.WriteField(v)
	}
	r.Set(name, v)
	return nil
}

// SetBacking makes name a live-backed field.
func (r *Record) SetBacking(name string, b FieldBacking) {
	if _, ok := r.index[name]; !ok {
		r.index[name] = len(r.keys)
		r.keys = append(r.keys, name)
	}
	if r.backing == nil {
		r.backing = map[string]FieldBacking{}
	}
	r.backing[name] = b
	delete(r.vals, name)
}

// Keys returns field names in insertion order.
func (r *Record) Keys() []string {
	out := make([]string, len(r.keys))
	copy(out, r.keys)
	return out
}

// Clone returns a shallow copy: plain values are copied as-is, and a
// live-backed field stays backed by the same FieldBacking (not
// snapshotted to its current value) — cloning doesn't sever the live
// connection.
func (r *Record) Clone() *Record {
	c := NewRecord()
	for _, k := range r.keys {
		if b, ok := r.backing[k]; ok {
			c.SetBacking(k, b)
			continue
		}
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
			avv, _ := av.Get(k)
			bvv, ok := bv.Get(k)
			if !ok || !Equal(avv, bvv) {
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
