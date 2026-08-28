package value

import "testing"

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
