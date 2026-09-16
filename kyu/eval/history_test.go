package eval

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestHistoryNotAvailableWithoutHook(t *testing.T) {
	env := NewGlobalEnv(nil)
	if err := runEnvErr(t, `history()`, env); err == nil {
		t.Fatalf("expected a hard error, got none")
	}
}

func TestHistoryDeleteNotAvailableWithoutHook(t *testing.T) {
	env := NewGlobalEnv(nil)
	if err := runEnvErr(t, `history_delete(0)`, env); err == nil {
		t.Fatalf("expected a hard error, got none")
	}
}

func TestHistoryClearNotAvailableWithoutHook(t *testing.T) {
	env := NewGlobalEnv(nil)
	if err := runEnvErr(t, `history_clear()`, env); err == nil {
		t.Fatalf("expected a hard error, got none")
	}
}

func TestHistoryListsEntriesAsIndexedRecords(t *testing.T) {
	env := NewGlobalEnv(nil)
	env.SetHistoryAccess(&HistoryAccess{
		List: func() []string { return []string{"first", "second"} },
	})
	v := runEnv(t, `history()`, env)
	lst, ok := v.(*value.List)
	if !ok || len(lst.Elems) != 2 {
		t.Fatalf("want a 2-element List, got %#v", v)
	}
	rec0, ok := lst.Elems[0].(*value.Record)
	if !ok {
		t.Fatalf("want *value.Record, got %#v", lst.Elems[0])
	}
	idx, _ := rec0.Get("index")
	text, _ := rec0.Get("text")
	if idx != value.Int(0) || text != value.String("first") {
		t.Errorf("entry 0 = {index: %#v, text: %#v}, want {0, \"first\"}", idx, text)
	}
}

func TestHistoryDeleteCallsHookAndReportsResult(t *testing.T) {
	env := NewGlobalEnv(nil)
	var deletedIndex int
	env.SetHistoryAccess(&HistoryAccess{
		Delete: func(index int) bool {
			deletedIndex = index
			return index == 1
		},
	})
	if v := runEnv(t, `history_delete(1)`, env); v != value.Bool(true) {
		t.Errorf("history_delete(1) = %#v, want true", v)
	}
	if deletedIndex != 1 {
		t.Errorf("hook called with index %d, want 1", deletedIndex)
	}
	if v := runEnv(t, `history_delete(5)`, env); v != value.Bool(false) {
		t.Errorf("history_delete(5) = %#v, want false", v)
	}
}

func TestHistoryClearCallsHook(t *testing.T) {
	env := NewGlobalEnv(nil)
	cleared := false
	env.SetHistoryAccess(&HistoryAccess{Clear: func() { cleared = true }})
	runEnv(t, `history_clear()`, env)
	if !cleared {
		t.Fatal("expected the Clear hook to be called")
	}
}
