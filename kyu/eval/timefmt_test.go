package eval

import (
	"testing"
	"time"

	"github.com/sandgorgon/9sh/kyu/value"
)

func TestFormatTime(t *testing.T) {
	// 2024-01-15 10:30:00 UTC
	epoch := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Unix()
	src := `format_time("2006-01-02 15:04:05 MST", ` + intLit(epoch) + `)`
	v := run(t, src)
	got, ok := v.(value.String)
	if !ok {
		t.Fatalf("got %#v (%s), want a String", v, v.Kind())
	}
	want := time.Unix(epoch, 0).Format("2006-01-02 15:04:05 MST")
	if string(got) != want {
		t.Errorf("format_time = %q, want %q", got, want)
	}
}

func TestFormatTimeWrongArgTypeIsErrorVal(t *testing.T) {
	v := run(t, `format_time("2006-01-02", "not an int")`)
	if v.Kind() != "error" {
		t.Fatalf("got %#v (%s), want an ErrorVal", v, v.Kind())
	}
}

func TestHumanizeTimeJustNow(t *testing.T) {
	v := run(t, `humanize_time(`+intLit(time.Now().Unix())+`)`)
	if v.(value.String) != "just now" {
		t.Errorf("humanize_time(now) = %v, want %q", v, "just now")
	}
}

func TestHumanizeTimePast(t *testing.T) {
	past := time.Now().Add(-5 * time.Minute).Unix()
	v := run(t, `humanize_time(`+intLit(past)+`)`)
	if v.(value.String) != "5 minutes ago" {
		t.Errorf("humanize_time(5m ago) = %v, want %q", v, "5 minutes ago")
	}
}

func TestHumanizeTimeFuture(t *testing.T) {
	// +5s margin: Unix() truncates to whole seconds at call time, and a
	// few ms elapse before humanize_time's own time.Now() runs -- right
	// at a 3h boundary that truncation can tip the result down to
	// "2 hours" instead of "3 hours". The margin keeps this well inside
	// the 3-hour bucket regardless of scheduling jitter.
	future := time.Now().Add(3*time.Hour + 5*time.Second).Unix()
	v := run(t, `humanize_time(`+intLit(future)+`)`)
	if v.(value.String) != "in 3 hours" {
		t.Errorf("humanize_time(3h future) = %v, want %q", v, "in 3 hours")
	}
}

// intLit renders n as a kyu integer literal for building test source
// strings -- fmt.Sprintf("%d", n) would work identically, this just
// keeps the call sites above reading as "an Int literal", not a stray
// numeric-format detail.
func intLit(n int64) string {
	return value.Int(n).String()
}
