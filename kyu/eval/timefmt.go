package eval

import (
	"fmt"
	"time"

	"github.com/sandgorgon/9sh/kyu/value"
)

// epochArg extracts a Unix-epoch-seconds Int from v -- the shape
// stat/ls's mtime/atime fields already are (see stat.go's statRecord doc
// comment: kyu has no time-of-day value type yet, only Duration, an
// elapsed span, so a point in time is a plain Int until one exists).
func epochArg(fn string, v value.Value) (int64, error) {
	n, ok := v.(value.Int)
	if !ok {
		return 0, fmt.Errorf("%s: expected an Int (Unix epoch seconds, e.g. stat's mtime/atime), got %s", fn, v.Kind())
	}
	return int64(n), nil
}

// biFormatTime implements format_time(layout, epoch_seconds) --
// epoch_seconds trailing, same "explicit args..., then the input"
// convention round()/format() already use, so it pipes naturally:
// stat("/x") | select("mtime") | ... | format_time("2006-01-02 15:04:05").
// layout is Go's reference-time layout string (time.Time.Format's own
// convention) -- reused as-is rather than inventing a strftime-style mini
// language, consistent with this session's "builtin over new syntax"
// principle: kyu scripts are already Go-adjacent (format()'s "{}" is the
// one exception, and that's positional interpolation, not a date DSL).
func biFormatTime(args []value.Value) (value.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("format_time: expected 2 arguments (layout, epoch_seconds), got %d", len(args))
	}
	layout, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("format_time: layout argument must be a string, got %s", args[0].Kind())
	}
	sec, err := epochArg("format_time", args[1])
	if err != nil {
		return value.ErrorVal{Msg: err.Error()}, nil
	}
	return value.String(time.Unix(sec, 0).Format(string(layout))), nil
}

// biHumanizeTime implements humanize_time(epoch_seconds): a relative,
// human-scale rendering ("just now", "5 minutes ago", "in 3 hours", ...)
// against time.Now() -- the quick "is this fresh or stale" glance
// format_time's fixed layout doesn't give you without the caller doing
// their own subtraction.
func biHumanizeTime(args []value.Value) (value.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("humanize_time: expected 1 argument (epoch_seconds), got %d", len(args))
	}
	sec, err := epochArg("humanize_time", args[0])
	if err != nil {
		return value.ErrorVal{Msg: err.Error()}, nil
	}
	return value.String(humanizeDuration(time.Since(time.Unix(sec, 0)))), nil
}

// humanizeDuration renders d (may be negative, for a future timestamp)
// as a short relative phrase, picking the coarsest unit that stays
// meaningful (60 "seconds ago" is worse than "1 minute ago") -- the same
// judgment call every relative-time library makes, kept intentionally
// simple (one unit, no "X hours Y minutes" composition) since this is
// for a quick glance, not a precise duration report (Duration/its own
// String() already exists for that).
func humanizeDuration(d time.Duration) string {
	future := d < 0
	if future {
		d = -d
	}
	var phrase string
	switch {
	case d < 30*time.Second:
		return "just now"
	case d < time.Minute:
		phrase = fmt.Sprintf("%d seconds", int(d/time.Second))
	case d < time.Hour:
		phrase = pluralUnit(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		phrase = pluralUnit(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		phrase = pluralUnit(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		phrase = pluralUnit(int(d/(30*24*time.Hour)), "month")
	default:
		phrase = pluralUnit(int(d/(365*24*time.Hour)), "year")
	}
	if future {
		return "in " + phrase
	}
	return phrase + " ago"
}

func pluralUnit(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
