# 9sh

[![CI](https://github.com/sandgorgon/9sh/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/sandgorgon/9sh/actions/workflows/ci.yml?query=branch%3Amaster)

A Plan-9-flavored Linux login shell: per-process namespaces, structured
pipes, and a small scripting language called **kyu**, built on top of
[`9p`](https://github.com/sandgorgon/9p), [`9vcs`](https://github.com/sandgorgon/9vcs),
[`9auth`](https://github.com/sandgorgon/9auth), and
[`tui`](https://github.com/sandgorgon/tui). It's meant to be the actual
shell you work in day to day.

The goal is genuine innovation, not another bash/zsh/fish remix: lean
into Plan 9's ideas — everything is reachable through one composable
namespace, that namespace is per-process and built purely by `bind` (no
FUSE, no OS mount table), and network transparency comes from 9P itself
rather than a bolted-on remoting layer.

## Requirements

- Go 1.26 or later.
- Optionally, [`9vcs`](https://github.com/sandgorgon/9vcs) on `PATH` for
  session history — 9sh runs fine without it, just without that one
  feature.

## Install

Grab a prebuilt binary from the
[Releases page](https://github.com/sandgorgon/9sh/releases) — each
release has `.tar.gz`s for `linux/amd64`, `linux/arm64`,
`darwin/arm64`, and `darwin/amd64`, plus a `.sha256` to verify against:

```
curl -LO https://github.com/sandgorgon/9sh/releases/download/<tag>/9sh_<tag>_linux_amd64.tar.gz
tar xzf 9sh_<tag>_linux_amd64.tar.gz
```

That unpacks a directory with the `9sh` binary, `LICENSE`, and this
`README.md`. Put the binary somewhere on your `PATH`.

Or build from source:

```
go build -o 9sh ./cmd/9sh
```

Run it with no arguments in a real terminal to get 9sh's own single-
screen interactive TUI (the default, primary way to use it — live
syntax highlighting, Ctrl-R history search, Tab completion); pipe
something into stdin or pass `-repl` for a plain line-based REPL; pass
a script path to run headlessly. For a real multi-pane terminal (9sh
alongside a shell, or several 9sh sessions side by side), see
[`9mux`](https://github.com/sandgorgon/9mux) — a separate project, not
something this binary hosts itself (see "Design" below for why).

## Quick start

```
9sh> bind /local, /work
9sh> ls("/work/*.go")
9sh> j := %sleep "5" &
9sh> j.status.state
"running"
9sh> j | wait
```

Prefer to learn by running real programs instead of reading prose? See
[`examples/`](examples/) — eight small, verified `.ky` scripts, one
feature area each (namespace basics, jobs, data pipelines, strings,
control flow, env/kyu vars, remote namespaces, file ops).

- `bind SRC, DST[, before|after|replace]` grafts something onto the
  namespace — a local directory, a job-control tree, a dialed remote
  peer's whole namespace, all the same mechanism.
- `%cmd` calls out to an ordinary Linux binary; a `%cmd ... &` job is a
  live record — `j.status`, `j.ctl = "stop"`, `j | wait` all read/write
  through to real namespace files, not a snapshot.
- `|` is a structured pipe by default (`where`/`select`/`sort_by`/
  `group_by`/`each`/...), not raw bytes — `%` is the sigil that marks
  "this call is bytes, not structured data."
- A `%cmd` whose name is listed in `fullscreen_programs` (a kyu
  variable, defaulted in `/config/config.ky` — `vim`, `top`, `ssh`,
  `man`, ... out of the box) gets the real screen and keyboard
  directly instead of a job-tracked buffer, in every mode including the
  interactive TUI — it hands the whole screen to the program until it
  exits, then returns to the kyu prompt. A namespace-only `Path`
  argument is transparently checked out to a real scratch location and
  written back on exit, instead of erroring the way an ordinary `%cmd`
  would. No job, no captured value, and it can't be backgrounded with
  `&` (nothing to hand the real screen to if it isn't in the
  foreground) — edit `/config/config.ky` (or extend
  `fullscreen_programs` from `common.ky`) to add your own.
- `while cond { ... }` loops, with `break`/`continue` — kyu's only loop
  construct; recursion via a self-referencing closure still works too.
- `cd(path)` sets the working directory `%cmd` subprocesses run
  in — per-session state (like `bind`), not a real `chdir`, since every
  entry point into a session shares one process. `pwd()` reads it back
  in-process (no `%pwd` subprocess needed), falling back to the real
  `os.Getwd()` before the first `cd()`.
- `getenv(name)`/`setenv(name, value)`/`unsetenv(name)` read and write
  real files under `/env` — Plan 9's own convention (environment
  variables *are* namespace files), not hidden shell state.
  `ls("/env/*")` to see what's there (or `glob("/env/*")` for just the
  paths) — a plain `%ls /env` won't work: `%cmd` hands a `Path`
  argument to the real external binary as a literal string, with no
  namespace resolution (`/env` has no real OS path at all). Since
  `/env` isn't bound from anywhere real, `%ls /env` fails with a clear
  error pointing at `checkout` rather than running at all; a namespace
  path that happens to coincide with an unrelated real file is the one
  case this can't catch (see the Design section's "No FUSE").
  `setenv("PATH", ...)` genuinely changes which binary `%cmd`
  resolves, not just what a subprocess sees about its own environment.
- Data-pipeline builtins beyond `where`/`select`/`sort_by`/`group_by`/
  `each`: `last`/`skip`/`reverse`/`uniq`/`flatten`, `sum`/`min`/`max`/
  `avg`, `any`/`all`, `to_json`/`from_json`, and string ops `split`/
  `trim`/`replace`/`contains`/`join`/`len`/`repeat`/`pad_left`/
  `pad_right`/`upper`/`lower`/`starts_with`/`ends_with`/`index_of` — the
  `pad_*`/`repeat`/`len` group is for building an exact line of output
  (a fixed-width column, a separator rule) rather than free-text
  templating, which is what `format` is for. `contains`/`index_of` work
  on a `List`/`Table` too (element equality), not just a substring
  check. `to_int(str)`/`to_float(str)` parse a String into a number —
  otherwise there'd be no way to do arithmetic on a script's own `args`,
  which are always `String`; an unparseable input is an `ErrorVal`, not
  a hard error. `round(places, number)` rounds to a fixed number of
  decimal digits (half-away-from-zero, always a `Float`) — pipe into
  `format` for a String with guaranteed decimal precision, e.g.
  `number | round(2) | format("{}")`.
- `vars()` lists your own `:=`-defined kyu variables — name, kind, and
  live value, as a `Table` (pipeable: `vars() | where { |v| v.kind == "path" }`).
  Unlike `/env`, kyu variables are plain lexical scope, not namespace
  state, so there's no `glob()`-able equivalent — `vars()` is the only
  way to see them, and it filters out builtins (they're `env.Define`d the
  same way, with no separate registry) so it only ever shows what you
  actually set. `unset(name)` is its companion, kyu variables' answer to
  `unsetenv`/`unbind`: removes a binding (reporting whether one existed,
  not erroring on a no-op — closer to `unsetenv`'s forgiving convention
  than `unbind`'s strict one) and refuses outright to remove a builtin.
- `glob(pattern)` — e.g. `glob("/local/*.go")` — matches namespace
  entries, not real OS paths (most of the namespace, like `/jobs` or a
  remote `/n/host` mount, has no OS path at all). Returns a `List` of
  `Path`, pipeable like anything else: `glob("/local/*.go") | count`.
  Single directory only, no recursive `**`, and always an explicit
  path — kyu has no notion of a "current namespace directory" (`cd`'s
  cwd is a separate, OS-path-only concept, just for subprocesses).
- `stat(path)`/`ls(pattern)` are `glob`'s metadata-bearing siblings:
  `stat` returns one `Path`'s real properties as a `Record` (`path`,
  `name`, `size`, `is_dir`, `mode` as an `"rwxr-xr-x"` string, `mtime`/
  `atime` as Unix-epoch `Int`, `uid`, `gid`); `ls(pattern)` is `glob`'s
  own directory-and-pattern matching, but returns a `Table` (`List` of
  those `Record`s) instead of bare `Path`s — the real `ls -la`
  experience, native to the namespace. `glob` itself is left alone for
  when you just want plain `Path`s to pipe into `bind`/`checkout`/etc.
  `stat`/`ls`'s `mtime`/`atime` are raw Unix-epoch `Int`s — pipe them
  into `format_time(layout, epoch_seconds)` for a fixed rendering (Go's
  reference-time layout string) or `humanize_time(epoch_seconds)` for a
  short relative one (`"5 minutes ago"`, `"in 3 hours"`).
- `find(dir, pattern)` is `glob`'s recursive sibling: walks every
  subdirectory beneath `dir`, matching `pattern` against each entry's
  base name at every depth (both files and directories are eligible,
  and a matching directory is still recursed into). Returns a `List` of
  `Path`, same as `glob`. A separate two-argument builtin rather than a
  `**` convention bolted onto `glob`'s own pattern string.
- `cat(path)` reads one namespace file's whole content back as a
  `String`, pipeable straight into `split`/`trim`/`contains`/etc — no
  `checkout()` round trip needed just to see what's in a namespace-only
  file (an `/env` var, a job's `status`/`ctl` file). `cp(src, dst)`
  copies one `Path`'s content to another, both ordinary namespace
  paths — a real OS file, a remote `/n/host` mount, or anything else
  bound in can be either side, with no separate transfer protocol,
  since they're already the same namespace once bound. `src` must be a
  regular file; `dst` may be an existing file (overwritten) or a new
  one at an already-existing directory level — like `checkout`'s own
  write-back, neither builtin creates a new namespace subdirectory.
  `rm(path)` removes one namespace file, anywhere `cp`'s `dst` can
  reach. `mv(src, dst)` moves/renames — a real in-place rename (no
  content copied) when `src`/`dst` share a parent directory, a
  copy-then-remove otherwise. Both regular-file only, no directories
  yet — same v1 scope as `cp`.
- `unbind DST` clears whatever's bound at `DST` — the inverse of
  `bind`, same statement-not-function shape (a namespace-mutating verb
  stays a keyword). Unbinding something never bound is an error.
- `help(name)` — e.g. `help("bind")` — returns that builtin/keyword's
  signature and description as a `Record`; `help()` with no arguments
  returns every documented entry as a `Table`. The interactive TUI's
  `F1` screen renders the exact same table as its language-reference
  section (press `2` there to jump straight to it), so the two can't
  drift apart.
- Closures take default parameters: `{ |a, b = 10| a + b }` — a later
  default may reference an earlier parameter (`{ |a, b = a| ... }`).
  Named, self-recursive, and mutually-recursive functions already work
  today via plain `name := { ... }` (a name is resolved when the
  closure is *called*, not frozen at creation), so there's no separate
  `func` keyword — default params were the one genuine capability gap.
- `format("hello {}, you're {}", name, age)` — positional `{}`
  interpolation, not new string-literal syntax; the placeholder count
  must exactly match the argument count. Pipeable like anything else:
  `name | format("hello {}")`.
- `exit_code()` — bash's `$?`, spelled as a function since kyu has no
  `$`-prefixed syntax. Tracks only the last *foreground* `%cmd` — a
  backgrounded `%cmd &`'s exit code is already on its own job record
  (`j.status.exit_code`, `j | wait`).
- `ps()` returns every job at `/jobs` as a `Table` of `Record`s (`id`,
  `kind`, `state`, `argv`, `pid`, `exit_code`, `signal`, `error`,
  `detached`, `cwd`, `started_at`, `finished_at`) — the structured,
  no-checkout-needed view of `/jobs`' own `status` files, e.g.
  `ps() | where { |j| j.state == "running" }`.
- A script's own arguments are visible as `args` (a `List` of `String`)
  — `9sh script.kyu foo bar` sees `args == ["foo", "bar"]`.
- `%cmd1 && %cmd2` / `%cmd1 || %cmd2` chain by real exit status, like a
  shell — `&&`'s right side runs only if the left command exited 0;
  `||`'s only if it didn't. Only a *bare* `%cmd` operand gets this —
  `x := %cmd1; x && ...` sees `x` as an ordinary (always-truthy)
  `Bytes` value instead, since storing the result first is an explicit
  opt-out. The chain's own result is always a plain `Bool`, never
  whichever command's output — chaining is for control flow, not for
  seeing output; use `if exit_code() == 0 { %cmd2 }` when you want the
  latter, since a plain `if`'s block result is what actually gets
  printed at the REPL.

Run a job on another 9sh, over mutual TLS, with no separate remote-job
protocol:

```
9sh> h := dial("otherhost:2049")
9sh> bind h, /n/otherhost
9sh> @otherhost { %sh "-c" "echo hi" & } | wait
```

`@host { ... }` re-roots job creation at the bound remote's own `/jobs`
for the block — a "proxy job" is nothing more than that.

`dial` also reaches a local Unix-domain-socket 9P server — no TLS, no
identity, the socket's own file permissions are the trust boundary:

```
9sh> h := dial("/run/user/1000/9ed/12345.sock")
9sh> bind h, /n/9ed
```

And a 9sh can serve its own namespace the same way, for another local
9sh (or any 9P-aware app) to dial back into:

```
$ 9sh -listen-unix /run/user/1000/9sh/main.sock
```

restricted to connections from your own user (permission bits plus an
`SO_PEERCRED` check) — see [Local namespace access](#local-namespace-access)
below. Anything `9sh -listen-unix` spawns as a job can find its way back
in with zero configuration: the socket path is exported to it as
`$_9SH_UNIX_SOCK`.

That same socket is also what [`9mux`](https://github.com/sandgorgon/9mux)'s
native 9P-browsing pane points at for a live, in-memory-fast view of
this namespace from outside the process — a directory listing (for
`/local`, `/config`, `/session`, ...), or a job table (with a wait-
driven auto-refresh and a kill keybinding) when the target looks like
`/jobs`:

```
# ~/.config/9mux/config
jobs = browse unix:/run/user/1000/9sh/main.sock
```

Drop `common.ky` / `hosts/<hostname>.ky` under `~/.config/9/ns` and 9sh
runs them at startup, against the same environment, for persistent bind
rules/aliases/env defaults — see [Design](#design) and
[Coming from bash/zsh](#coming-from-bashzsh-there-is-no-current-directory)
below.

## Coming from bash/zsh: there is no current directory

The single easiest wrong assumption to carry over from a Unix shell is
that `/local` (or wherever you've `bind`ed things) is "where you are,"
the way `$PWD` is. It isn't, and the difference is structural, not
cosmetic:

- **The namespace has no cwd.** Every namespace path — a `bind` target,
  `glob(...)`, `checkout(...)`'s first argument, anything typed as a
  `Path` — is always a full path from the namespace root. There's no
  implicit context a partial path resolves against, so "what's my
  current directory in the namespace"
  isn't a question with an answer; it's a category error, like asking
  for the current directory of a URL.
- **`/local` is a bootstrap convenience, not a home.** At startup 9sh
  grafts the real OS directory it was launched from onto `/local`,
  purely so a brand-new session has *something* real and browsable —
  not because `/local` is a designated place you're meant to work from.
  It's one bind among any others you make, with no special status once
  you've made your own.
- **`cd`/`pwd` are real, but they're not about the namespace.** External
  Unix binaries (`%cmd`) genuinely need a process cwd to run in —
  that's an OS-level fact 9sh can't paper over — so `cd(path)`/`pwd()`
  give them one. It starts out equal to `/local`'s target (both come
  from the same `os.Getwd()` at launch), which is exactly what makes the
  two feel like the same concept. They aren't: rebinding `/local` never
  moves `cd`'s cwd, and `cd()` never touches `/local`'s bind. Treat `cd`
  as configuration you hand to subprocesses, not as "where you are."
- **Practical upshot:** always write full namespace paths. There's no
  relative-path shorthand to reach for, and syntactically a `Path`
  literal must start with `/` — `bind fdir, /local/f` doesn't parse as
  "bind the fdir next to me"; `fdir` there is a reference to an
  undefined variable. `join_path(base, ...segments)` takes the
  retyping-the-whole-thing edge off that without smuggling in a cwd:
  `work := /local/some/project` once, then `join_path(work, "sub")` as
  often as you like — `base` still has to already be a fully-qualified
  `Path`, so this only ever shortens repetition of a root you already
  spelled out, never resolves against implicit state. `path(str)`
  crosses the same `String`/`Path` boundary the other way: dynamically
  built path text (`format(...)`, `split`/`join`, ...) has no way to
  reach `bind`/`checkout`/`stat`/`join_path`'s base without it, since
  all of those hard-require an actual `Path`, never a `String` — same
  as `dial`/`dir` hard-require a `String`, never a `Path`, in the
  opposite direction. Both crossings are always an explicit function
  call, never an implicit guess based on what a value looks like.
- **Handing a namespace path straight to `%cmd` is caught, not
  silently wrong.** The natural first mistake this mental model
  produces — `%cat /local/foo`, treating `/local` like a real directory
  a legacy binary can just open — errors with a hint to use `checkout`
  instead of reaching the binary as a meaningless literal string. Tab
  completion (inside a bare `Path`) offers both real filesystem and
  namespace entries, which is exactly how this mistake tends to get
  typed in the first place. Two exceptions: a fullscreen program
  (`vim`, ... — see `fullscreen_programs`) gets a namespace-only `Path`
  checked out and written back automatically instead of erroring, and a
  native program (`9ed`, ... — see `native_programs`) gets the `Path`
  passed through as its literal path text, untouched, trusting the
  program to resolve it itself — a native program also doesn't need the
  `%` sigil at all, callable bareword like a builtin.

### Example: a starter `common.ky`

A namespace layout is something you build up yourself via dotfiles,
same as a `.bashrc`'s aliases and `PATH` — there's no single blessed
default. A minimal starting point:

```
# ~/.config/9/ns/common.ky — run once at every 9sh startup

# Give the launch directory a name that isn't the bootstrap default.
bind /local, /work

# dir(path) wraps any host directory into a bindable value — the local
# sibling to dial(addr) — so this isn't limited to subtrees of wherever
# 9sh happened to launch from. bind it once at a short namespace path,
# then join_path builds further namespace paths off that alias instead
# of retyping the host path each time.
bind dir("/u/some/long/path"), /std/projects
bind join_path(/std/projects, "locn"), /std/locn

# Best-effort: if nothing's listening yet, this bind fails and
# dotfiles.Load reports one startup warning — it never blocks the
# rest of common.ky or hosts/<hostname>.ky from running.
bind dial("/run/user/1000/9ed/main.sock"), /n/9ed
```

```
# ~/.config/9/ns/hosts/build-box.ky — only run on that one host

bind dial("ci.internal:2049"), /n/ci
```

`dir(path)` requires an absolute host path (kyu has no "current
directory" to resolve a relative one against — see above) and, like
`dial`, returns an ordinary `ErrorVal` rather than aborting if the path
doesn't exist.

## Startup sequence

Every mode (the interactive TUI, the plain `-repl`, or a script) shares
one bootstrap, in this order, before any of your own code runs:

1. `/jobs` is bound (the job manager).
2. `/local` is bound — `dirfs` over the real directory 9sh was launched
   from.
3. `-listen`/`-listen-unix`, if either flag was passed, start serving
   this namespace out. `-listen-unix` also exports
   `$_9SH_UNIX_SOCK` into 9sh's own process environment, so every job
   it spawns inherits it with zero configuration.
4. `/env` is bound — a one-time snapshot of `os.Environ()`, taken
   *after* step 3 so a job reading `/env` also sees
   `_9SH_UNIX_SOCK`. It's a snapshot, not a live view: `setenv()`
   writes into this snapshot, not into 9sh's real process environment.
5. `/config` is bound — `~/.config/9/config/config.ky` is seeded with
   defaults (`fullscreen_programs`, `native_programs`) the first time
   only; an existing file is never overwritten.
6. `/session` is bound — best-effort (needs `9vcs` on `PATH` and a home
   directory); the directory is still bound even when the recorder
   itself couldn't start, so past history stays readable as plain
   files either way.
7. The shared kyu `Env` is created, with everything above already
   live in the namespace.
8. **`config.ky` runs** (`~/.config/9/config/config.ky`) — settings
   like `fullscreen_programs`/`native_programs`.
9. **Dotfiles run**: `~/.config/9/ns/common.ky`, then
   `~/.config/9/ns/hosts/<hostname>.ky`, against that same `Env`.

Two things fall out of running in exactly this order:

- **Settings are in scope before dotfiles run.** `config.ky` (step 8)
  runs before dotfiles (step 9) specifically so a dotfile can *extend*
  `fullscreen_programs`/`native_programs` (`native_programs :=
  native_programs + ["mytool"]`) instead of having to redeclare the
  whole list.
- **The host file overrides common, because it loads second.** Both
  files run against the same `Env`, so a variable redefined in
  `hosts/<hostname>.ky` simply shadows whatever `common.ky` set —
  there's no merging.

There are three separate, independently-optional files/directories
under `~/.config/9`, not one: `config/config.ky` (settings, auto-seeded
once), `ns/common.ky` + `ns/hosts/<hostname>.ky` (namespace recipes,
never auto-created — a fresh install has none of these), and
`session/` (auto-managed history, not meant to be hand-edited). None of
steps 3 through 9 are fatal to starting the shell on their own: a
missing `9vcs`, no home directory, or a syntax error in `config.ky`/
`common.ky`/`hosts/<hostname>.ky` each print one warning to stderr and
are otherwise skipped — a broken `common.ky` doesn't even block a
working `hosts/<hostname>.ky` from still loading. Only `-listen`/
`-listen-unix` failing to bind is fatal, since you explicitly asked to
serve on that address.

## Using the interactive TUI

Run `9sh` with no arguments in a real terminal and you land in the
interactive TUI — 9sh's primary, default way to work, not a fallback.
It's always exactly one kyu session filling the whole screen, by
design: for a real multi-pane terminal (9sh alongside a shell, or
several 9sh sessions side by side), see
[`9mux`](https://github.com/sandgorgon/9mux) instead, a separate
project this binary doesn't host itself. The same keybinding reference
below is built into 9sh: press `F1` any time.

| Key | Does |
|---|---|
| `Enter` | Submit, or keep editing if brackets are still open |
| `Tab` | Complete the identifier before the cursor (variables, builtins, keywords) — right after a `%` sigil, an external command name from `PATH`; inside a bare `Path` literal, entries from both the real filesystem and the attached namespace, merged — fills the longest common match, cycles through candidates on repeated `Tab` |
| Ctrl+R | Reverse history search (bash's reverse-i-search) — type to search, `Enter` runs the match immediately, `Esc` loads it into the input line without running it, repeated Ctrl+R searches further back |
| `←`/`→`, Ctrl+`←`/`→` | Move the cursor by character / by word |
| `Home`/`End`, Ctrl+A/Ctrl+E | Jump to the start/end of the current line |
| `Backspace`/`Delete` | Delete before/after the cursor |
| Ctrl+W | Delete the word before the cursor |
| Ctrl+U / Ctrl+K | Delete to line start / delete to line end |
| Ctrl+L | Clear the transcript (bash/zsh/readline convention) — history (Up/Down, Ctrl-R) is untouched |
| `↑`/`↓` | Recall previous/next submitted input (only outside a multi-line continuation) |
| `PageUp`/`PageDown`, mouse wheel | Scroll the transcript, independent of the input line |
| Ctrl+C | Copy the whole transcript |
| Alt+C | Copy only what's currently visible on screen |
| paste | Inserts at the cursor |
| `F1` | Toggle the built-in help screen |
| Ctrl+D (at an empty prompt) | Quit 9sh — bash/zsh's own "EOF at an empty prompt exits" convention |

Ctrl+C is "copy all," not the Ctrl+Shift+C you might expect from a
desktop terminal: most terminal emulators (this one's own standing
test target, gnome-terminal/VTE, included) send the identical byte for
Ctrl+C and Ctrl+Shift+C on a plain letter key — only a kitty-keyboard-
protocol-aware terminal can tell them apart, which isn't something to
assume. Alt+C for "just the visible part" sidesteps that ambiguity
entirely.

The input line is syntax-highlighted live as you type (keywords,
strings, numbers, paths, and the `%`/`@` sigils each get their own
color) — the already-submitted transcript above it doesn't, by design;
this is a live editing aid, not a retroactive recolor of everything
ever printed.

No undo/redo, and no multi-line-aware history recall (Up/Down inside
an open multi-line continuation navigate lines, not history) —
deliberate scope cuts for a REPL input line, not a general text
editor.

Running a fullscreen program (`vim`, `top`, `ssh`, ... — see
`fullscreen_programs` above) hands the whole screen and keyboard to it
directly until it exits; a namespace-only `Path` argument is checked
out and written back automatically, no `checkout()` call needed. Mouse
wheel and `PageUp`/`PageDown` scroll its scrollback too (up to 10,000
lines), unless the program manages its own full-screen display (`vim`,
`htop`, `less`, ...), in which case those keys go to it as normal.

## Local namespace access

`dial(addr)` and `-listen`/`-listen-unix` are two ends of the same
mechanism — reaching a namespace that isn't your own — split by whether
the other end is on this machine or somewhere else:

| | Same machine | Different machine |
|---|---|---|
| **Reach in** (`dial`) | `dial("/path/to.sock")` or `dial("unix:/path")` | `dial("host:port")` |
| **Serve out** | `-listen-unix path` | `-listen host:port` |
| **Trust** | this user's UID only | mutual TLS + `9auth` identity, TOFU-pinned |

Cross-machine traffic (`-listen`/`dial("host:port")`) always goes over
mutual TLS: both sides present their standing `9auth` identity
(`~/.config/9/identity.{key,cert}`, generated on first use), and an
unrecognized peer's fingerprint prompts once to trust-and-remember it
(`~/.config/9/known-peers`) — a later mismatch is always a loud refusal,
never a silent pass. Only fingerprints listed in
`~/.config/9/authorized-peers` can attach at all.

Same-machine traffic (`dial` on a path, `-listen-unix`) skips all of
that by design: a Unix socket's own file permissions are already the
trust boundary, the same one a local directory bind (`/local`) sits in.
`-listen-unix` reinforces it two ways — the socket file is `chmod`'d
`0600` regardless of your umask, and every connection is checked via
`SO_PEERCRED` against your own UID and dropped on any mismatch, so
nothing short of your own user (or root) can attach. That access is
intentionally *not* scoped down to local-only content: a connection is
trusted to see the whole namespace as assembled, remote binds included
— the same way connecting to `ssh-agent` lets you use whatever remote
hosts its loaded keys are already trusted by, not just local ones.

A Unix socket path is capped at 108 bytes by the OS
(`sockaddr_un.sun_path`); keep it short — somewhere under
`$XDG_RUNTIME_DIR` is the usual choice.

## Status

Pre-1.0 (`v0.4.28`). The full v1 build-order plan (namespace
core, jobs, kyu, an interactive TUI, session history, remote namespace/
auth, dotfiles sync) is implemented and covered by real tests — real 9P
traffic over Unix sockets and TCP, real subprocess execution, real
mutual-TLS handshakes between distinct identities, `-race` clean
throughout, and every phase additionally exercised through the actual
built binary, not just `go test`.

9sh's interactive TUI used to be a full multi-pane multiplexer
(package `pane`) — several real bugs (invisible keyboard focus on
launch, blank control-strip/title-bar chrome, an invisible cursor,
stale content surviving a window resize, a resize floor with no room
to actually shrink) were found and fixed through substantial real
hands-on use, not caught by any headless `tui.App` test, and pane
management grew well past the original v1 scope (a real 2D tiling
tree, maximize/zoom, per-pane box-drawing frames, two more namespace-
aware panes for jobs and session history). That generic multi-pane
capability has since moved to its own project,
[`9mux`](https://github.com/sandgorgon/9mux) — 9sh itself now ships a
single-screen interactive TUI (package `replui`) with everything that
was always specific to *it* (the full kyu REPL line editor: cursor
movement, history recall, kill commands, paste, independent scrolling,
clipboard copy, live syntax highlighting, a built-in help screen — see
[Using the interactive TUI](#using-the-interactive-tui)) and nothing
that was only ever about hosting several panes at once. See `9mux`'s
own README for the split's full rationale, including the native 9P-
browsing pane (shipped as of `9mux` `v0.1.0`) that generalizes the old
job-viewer/namespace-browser/session-viewer panes' capability — point
it at a running 9sh's `-listen-unix` socket for a live directory
listing or job table, no dependency on 9sh as a Go library either way.

Getting close to usable as an actual daily driver, not just ready for
hands-on testing — but the kyu language itself is still young enough
that rough edges are expected there. Proxy-job (`@host{}`) session
recording now has a local-side linking record (host, remote job id,
argv, exit/signal, alongside the ordinary entry the remote peer's own
session repo already logged for it); the remote-namespace ACL model
gained a `propose` permission tier (enough to write/create, short of
remove/wstat) and `ListenWithRootPerms`, scoping a distinct
authorized-peers file to one exported root instead of only the single
global allowlist `Listen` alone still uses. A `native_programs` kyu
variable now lets an external program that's itself namespace-aware
(`9ed` and `9vcs` by default) be called bareword — no `%` sigil — with
the same transparent namespace-path handling `fullscreen_programs`
gets; see "Three call-name categories" in Design below.

`dial`/`bind` and a new `-listen-unix` now cover the same-machine half
of namespace access without any TLS/`9auth` overhead — see
[Local namespace access](#local-namespace-access) — closing the gap
where a purely local 9P server or reader had no lighter-weight option
than the full remote-peer trust machinery.

## Design

- **No FUSE.** Namespace transparency is userspace-only, through three
  interop tiers: anything linking `9p`'s client gets full transparency
  for free; classic Unix pipeline tools need nothing beyond stdin/
  stdout; tools needing a real seekable path use `checkout` to
  materialize a subtree to a scratch directory and write back on close.
  A `Path` argument handed straight to `%cmd` that only resolves
  in the namespace, not on the real filesystem, is caught with an error
  pointing at `checkout` rather than reaching the external binary as a
  meaningless literal string — the one case this can't catch is a
  namespace path that happens to alias an unrelated real file, since no
  FUSE means there's no way to tell the two apart from outside. A
  fullscreen program (`fullscreen_programs`, e.g. `vim`) is the third
  tier automated: `checkout`'s own materialize-and-write-back runs
  transparently around it instead of erroring.
- **Three call-name categories, one of them config-driven.** Everything
  callable in kyu is either a language builtin (`where`, `format`, no
  namespace/OS involvement), a namespace app (`cat`, `cp`, `rm`, `mv`,
  `stat`, ... — in-process Go functions that touch the namespace,
  already called bareword), or an external program reached with `%`
  (Bytes-only, legacy). A fourth spelling of the third category —
  `native_programs` — is for external programs that are themselves
  namespace-aware: `9ed` was the first, `9vcs` (its `-C <path>` flag,
  as of 9vcs v0.1.7) is the second. Listed there, each is callable
  bareword like a namespace app with no `%`, and given a namespace-only
  `Path` argument as its literal path text, untouched, instead of
  `%cmd`'s ordinary error — no `checkout`, no scratch copy; the program
  is trusted to dial 9sh's namespace socket and resolve the path itself
  (see 9ed's own `nsopen.go`: an absolute path is tried as a literal
  namespace `Walk` first, a relative one is rooted at `/local`, and
  either falls back to a real OS path only if the namespace doesn't
  claim it). Extend it the same way:
  `native_programs := native_programs + ["mytool"]`. An existing
  identifier (a builtin, or anything already defined) always wins over a
  same-named `native_programs` entry, silently — matching how
  PATH-resolved `%cmd` names already coexist with kyu identifiers with
  no collision today.
- **Structured pipes.** Records and tables flow through `|` by default
  (nushell/PowerShell-style); `%` marks a call into legacy/external
  Bytes-land.
- **One identity, one trust decision.** `9auth`'s per-install Ed25519
  identity and TOFU peer trust cover 9vcs sync, remote namespace mounts,
  and proxy jobs alike — mutual TLS authenticates once at the transport
  layer, so `Tattach`'s `uname` is never a client-asserted string.
- Package doc comments throughout (`ns`, `job`, `kyu/eval`, `remote`,
  `session`, `dotfiles`, `config`, `replui`) go into the "why," not just
  the "what," for anyone picking a subsystem apart.

## Testing

```
go build ./...
go vet ./...
go test -race ./...
```

## License

MIT — see [`LICENSE`](LICENSE).
