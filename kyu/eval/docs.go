package eval

import (
	"fmt"

	"github.com/sandgorgon/9sh/kyu/value"
)

// BuiltinDoc is one language-reference entry — the single source of
// truth for both help(name) (biHelp, this file) and package replui's
// expanded help screen's language-reference section (see
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
	{"bind", "bind SRC, DST[, before|after|replace][, ro]",
		"Grafts SRC onto the namespace at DST — a Path, a namespace-union expression (a + b), or a MountHandle from dial()/dir(). Optional trailing words: a disposition (before/after/replace) and/or ro, which makes writes through this bind fail (the same tree bound elsewhere stays writable). A statement, not a function; comma-separated, not space-separated."},
	{"unbind", "unbind DST",
		"Clears whatever's bound at DST. Unbinding somewhere nothing is bound is an error, unlike unsetenv's forgiving convention."},
	{"glob", `glob(pattern)`,
		"Matches namespace entries in one directory against pattern's final segment (*, ?, [...] glob syntax). Returns a List of Path. No recursive **, no cwd to resolve a relative pattern against."},
	{"ls", `ls(pattern)`,
		"glob's metadata-bearing sibling: same matching, but returns a Table (Record fields: path, name, size, is_dir, mode, mtime, atime, uid, gid, dev) instead of bare Paths."},
	{"stat", "stat(path)",
		"One Path's real metadata (the same fields ls returns) as a single Record. dev is the id of the bound layer serving the file (0 for a synthetic directory) — match it against binds() or which_bind()."},
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
		"Copies src's content to dst — both ordinary namespace Paths, so a real OS file, a remote /n/host mount, or anything else bound in can be either side. If dst is an existing directory the copy goes into it, as with Unix cp: cp(/f, /d) writes /d/f. Otherwise, for a regular file, dst may be an existing file (overwritten) or a new name at an existing directory level. A directory src is copied as a full recursive tree to a fresh destination — one that already has that name is refused (no merge), and a directory can't be copied into itself."},
	{"rm", "rm(path)",
		"Removes one namespace file — works anywhere cp's dst does (a real OS file, a remote /n/host mount, ...). path must be a regular file; use rmdir for a directory."},
	{"mv", "mv(src, dst)",
		"Moves/renames src to dst. A real in-place rename (no content copied) when src and dst share the same parent directory — works for a directory src too, since it's a metadata-only rename. Otherwise falls back to copy-then-remove (a recursive tree copy for a directory src). If dst is an existing directory the move goes into it, as with Unix mv: mv(/a/f, /d) moves it to /d/f. Same restrictions as cp for a directory: the destination name must be fresh, and a directory can't be moved into itself; moving a file onto itself (mv(/d/f, /d)) is refused rather than deleting it."},
	{"mkdir", "mkdir(path)",
		"Creates path, creating any missing intermediate directories along the way (mkdir -p semantics). A no-op if path already exists as a directory; an ErrorVal if any component exists as something else."},
	{"rmdir", "rmdir(path)  or  rmdir(path, recursive)",
		"Removes one namespace directory. With no second argument, only an empty directory (errors, from the real filesystem, if it isn't). Pass true as a second argument to remove a non-empty directory and everything beneath it."},
	{"history", "history()",
		"The current session's REPL recall history (Up/Down, Ctrl-R) as a Table (index, text) — index is what history_delete(index) takes. TUI-only: the plain -repl/script entry points have no recall history to list."},
	{"history_delete", "history_delete(index)",
		"Removes one history() entry by index. Reports whether one existed there (unset()'s own convention), not an ErrorVal."},
	{"history_clear", "history_clear()",
		"Removes every history entry. Ctrl+L's transcript-clear deliberately leaves history untouched — this is the explicit way to clear it too."},
	{"history_mode", `history_mode := "all" | "unique"`,
		`Controls how a submitted line is added to REPL recall history. "all" (the default) keeps every submission, duplicates included. "unique" removes any earlier occurrence of an identical line before appending it, so history never holds two copies of the same command and Ctrl-R search stays decluttered.`},
	{"source_config", "source_config()",
		"Re-runs config.ky, then common.ky/hosts/<hostname>.ky, against this session — the same startup sequence cmd/9sh runs once at launch, callable again at runtime. Additive: a plain bind refreshes its destination (default disposition is replace), a := redefinition overwrites, and only an explicit before/after union bind actually stacks another layer."},
	{"reset_config", "reset_config()",
		"source_config's fresh-start sibling: first clears every := -defined variable and unbinds every namespace entry outside jobs/local/env/config/session/ns, then does exactly what source_config does — so a removed bind or variable actually goes away instead of lingering from a previous source. Limitation: those six roots are never touched, so a layer a dotfile bound onto one of them (before/after, or a replacing bind) survives a reset — bind such layers at a path outside the six to keep them resettable, or restart 9sh."},
	{"dial", `dial(addr)`,
		"Connects to a remote 9sh/9P peer, or a local Unix-socket 9P server, and returns an unbound MountHandle — bind grafts it in. Mutual TLS + 9auth identity for host:port; same-UID trust only for a socket path."},
	{"dir", `dir(path)`,
		"dial's local sibling: wraps an arbitrary absolute host directory into a bindable MountHandle. Requires an absolute path."},
	{"path", `path(str)`,
		"Converts a String to a Path — the explicit way to use dynamically-built path text (format(...), split/join, ...) with bind/checkout/stat/join_path, which all require an actual Path. Requires an absolute string."},
	{"join_path", "join_path(base, ...segments)",
		"Builds a Path from an already-fully-qualified base Path plus string segments — cuts repetition without introducing a namespace cwd (kyu deliberately has none). `base + \"segment\"` does the same for one segment (`/n + name`); the Path goes on the left, and a Path on the right means namespace union instead."},

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
		"Blocks until a backgrounded job (from &, subprocess or in-process) reaches a terminal state, then returns it."},
	{"attach", "attach(job)",
		"Takes over the local terminal and streams raw bytes directly to/from a &pty job's own pty — Ctrl-D/Ctrl-C/Ctrl-Z reach it like a real terminal, Ctrl-] detaches (typed twice sends a literal Ctrl-]). Works on a remote @host{} job too. Errors if job has no pty, or inside the interactive TUI (not supported yet)."},
	{"ps", "ps()",
		"Every job at /jobs as a Table of Records (id, kind, state, argv, pid, exit_code, signal, error, detached, cwd, started_at, finished_at) — the structured, no-checkout-needed view of /jobs' own status files."},
	{"bind_log", "bind_log()",
		"Every successful bind and unbind so far, oldest first, as a Table of Records (seq, time, op, dst, src, disp) — what was actually typed, including the before/after that binds() can't recover and every unbind. The text form, /ns/log, is replayable kyu. Capped at the newest 1000 entries."},
	{"write", "write(path, str)",
		"Replaces a namespace file's content with a String, creating it (in an existing directory) if needed — cat's write-side counterpart. Any failure is an ErrorVal."},
	{"append", "append(path, str)",
		"Adds a String to the end of a namespace file, creating it if needed. Needs a file server that reports a real length (every real directory does)."},
	{"which_bind", "which_bind(path)",
		"Which bind point and union layer serves a path, as a Record (path, kind, dst, src, layer, layers, dev, inner) — the layer behind one path (ls's dev field gives it per entry). kind is layer, bindpoint or tree; an unresolvable path is an ErrorVal. Local namespace only."},
	{"source", "source(path)",
		"Runs a kyu file from the namespace against the session, as if typed at the prompt — e.g. source(/ns/binds) replays this namespace's binds. A parse error is an ErrorVal and runs nothing; a runtime error aborts. Runs arbitrary kyu with your full authority, so cat() a peer's file before sourcing it."},
	{"binds", "binds()  or  binds(path)",
		"Every layer bound in the namespace (or, with a path, at or beneath it) as a Table of Records (dst, src, disp, ro, dev), in bind order — the structured view of /ns/binds, whose text is replayable kyu. src is null for a bootstrap bind with no kyu spelling; disp is the canonical replay disposition (replace for a path's first layer, after for the rest), not the one originally given. dev is the id ls/stat stamp on that layer's files (0 for a layer that binds an existing path: its files keep their source's id)."},
	{"error", `error(msg)`,
		"Builds an ErrorVal directly — falsy, flows through a pipeline as an ordinary value; a trailing ? promotes it to a hard abort."},

	// Data pipeline
	{"where", `... | where { |row| cond }`, "Filters a Table/List by a boolean expression evaluated per row. where/select/etc. are plain functions, not keywords — the '{ |row| ... }' form is pipe-position sugar for a single-argument call; a named closure needs explicit parens, e.g. where(pred)."},
	{"select", `... | select("field1", "field2", ...)`, "Projects a Table down to just the named fields (String field-name arguments, not barewords)."},
	{"get_field", `record | get_field("name")  or  get_field("name", record)`, "The dynamic counterpart to record.field: reads one field by a runtime String name instead of a literal identifier. Same semantics as record.field otherwise (a missing field is a hard error, a live-backed field is read fresh). Composes with each for a list of property names: [\"a\", \"b\"] | each { |p| get_field(p, record) }."},
	{"range", `range(stop)  or  range(start, stop [, step])`, "A List of Ints, half-open like slicing: range(3) is [0, 1, 2]. A negative step counts down; a zero step or more than a million elements is an error."},
	{"zip", `xs | zip(ys)`, "A List of two-element Lists pairing elements by position, stopping at the shorter list."},
	{"keys", `record | keys`, "A record's field names, in order, as a List of Strings. Never reads a field's value, so it is safe on a live job record."},
	{"values", `record | values`, "A record's field values, in key order. A live-backed field is read fresh (a job's wait blocks; its ctl fails) — pick fields with keys and get_field instead."},
	{"match", `s | match(pattern)`, "Whether the regular expression matches anywhere in s (anchor with ^ and $ for the whole string). Go RE2 syntax: linear time, no backreferences or lookaround. A bad pattern is an error."},
	{"capture", `s | capture(pattern)`, "The first regex match as a List of Strings — the whole match, then each group — or null if nothing matches. A group that didn't take part is null in its slot."},
	{"replace_re", `s | replace_re(pattern, repl)`, "Replaces every regex match, expanding $1 / ${name} in repl to that group. replace() is the literal, no-regex form."},
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
	{"%cmd", "%cmd arg1 arg2 ...  %(expr) arg1 arg2 ...", "Calls an ordinary external/legacy binary. Routes through /jobs when a namespace is attached, so it shows up in session history like any job. A Path argument that only resolves in the namespace, not on the real filesystem, errors with a hint to use checkout instead of reaching the binary as a meaningless literal string. If the command's name is listed in the fullscreen_programs kyu variable (see /config/config.ky), it instead gets the real screen and keyboard directly — no job, no capture — with any namespace-only Path argument transparently checked out and written back instead of erroring; can't be backgrounded with &. The command name can be computed too: %(expr) evaluates expr to a String at call time first, then behaves exactly like a literal %name from there — the %-sigil counterpart to @(expr) for a job's mount point. A bareword native_programs call (no % sigil, e.g. 9ed) has no computed-name form."},
	{"native_programs", "cmdname arg1 arg2 ...  (no % needed)", `A third call form, for external programs that are themselves namespace-aware (e.g. 9ed, 9vcs) rather than legacy Bytes-only binaries: any name listed in the native_programs kyu variable (see /config/config.ky) can be called bareword, no % sigil, same argument shape %cmd takes. A Path argument is passed through as its literal path text, untouched — no checkout, no scratch copy — trusting the program to resolve it itself (dial 9sh's namespace socket, Walk an absolute path or one rooted at /local, fall back to a real OS path only if the namespace doesn't claim it). Extend it like fullscreen_programs: native_programs := native_programs + ["mytool"].`},
	{"&", "expr &", `Backgrounds expr as a live job record: j.status, j.ctl = "kill", j | wait. A %cmd becomes a real subprocess job; any other expression (a bare { ... } block is auto-invoked with zero arguments) becomes an in-process one instead -- status.kind says which. An in-process job's result comes back as bytes on its stdout field, the same as a %cmd's captured output; killing one is cooperative (stops at its next loop iteration or function call, not instantly) since there's no real process to signal, and unbounded recursion is separately depth-bounded so a missing base case fails cleanly rather than crashing the session. In-process jobs are local only -- backgrounding non-%cmd code inside @host{ ... } is an error. A fullscreen %cmd (see %cmd) refuses & outright -- nothing to hand the real screen to if it isn't in the foreground.`},
	{"in_ns", "in_ns { ... }", "Runs the block against a private copy of the namespace — binds and unbinds inside it don't leak out, and everything already bound is visible inside (Plan 9's rfork). The original is restored however the block ends. A bind is a view, so writes through a shared directory still reach it; a background job started inside keeps the job it was given. The block's last value is its value."},
	{"@host", "@host { ... }  @/path { ... }  @(expr) { ... }", "Re-roots job creation at a bound peer's own /jobs for the block — 'proxy jobs,' no separate remote-job protocol. The operand is a mount point, a Path like bind's destination: `@host` is shorthand for `@/n/host` (always a literal name, never a variable), `@/path` names any mount, and `@(expr)` takes any expression that yields a Path, so it can be computed: `@(/n + name) { %uptime }`, or over a list `hosts | each { |h| @(/n + h) { %uptime } }`."},
}

// namespaceAppNames is every BuiltinDoc name that's a namespace-aware
// "app" (an in-process Go function that touches env.Namespace()) rather
// than a pure language builtin (data pipeline, strings, control flow,
// process-state accessors like cd/pwd/host that read/write Env fields
// but never the namespace itself) — the first two of the three tiers
// README's Design section describes; the third (native external
// programs, config-driven via native_programs, see fullscreen.go's
// sibling isNativeProgram) isn't a BuiltinDoc at all, since membership
// is runtime config, not a compiled-in name — see the dedicated %cmd-
// adjacent doc entry above for that tier instead.
//
// Kept as an explicit set here, checked by name in Category() below,
// rather than a new BuiltinDoc struct field: BuiltinDoc's literals above
// are all positional (Go requires every field when a struct literal is
// positional), so adding a field would mean touching all ~60 existing
// entries just to tag them — this reaches the same three-way split
// without that diff.
var namespaceAppNames = map[string]bool{
	"bind": true, "unbind": true, "glob": true, "ls": true, "stat": true,
	"checkout": true, "find": true, "cat": true, "write": true, "append": true, "cp": true, "rm": true, "mv": true,
	"mkdir": true, "rmdir": true, "source_config": true, "reset_config": true,
	"history": true, "history_delete": true, "history_clear": true,
	"dial": true, "dir": true, "getenv": true, "setenv": true, "unsetenv": true,
	"vars": true, "unset": true, "ps": true, "binds": true, "bind_log": true, "which_bind": true, "source": true, "wait": true,
	"%cmd": true, "&": true, "@host": true, "in_ns": true, "attach": true,
}

// Category classifies d as "language" (no namespace/OS involvement) or
// "namespace-app" (touches env.Namespace()) — see namespaceAppNames'
// doc comment for the full three-tier picture and why this is a lookup
// rather than a struct field.
func (d BuiltinDoc) Category() string {
	if namespaceAppNames[d.Name] {
		return "namespace-app"
	}
	return "language"
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
	r.Set("category", value.String(d.Category()))
	return r
}

// biHelp implements `help()`/`help(name)`: with no arguments, every
// documented name as a Table (Record: name, signature, description);
// with one String name, that single entry's Record. The same table
// replui/help.go's expanded help screen language-reference section
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
