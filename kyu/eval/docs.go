package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// BuiltinDoc is one language-reference entry — the single source of
// truth for both help(name) (biHelp, this file) and package replui's
// expanded F1 help screen's language-reference section (see
// replui/help.go), so the two can never drift apart the way two
// hand-maintained copies of the same reference would.
type BuiltinDoc struct {
	Name        string
	Signature   string
	Description string
}

// Docs returns every documented name, grouped by category (namespace,
// variables/env, process, data pipeline, strings, control flow/syntax)
// rather than alphabetically — matching the README's own Quick Start
// grouping, since learning the language by category reads better than
// an A-Z dump; help(name) already covers "I know the name, look it
// up" directly. Exported so replui/help.go (a different package) can
// render the same table without duplicating it.
func Docs() []BuiltinDoc {
	return builtinDocs
}

var builtinDocs = []BuiltinDoc{
	// Namespace
	{"bind", "bind SRC, DST[, before|after|replace]",
		"Grafts SRC onto the namespace at DST — a Path, a namespace-union expression (a + b), or a MountHandle from dial()/dir(). A statement, not a function; comma-separated, not space-separated."},
	{"unbind", "unbind DST",
		"Clears whatever's bound at DST. Unbinding somewhere nothing is bound is an error, unlike unsetenv's forgiving convention."},
	{"glob", `glob(pattern)`,
		"Matches namespace entries in one directory against pattern's final segment (*, ?, [...] glob syntax). Returns a List of Path. No recursive **, no cwd to resolve a relative pattern against."},
	{"ls", `ls(pattern)`,
		"glob's metadata-bearing sibling: same matching, but returns a Table (Record fields: path, name, size, is_dir, mode, mtime, atime, uid, gid) instead of bare Paths."},
	{"stat", "stat(path)",
		"One Path's real metadata (the same fields ls returns) as a single Record."},
	{"format_time", `format_time(layout, epoch_seconds)`,
		`Renders stat/ls's raw mtime/atime (Unix epoch seconds) as a String using layout, Go's reference-time format ("2006-01-02 15:04:05").`},
	{"humanize_time", `humanize_time(epoch_seconds)`,
		`Renders stat/ls's raw mtime/atime as a short relative phrase ("5 minutes ago", "in 3 hours") against the current time.`},
	{"checkout", "checkout(path, closure)",
		"Materializes a namespace subtree to a real scratch directory, runs closure with that real Path, writes back any changes when it returns. For tools (vim, a compiler) that need a real seekable path, not just bytes."},
	{"find", "find(dir, pattern)",
		"glob's recursive sibling: walks every subdirectory beneath dir, matching pattern against each entry's base name at every depth. Returns a List of Path, both files and directories eligible."},
	{"cat", "cat(path)",
		"Reads one namespace file's whole content back as a String, pipeable into split/trim/contains/etc. No checkout() round trip needed for namespace-only files (/env, /jobs)."},
	{"cp", "cp(src, dst)",
		"Copies src's content to dst — both ordinary namespace Paths, so a real OS file, a remote /n/host mount, or anything else bound in can be either side. src must be a regular file; dst may be an existing file or a new one at an existing directory level."},
	{"rm", "rm(path)",
		"Removes one namespace file — works anywhere cp's dst does (a real OS file, a remote /n/host mount, ...). path must be a regular file; directory removal isn't supported yet."},
	{"mv", "mv(src, dst)",
		"Moves/renames src to dst. A real in-place rename (no content copied) when src and dst share the same parent directory; otherwise a copy-then-remove. src must be a regular file."},
	{"dial", `dial(addr)`,
		"Connects to a remote 9sh/9P peer, or a local Unix-socket 9P server, and returns an unbound MountHandle — bind grafts it in. Mutual TLS + 9auth identity for host:port; same-UID trust only for a socket path."},
	{"dir", `dir(path)`,
		"dial's local sibling: wraps an arbitrary absolute host directory into a bindable MountHandle. Requires an absolute path."},
	{"path", `path(str)`,
		"Converts a String to a Path — the explicit way to use dynamically-built path text (format(...), split/join, ...) with bind/checkout/stat/join_path, which all require an actual Path. Requires an absolute string."},
	{"join_path", "join_path(base, ...segments)",
		"Builds a Path from an already-fully-qualified base Path plus string segments — cuts repetition without introducing a namespace cwd (kyu deliberately has none)."},

	// Variables & environment
	{"vars", "vars()",
		"Lists your own := -defined kyu variables — name, kind, value — as a Table, filtered clear of builtins."},
	{"unset", `unset(name)`,
		"Removes a kyu variable binding by String name. Reports whether one existed; refuses to remove a builtin."},
	{"getenv", `getenv(name)`,
		"Reads a real file under /env — Plan 9's own convention: environment variables are namespace files, not hidden shell state."},
	{"setenv", `setenv(name, value)`,
		`Writes /env/<name>. setenv("PATH", ...) genuinely changes which binary %cmd resolves.`},
	{"unsetenv", `unsetenv(name)`,
		"Removes /env/<name>. A no-op on an already-absent name, not an error."},

	// Process / subprocess
	{"cd", "cd(path)",
		"Sets the working directory %cmd subprocesses run in — per-session state, not a real chdir, since every entry point into a session shares one process."},
	{"pwd", "pwd()",
		"Reads cd's working directory back in-process, falling back to the real os.Getwd() before cd() has ever been called."},
	{"exit_code", "exit_code()",
		`The last foreground %cmd's real exit status — bash's $?, spelled as a function since kyu has no $-prefixed syntax.`},
	{"host", "host()",
		`This machine's real hostname — e.g. for an if host() == "laptop" { ... } conditional inside common.ky/hosts/<hostname>.ky.`},
	{"wait", "job | wait",
		"Blocks until a backgrounded job (from %cmd &) reaches a terminal state, then returns it."},
	{"ps", "ps()",
		"Every job at /jobs as a Table of Records (id, kind, state, argv, pid, exit_code, signal, error, detached, cwd, started_at, finished_at) — the structured, no-checkout-needed view of /jobs' own status files."},
	{"error", `error(msg)`,
		"Builds an ErrorVal directly — falsy, flows through a pipeline as an ordinary value; a trailing ? promotes it to a hard abort."},

	// Data pipeline
	{"where", `... | where { |row| cond }`, "Filters a Table/List by a boolean expression evaluated per row. where/select/etc. are plain functions, not keywords — the '{ |row| ... }' form is pipe-position sugar for a single-argument call; a named closure needs explicit parens, e.g. where(pred)."},
	{"select", `... | select("field1", "field2", ...)`, "Projects a Table down to just the named fields (String field-name arguments, not barewords)."},
	{"sort_by", `... | sort_by("field")`, "Sorts a Table/List by a field name (String), or by a { |row| expr } closure."},
	{"group_by", `... | group_by("field")`, "Groups a Table by a field name (String), or by a { |row| expr } closure, into a Table of {key, items} records."},
	{"each", `... | each { |row| expr }`, "Maps closure over every element, returning a new List/Table."},
	{"take", `... | take(n)`, "The first n elements."},
	{"first", "... | first", "The first element, or null if empty."},
	{"count", "... | count", "The number of elements."},
	{"last", `... | last  or  ... | last(n)`, "With no argument, the last element (or null if empty); with a count, a List of the last n elements."},
	{"skip", `... | skip(n)`, "Every element after the first n."},
	{"reverse", "... | reverse", "Elements in reverse order."},
	{"uniq", "... | uniq", "Duplicate elements removed, order preserved."},
	{"flatten", "... | flatten", "One level of nested Lists flattened into their parent."},
	{"sum", "... | sum", "The sum of a numeric List/field."},
	{"min", "... | min", "The smallest element."},
	{"max", "... | max", "The largest element."},
	{"avg", "... | avg", "The mean of a numeric List/field."},
	{"any", `... | any { |row| cond }`, "True if any element matches cond."},
	{"all", `... | all { |row| cond }`, "True if every element matches cond."},
	{"to_json", "... | to_json", "Renders a value as a JSON String."},
	{"from_json", `from_json(str)`, "Parses a JSON String into kyu values."},

	// Strings
	{"split", `split(sep, str)`, "Splits a String on sep into a List of String."},
	{"trim", "trim(str)", "Leading/trailing whitespace removed."},
	{"replace", `replace(old, new, str)`, "All occurrences of old replaced with new."},
	{"contains", `contains(sub, str)  or  contains(elem, list)`, "True if str contains substring sub, or if list has an element equal to elem."},
	{"join", `list | join(sep)`, "Joins a List's elements into one String with sep between them. (Not join_path — that builds a Path from a base plus segments.)"},
	{"format", `format(tmpl, ...args)`, `Positional "{}" interpolation — the placeholder count must exactly match the argument count.`},
	{"len", "len(str_or_list)", "Rune count for a String, element count for a List/Table — count's sibling for strings."},
	{"repeat", `repeat(n, str)`, "str tiled n times, e.g. repeat(5, \"-\") for a separator line."},
	{"pad_left", `pad_left(width, str)  or  pad_left(width, fill, str)`, `Right-aligns str within width, padding on the left (space by default, or fill if given). No-op if str is already >= width.`},
	{"pad_right", `pad_right(width, str)  or  pad_right(width, fill, str)`, `Left-aligns str within width, padding on the right. No-op if str is already >= width.`},
	{"upper", "upper(str)", "Uppercased."},
	{"lower", "lower(str)", "Lowercased."},
	{"starts_with", `starts_with(prefix, str)`, "True if str starts with prefix — an anchored check, unlike contains."},
	{"ends_with", `ends_with(suffix, str)`, "True if str ends with suffix."},
	{"index_of", `index_of(needle, str_or_list)`, "The first position of needle: a rune index for a String, an element index for a List/Table (needle compared by value equality). -1 if not found."},
	{"to_int", "to_int(str_or_number)", `Parses a String into an Int (base 10); truncates a Float; an Int passes through unchanged. An unparseable String is an ErrorVal, not a hard error — a script's own args are always String, so this is how you do arithmetic on one.`},
	{"to_float", "to_float(str_or_number)", "Parses a String into a Float; an Int/Float pass through (widening/unchanged). An unparseable String is an ErrorVal, same as to_int."},
	{"round", `round(places, number)`, `Rounds to places decimal digits, half-away-from-zero; always returns a Float. For turning a number into a String with that precision, pipe into format: number | round(2) | format("{}").`},

	// Control flow / syntax
	{"while", "while cond { ... }", "kyu's only loop construct, with break/continue. A self-referencing closure also works for recursion."},
	{"if", "if cond { ... } [else { ... }]", "A block's last expression is its value — what prints at the REPL."},
	{"%cmd", "%cmd arg1 arg2 ...", "Calls an ordinary external/legacy binary. Routes through /jobs when a namespace is attached, so it shows up in session history like any job. A Path argument that only resolves in the namespace, not on the real filesystem, errors with a hint to use checkout instead of reaching the binary as a meaningless literal string. If the command's name is listed in the fullscreen_programs kyu variable (see /config/config.ky), it instead gets the real screen and keyboard directly — no job, no capture — with any namespace-only Path argument transparently checked out and written back instead of erroring; can't be backgrounded with &."},
	{"&", "%cmd ... &", `Backgrounds a %cmd as a live job record: j.status, j.ctl = "stop", j | wait. Refused for a fullscreen program (see %cmd) -- nothing to hand the real screen to if it isn't in the foreground.`},
	{"@host", "@host { ... }", "Re-roots job creation at a dial()'d remote peer's own /jobs for the block — 'proxy jobs,' no separate remote-job protocol."},
}

// docByName looks up one entry by exact name, for biHelp.
func docByName(name string) (BuiltinDoc, bool) {
	for _, d := range builtinDocs {
		if d.Name == name {
			return d, true
		}
	}
	return BuiltinDoc{}, false
}

// docRecord renders one BuiltinDoc as a Record, the same Table-of-
// Record shape stat/ls/vars already use for structured results —
// pipeable (`help() | where { |d| d.name == "bind" }`), inspectable field by
// field, rather than a preformatted block of text.
func docRecord(d BuiltinDoc) *value.Record {
	r := value.NewRecord()
	r.Set("name", value.String(d.Name))
	r.Set("signature", value.String(d.Signature))
	r.Set("description", value.String(d.Description))
	return r
}

// biHelp implements `help()`/`help(name)`: with no arguments, every
// documented name as a Table (Record: name, signature, description);
// with one String name, that single entry's Record. The same table
// replui/help.go's expanded F1 screen language-reference section
// renders (see Docs), so the two can't drift apart.
//
// An unknown name is an ordinary in-stream ErrorVal, not a hard Go
// error — matching dial/dir/stat's convention for "the thing you
// asked for doesn't exist," an expected, non-fatal outcome.
func biHelp(args []value.Value) (value.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("help: expected 0 or 1 arguments (a name), got %d", len(args))
	}
	if len(args) == 0 {
		elems := make([]value.Value, len(builtinDocs))
		for i, d := range builtinDocs {
			elems[i] = docRecord(d)
		}
		return value.NewList(elems), nil
	}
	name, ok := args[0].(value.String)
	if !ok {
		return nil, fmt.Errorf("help: expected a string name, got %s", args[0].Kind())
	}
	d, found := docByName(string(name))
	if !found {
		return value.ErrorVal{Msg: fmt.Sprintf("help: no entry for %q", string(name))}, nil
	}
	return docRecord(d), nil
}
