package value

import (
	"errors"
	"testing"
)

func TestEqual(t *testing.T) {
	if !Equal(Int(5), Int(5)) {
		t.Error("Int(5) should equal Int(5)")
	}
	if Equal(Int(5), Float(5)) {
		t.Error("Int(5) should not equal Float(5) — Equal requires matching Kind")
	}
	r1 := RecordFrom([]RecordField{{"a", Int(1)}, {"b", String("x")}})
	r2 := RecordFrom([]RecordField{{"a", Int(1)}, {"b", String("x")}})
	if !Equal(r1, r2) {
		t.Error("records with the same fields should be equal")
	}
	r3 := RecordFrom([]RecordField{{"a", Int(2)}, {"b", String("x")}})
	if Equal(r1, r3) {
		t.Error("records with a differing field should not be equal")
	}
}

func TestCompare(t *testing.T) {
	c, err := Compare(Int(1), Int(2))
	if err != nil || c >= 0 {
		t.Errorf("Compare(1,2) = %d, %v; want <0, nil", c, err)
	}
	c, err = Compare(Int(2), Float(2.0))
	if err != nil || c != 0 {
		t.Errorf("Compare(Int(2),Float(2.0)) = %d, %v; want 0, nil", c, err)
	}
	if _, err := Compare(String("a"), Int(1)); err == nil {
		t.Error("comparing a string to an int should error")
	}
}

func TestTruthy(t *testing.T) {
	cases := []struct {
		v    Value
		want bool
	}{
		{Null{}, false},
		{Bool(false), false},
		{Bool(true), true},
		{Int(0), true}, // zero values are truthy — only false/null/error are falsy
		{String(""), true},
		{ErrorVal{Msg: "x"}, false},
	}
	for _, c := range cases {
		if got := Truthy(c.v); got != c.want {
			t.Errorf("Truthy(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestRecordOrderPreserved(t *testing.T) {
	r := NewRecord()
	r.Set("z", Int(1))
	r.Set("a", Int(2))
	r.Set("z", Int(3)) // overwrite should not move it in Keys()
	want := []string{"z", "a"}
	got := r.Keys()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	v, _ := r.Get("z")
	if v.(Int) != 3 {
		t.Errorf("Get(z) = %v, want 3 (overwritten)", v)
	}
}

// fakeBacking is a trivial in-memory FieldBacking for testing the
// live-field mechanics without any real namespace/9P plumbing.
type fakeBacking struct {
	val      Value
	readErr  error
	writable bool
}

func (b *fakeBacking) ReadField() (Value, error) {
	if b.readErr != nil {
		return nil, b.readErr
	}
	return b.val, nil
}
func (b *fakeBacking) WriteField(v Value) error {
	if !b.writable {
		return errors.New("field is read-only")
	}
	b.val = v
	return nil
}

func TestBackedFieldReadIsLive(t *testing.T) {
	b := &fakeBacking{val: Int(1)}
	r := NewRecord()
	r.SetBacking("status", b)

	v, ok := r.Get("status")
	if !ok || v.(Int) != 1 {
		t.Fatalf("Get(status) = %v, %v; want 1, true", v, ok)
	}
	b.val = Int(2) // change the backing directly, not through the record
	v, _ = r.Get("status")
	if v.(Int) != 2 {
		t.Fatalf("Get(status) after backing change = %v, want 2 (should re-read live)", v)
	}
}

func TestBackedFieldReadErrorBecomesErrorVal(t *testing.T) {
	b := &fakeBacking{readErr: errors.New("boom")}
	r := NewRecord()
	r.SetBacking("status", b)

	v, ok := r.Get("status")
	if !ok {
		t.Fatal("Get should report the field present even when the backing read fails")
	}
	ev, ok := v.(ErrorVal)
	if !ok || ev.Msg != "boom" {
		t.Fatalf("Get(status) = %#v, want ErrorVal{boom}", v)
	}
	if Truthy(v) {
		t.Error("an ErrorVal from a failed backed read should be falsy")
	}
}

func TestSetFieldWritesThroughBacking(t *testing.T) {
	b := &fakeBacking{val: String("pending"), writable: true}
	r := NewRecord()
	r.SetBacking("ctl", b)

	if err := r.SetField("ctl", String("stop")); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if b.val.(String) != "stop" {
		t.Fatalf("backing value = %v, want stop (write-through)", b.val)
	}
}

func TestSetFieldReportsWriteFailure(t *testing.T) {
	b := &fakeBacking{val: String("x"), writable: false}
	r := NewRecord()
	r.SetBacking("status", b)

	if err := r.SetField("status", String("y")); err == nil {
		t.Fatal("SetField on a read-only backed field should return an error")
	}
}

func TestPlainSetDetachesBacking(t *testing.T) {
	b := &fakeBacking{val: Int(1)}
	r := NewRecord()
	r.SetBacking("x", b)
	r.Set("x", Int(99)) // internal construction paths use Set, not SetField

	v, _ := r.Get("x")
	if v.(Int) != 99 {
		t.Fatalf("Get(x) = %v, want 99 (plain Set should detach the backing)", v)
	}
}

func TestCloneSharesBacking(t *testing.T) {
	b := &fakeBacking{val: Int(1)}
	r := NewRecord()
	r.SetBacking("x", b)
	c := r.Clone()

	b.val = Int(7)
	v, _ := c.Get("x")
	if v.(Int) != 7 {
		t.Fatalf("clone's Get(x) = %v, want 7 (Clone should share the live backing, not snapshot it)", v)
	}
}
