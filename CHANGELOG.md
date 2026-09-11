# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project intends to follow [Semantic Versioning](https://semver.org/)
once a first tagged release is cut.

## [Unreleased]

### Added

- Docs: a "Startup sequence" section in README.md spelling out
  `bootstrap`'s exact order (namespace binds, `-listen`/`-listen-unix`,
  then `config.ky`, then `common.ky`/`hosts/<hostname>.ky`) and the
  nuances that fall out of it — `config.ky` loading before dotfiles so
  a dotfile can extend `fullscreen_programs`/`native_programs` instead
  of redeclaring them, the host file overriding `common.ky` because it
  loads second, and which of the three separate `~/.config/9`
  subdirectories are auto-seeded vs. auto-managed vs. never
  auto-created. Previously only documented as Go doc comments on
  `cmd/9sh`'s `bootstrap`, `config.Load`, and `dotfiles.Load`. Mirrored
  as a fourth in-app `?`/F1 help-screen section (`replui/help.go`,
  `startupSequenceHelp`), jumpable with `4` alongside the existing
  `1`/`2`/`3` sections.

## [0.4.26] - 2026-09-10

### Changed

- Bumped `github.com/sandgorgon/tui` from v0.6.2 to v0.8.0. The
  fullscreen `%cmd` handoff (`vim`, `top`, `ssh`, ... — see
  `fullscreen_programs`) now supports scrollback via mouse wheel and
  `PageUp`/`PageDown` (up to 10,000 lines) — the same `widget.Terminal`
  9mux's own Terminal pane uses — while leaving alt-screen programs
  (`vim`, `htop`, `less`, ...) alone since they manage their own
  scrolling. Wired the app's theme through so the "[scrollback N/M]"
  indicator matches the rest of the UI instead of rendering unstyled.
  v0.6.2 to v0.7.0 in between was theme-only (a new `Chrome`/
  `ChromeText` pair backing the scrollback indicator's styling; no
  9sh-visible effect on its own).

## [0.4.25] - 2026-09-09

### Added

- `9vcs` added to the default `native_programs` list, alongside `9ed`.
  `9vcs` v0.1.7 added a `-C <path>` flag that, under a 9sh session
  (`$_9SH_UNIX_SOCK` set), resolves against 9sh's own namespace first
  — the same self-resolving pattern `9ed` already uses — so it
  qualifies for the same literal-`Path`-passthrough treatment instead
  of `%cmd`'s ordinary checkout guard. `9vcs` is an ordinary CLI, not
  a terminal-owning program, so it's added only to `native_programs`,
  not `fullscreen_programs`.

### Changed

- Bumped `github.com/sandgorgon/9p` from v0.7.1 to v0.9.1 — no API
  breakage, both intervening releases are additive/opt-in
  (`client.File.Rename`/`Remove`; optional 9P2000.u symlink support
  via `client.WithUnixExtensions()`, negotiated only when both sides
  ask for it). Worth taking regardless of the additions: v0.8.0 also
  fixed a real path-confinement gap in `examples/dirfs` — a symlink
  planted at an intermediate path component could escape the exported
  root at syscall time — and 9sh's own `/local`/`/env`/`/config`/
  `/session` namespace binds go through that exact `dirfs` backend
  (`cmd/9sh/main.go`, `kyu/eval/builtins.go`).
- `sandgorgon/tui` checked against upstream — already at the latest
  tag (`v0.6.2`, matching go.mod); no bump needed.
- `native_programs`' namespace-only `Path` handling no longer checks
  out/materializes to a scratch copy — a `Path` argument is now passed
  through as its literal path text, untouched. This follows `9ed`
  v0.8.0 dropping `$_9SH_NS_PATH` in favor of resolving namespace paths
  itself (dialing `$_9SH_UNIX_SOCK`, trying an absolute argument as a
  literal namespace `Walk` first or a relative one rooted at `/local`,
  falling back to a real OS path only if the namespace doesn't claim
  it): the checkout round-trip 9sh did on `9ed`'s behalf was not only
  redundant but reintroduced the read/write staleness window
  `native_programs` was meant to avoid. `fullscreen_programs`'
  checkout-and-write-back treatment (`vim`, ...) is unchanged.

## [0.4.24] - 2026-09-08

### Added

- `rm(path)`/`mv(src, dst)` namespace builtins — `server.File.Remove`/
  `WStat`, already implemented by every backend a namespace can bind,
  now exposed to kyu directly. `mv` does a real in-place rename via
  `WStat` when `src`/`dst` share a parent directory, falling back to
  copy-then-remove otherwise. Both regular-file only, no recursive
  directory support yet, same v1 scope as `cp`.
- `format_time(layout, epoch_seconds)`/`humanize_time(epoch_seconds)`
  give `stat`/`ls`'s raw Unix-epoch `mtime`/`atime` fields a
  human-readable rendering (a fixed Go reference-time layout, or a
  short relative phrase like `"5 minutes ago"`).
- Ctrl+L clears the interactive TUI's transcript (bash/zsh/readline
  convention) — history (Up/Down, Ctrl-R) is untouched.
- A third call-name tier alongside kyu builtins and `%cmd`: a new
  `native_programs` kyu variable (mirrors `fullscreen_programs`,
  defaulted in `/config/config.ky`) names external programs that are
  themselves namespace-aware — `9ed` is the first, seeded by default.
  Listed names are callable bareword, no `%` sigil needed (including
  Plan-9-style digit-leading names like `9ed`), and a namespace-only
  `Path` argument gets the same transparent checkout-and-write-back
  treatment `fullscreen_programs` already gets, instead of `%cmd`'s
  ordinary error. Composes into pipes and closures exactly like `%cmd`
  does, since it produces the identical AST node under the hood.

### Fixed

- A foreground `%cmd`'s captured stderr was written straight to the
  real `os.Stderr` unconditionally, corrupting the interactive TUI's
  own screen (it owns the terminal via a diffed renderer; a raw write
  outside that renderer's bookkeeping desyncs "what's on screen" from
  reality). Now routed through the same transcript ordinary results
  already go through, whenever the TUI is running.
- `list + list` had no case in kyu's `+` operator at all, despite
  `fullscreen_programs`' own docs already describing `x := x + [...]`
  as the way to extend a config-driven list variable — nobody had
  exercised it until `native_programs` needed the identical pattern.

### Changed

- Bumped the `tui` dependency to v0.6.2: retunes `DefaultDark`/
  `DefaultLight`'s `Border`/`Muted` contrast and `Success`/`Warning`/
  `Error` colorblind-safety/ANSI-16 fallback — the exact theme the
  help overlay renders with.

## [0.4.23] - 2026-09-07

### Added

- `%cmd` now detects fullscreen programs (`vim`, `top`, `htop`, `less`,
  `man`, `ssh`, `nano`, `mutt`, `emacs`, `vi`, `nvim` by default) via a
  new `fullscreen_programs` kyu variable, and hands them the real
  screen and keyboard directly — including inside the interactive TUI,
  via the same real-pty machinery a hosted shell already used. A
  namespace-only `Path` argument to a fullscreen command is
  transparently materialized via `checkout()` and written back on
  exit, instead of erroring the way an ordinary `%cmd` does.
- New `/config` namespace path, backed by `~/.config/9/config/config.ky`
  (auto-created with sensible defaults on first run) — settings like
  `fullscreen_programs` are now real, namespace-visible, checkout-able
  files instead of hidden Go-side state, loaded before `common.ky`/
  `hosts/<hostname>.ky` so dotfiles can extend them.
- New `/session` namespace path, backed by the same directory session
  history is already recorded to (`~/.config/9/session`) — the day-
  sharded `.nrl` history files are now real, namespace-visible,
  checkout-able files, reachable the same way `/jobs`/`/local` already
  are (including from an external tool like `9mux`'s 9P-browsing pane
  over `-listen-unix`, with no bespoke session-viewer needed on that
  side). Bound unconditionally whenever a home directory exists, even
  without `9vcs` on `PATH` — reading past history back is plain disk
  I/O — so a 9vcs-less run still gets a (possibly empty) `/session`,
  just without new entries being recorded into it.

### Changed

- **Breaking:** the multi-pane multiplexer (package `pane` — split/
  resize/minimize/zoom, the control strip, and the namespace-browser/
  job-viewer/session-viewer panes) is gone from this binary. Its
  generic mechanics moved to a new, separate project,
  [`9mux`](https://github.com/sandgorgon/9mux) (any command in a pane,
  9sh included), which as of its own `v0.1.0` also ships a native 9P-
  browsing pane — pointed at a running 9sh's `-listen-unix` socket, it
  reproduces the removed browser/job-viewer panes' own capability (a
  live directory listing; a job table with live wait-driven auto-
  refresh and a kill keybinding) without 9mux depending on 9sh as a Go
  library — see that repo's README for the browsing-preset config
  syntax. 9sh itself now ships a single-screen interactive TUI instead
  (new package `replui`) — the same kyu REPL
  editing experience (live syntax highlighting, Ctrl-R history search,
  Tab completion, the fullscreen-`%cmd` handoff above) with no split
  tree around it. Run `9sh` with no arguments for this screen exactly
  as before; run it inside a `9mux` pane for a multi-pane terminal. New
  keybindings replace what the removed control strip's buttons did:
  `F1` toggles the built-in help screen (was the `help` button), and
  Ctrl+D at an empty prompt quits (was the `quit` button) — the
  `theme` button has no replacement, since nothing left on this single
  screen is theme-colored chrome to toggle.

### Removed

- **Breaking:** the `$cmd` sigil/syntax (`ast.PassthroughStmt`) is
  gone. It existed to give a command the real terminal directly, but
  only worked outside the TUI; `%cmd`'s new fullscreen detection
  supersedes it everywhere, including inside the TUI, which `$cmd`
  never could reach. `$` is now an illegal lexer token — replace any
  `$cmd arg...` with a plain `%cmd arg...` (add the command name to
  `fullscreen_programs` if it isn't already covered by the default
  list).

## [0.4.22] - 2026-09-07

### Fixed

- Bumped `github.com/sandgorgon/tui` to v0.6.0 and set `Frameless` on
  the job-viewer, session-viewer, and namespace-browser panes' `List`
  widgets — each was drawing its own border one cell inside the
  pane's own frame, a redundant double border that also wasted two
  rows and two columns of content space.

## [0.4.21] - 2026-09-07

### Fixed

- Bumped `github.com/sandgorgon/tui` to v0.5.2. Clicking the control
  strip's `help` button opened and then immediately closed the `?`
  screen again on the same click's release event
  ([sandgorgon/tui#28](https://github.com/sandgorgon/tui/issues/28)).
  Exiting 9sh while focus was on a widget that hides its own caret
  (e.g. a button, a `List`) also left the real terminal cursor hidden
  after exit ([sandgorgon/tui#29](https://github.com/sandgorgon/tui/issues/29)).

## [0.4.20] - 2026-09-07

### Added

- `find(dir, pattern)`, `cat(path)`, `cp(src, dst)`, and `ps()`
  builtins — recursive namespace search (`glob`'s missing `**`), a
  namespace file's content read and copied without a full
  `checkout()` round trip, and a structured `Table` view of `/jobs`
  instead of hand-walking its `status` files.
- The namespace-browser pane now previews a selected file's content in
  place (`Enter`/click a file; `Esc`/`Backspace` returns to the
  listing) instead of doing nothing — the browse pane's answer to
  `cat(path)`.

### Fixed

- A pane title bar's `(x/d/r/z/+/-)` hint now sits directly beside its
  title words instead of past the `[zoomed]`/`(exited)` badges at the
  very end of the label.

## [0.4.19] - 2026-09-07

### Fixed

- Bumped `github.com/sandgorgon/tui` to v0.5.1, fixing
  [sandgorgon/tui#26](https://github.com/sandgorgon/tui/issues/26) —
  a shell pane's `Terminal` was forwarding every mouse click to the
  hosted shell unconditionally, so a plain shell with no mouse
  reporting enabled received the raw SGR mouse bytes as literal
  keyboard input (visible as garbage like `0;37;9M3;37;9m` typed into
  the prompt on click). Also picks up correct SS3 arrow/Home/End key
  encoding under DECCKM application cursor-key mode, relevant to
  full-screen programs (vim, less, ...) run inside a shell pane.

## [0.4.18] - 2026-09-07

### Fixed

- A focused pane's title bar no longer fills its entire width with the
  focus highlight — the highlight now stops right after the label text
  (e.g. the closing `)` in `(x/d/r/z/+/-)`), with the rest of the row
  falling back to the plain border color, so a focused title reads as
  a highlighted label rather than one solid-colored block. Control-strip
  buttons are unaffected.

## [0.4.17] - 2026-09-06

### Added

- `round(places, number)` builtin — rounds to a fixed number of decimal
  digits, half-away-from-zero, always returning a Float. Fills the gap
  `format`'s `"{}"` doesn't cover (shortest round-trip formatting, no
  precision control): `number | round(2) | format("{}")` for a String
  with a guaranteed decimal precision.

## [0.4.16] - 2026-09-06

### Added

- `len`/`repeat`/`pad_left`/`pad_right` builtins — `len` counts runes
  for a String or elements for a List/Table; `repeat`/`pad_left`/
  `pad_right` are for building an exact fixed-width line of output
  (aligned columns, a separator rule) without hand-writing a
  self-recursive closure for something this ordinary.
- `upper`/`lower`/`starts_with`/`ends_with`/`index_of` string builtins,
  and `contains`/`index_of` now also accept a `List`/`Table` input
  (element equality) instead of only a String substring check.
- `to_int`/`to_float` builtins — parse a String into a number (an
  unparseable one is an `ErrorVal`, not a hard error); previously there
  was no way to do arithmetic on a script's own `args`, which always
  arrive as `String`.

### Fixed

- `kyu/eval/docs.go`'s documented signatures for `split`/`replace`/
  `contains` had the string argument first (`split(str, sep)`); the
  real, tested argument order puts it last (`split(sep, str)`), matching
  every other builtin's "explicit args..., then the pipeable input"
  convention. Docs corrected to match actual behavior.

## [0.4.15] - 2026-09-06

### Fixed

- `pane/kyurepl.go`'s `resultLines` only split embedded `\n` characters
  into separate transcript rows for `%cmd` output (`value.Bytes`) —
  every other result kind, including a plain kyu `String` (e.g. from
  `... | join("\n")`), was handed to a single transcript row as-is.
  Since a row painter has no notion of a line break mid-row, each
  embedded newline rendered as a blank/space instead of an actual line
  break — two consecutive newlines (a template's own `\n` plus
  `join("\n")`'s separator) showed up as two spaces. `resultLines` now
  splits on `\n` for any result kind, not just `Bytes`.

## [0.4.14] - 2026-09-06

### Fixed

- `kyu/eval/docs.go`'s `Signature` field for `where`/`select`/`sort_by`/
  `group_by`/`each`/`any`/`all`/`take`/`skip`/`last` showed bareword
  pseudo-syntax (e.g. `... | select field1, field2, ...`) that doesn't
  actually parse — these are plain functions, not keywords, so the real
  syntax needs call-parens and, for predicates, the `{ |row| ... }`
  pipe-position closure sugar (e.g. `... | select("field1", "field2")`,
  `... | where { |row| cond }`). This table is the source for both
  `help(name)` and the in-app `?` screen's language-reference section,
  so the wrong syntax was reaching users directly. Also fixed the same
  bug in README.md's own `vars() | where kind == "path"` example.

### Added

- `last` now accepts an optional count (`... | last(n)`, the last n
  elements as a List), matching what its doc description already
  claimed and mirroring `take`/`skip`'s existing count-argument
  handling — previously `last` rejected any argument at all.

## [0.4.13] - 2026-09-06

### Fixed

- v0.4.12's Tab-completion change updated README.md and `docs.go` (the
  `help(name)`/`?`-screen-section-2 source) but missed `pane/help.go`'s
  `keybindingHelp` — a separate, hand-maintained block behind the
  in-app `?` screen's section 1, describing the old (identifiers/
  external commands only) Tab behavior. Also added a bullet to the
  README's "Coming from bash/zsh" section (and its `?`-screen
  condensation, `bashZshHelp`) covering the new `%cmd`/`$cmd`
  namespace-path guard directly, since that section is exactly where a
  user would otherwise make the mistake it catches.

## [0.4.12] - 2026-09-05

### Fixed

- A bare `Path` right after a `%cmd`/`$cmd` command name (`%cat
  /etc/hosts`) mis-lexed as division (`cat / etc / hosts`) instead of
  a Path literal — the same disambiguation gap already known and
  worked around for `bind`'s SRC/DST, never previously hit for
  external calls. Only the first argument right after the command
  name is covered; a bareword Path after a non-Path argument (`%grep
  "foo" /path`) still divides, a known, narrower remaining gap.

### Added

- `%cmd`/`$cmd` now guard against a namespace-only `Path` argument
  (e.g. `%cat /local/foo`, where `/local` has no real OS path):
  previously it reached the external binary as a meaningless literal
  string, either a confusing `ENOENT` from inside the binary's own
  process or, worse, an unrelated real file at the same string. Now
  it errors with a clear hint to use `checkout` instead — unless the
  path resolves to a real filesystem path too, or doesn't resolve in
  the namespace at all (an ordinary typo still gets the external
  tool's own honest error).
- Tab completion inside a bare `Path` literal now offers entries from
  both the real filesystem and the attached namespace, merged —
  previously only external-command-name completion (right after a
  `%`/`$` sigil) existed; a `Path` fragment completed to nothing at
  all. Directories (real or namespace) get a trailing `/`, matching
  classic shell completion.

## [0.4.11] - 2026-09-04

### Added

- `help(name)`/`help()` builtin: returns a builtin/keyword's signature
  and description as a `Record` (or every entry as a `Table` with no
  argument), read from a single hand-maintained table shared with the
  pane multiplexer's `?` screen.
- The `?` help screen now has three sections instead of just
  keybindings: the original keybinding reference, a full kyu language
  reference (generated from the same table `help()` reads, so the two
  can't drift apart), and a plain-text condensation of the README's
  "Coming from bash/zsh" mental-model section. `1`/`2`/`3` jump
  straight to a section.

## [0.4.10] - 2026-09-04

### Added

- `path(str)` builtin: explicit `String` -> `Path` conversion, so
  dynamically-built path text (`format(...)`, `split`/`join`,
  `getenv(...)`) can reach `bind`/`checkout`/`stat`/`join_path`'s base
  — all of which require an actual `Path` and reject a `String`, same
  as `dial`/`dir` require a `String` and reject a `Path` in the
  opposite direction. Requires an absolute string, same rule `dir()`
  already applies.

## [0.4.9] - 2026-09-04

### Added

- `stat(path)`/`ls(pattern)` builtins: real namespace file metadata
  (size, permissions, `is_dir`, mtime/atime, uid/gid), which was
  always one `Stat(ctx)` call away in the underlying 9P layer but never
  surfaced to kyu. `stat` returns one `Path`'s properties as a
  `Record`; `ls(pattern)` is `glob`'s metadata-bearing sibling — same
  directory-and-pattern matching, returning a `Table` instead of bare
  `Path`s.

### Fixed

- Docs: several README examples showed `%ls <namespace-path>` (e.g.
  the Quick Start's own first example) as if it worked — it doesn't
  and can't: `%cmd` hands a `Path` argument to the real external binary
  as a literal string, with no namespace resolution. Replaced with
  `ls`/`glob`, the actual working namespace-native mechanisms.

## [0.4.8] - 2026-09-04

### Fixed

- Pane multiplexer: a pane's title bar now stays highlighted while
  focus is on any of that pane's own widgets (its content included),
  not only while the title bar itself is the literal tab-focused
  widget — previously tabbing from a pane's title onto its own content
  made the highlight disappear even though you were still working in
  that pane, including the very first pane at launch, whose title bar
  showed unfocused despite already having real keyboard input. Bumps
  `tui` v0.1.13 → v0.5.0 for the new `tui.FocusAware` hook
  (`sandgorgon/tui#24`).

## [0.4.7] - 2026-09-03

### Added

- `dir(path)` builtin: wraps an arbitrary absolute host directory into
  a bindable value, `dial(addr)`'s local sibling — `bind` could
  previously only reach the real filesystem through `/local`'s own
  startup bind, nothing else.
- `join_path(base, ...segments)` builtin: builds a `Path` from an
  already-fully-qualified base plus string segments, without
  introducing any notion of a namespace-relative cwd (kyu deliberately
  has none — see the new README section below).
- `vars()` builtin: lists your own `:=`-defined kyu variables as a
  pipeable `Table` (name/kind/value), filtered clear of builtins.
  `unset(name)` is its companion removal verb.
- README: new "Coming from bash/zsh: there is no current directory"
  section, explaining why kyu's namespace has no cwd concept and what
  `/local`/`cd()`/`pwd()` actually are — a persistent point of
  confusion for anyone arriving from a Unix shell.

## [0.4.6] - 2026-09-03

### Fixed

- Docs: the README's kyu-repl Tab row only described identifier
  completion, missing the `%`/`$`-sigil PATH-command behavior added in
  0.4.5. The in-app help screen (opened with `?`) was also missing Tab
  completion, Ctrl+R reverse history search, and Ctrl+\ release-focus
  for the kyu-repl pane entirely — a pre-existing gap from when those
  first shipped, not something 0.4.5 introduced.

## [0.4.5] - 2026-09-03

### Added

- Kyu-repl pane: Tab now completes external command names against real
  `PATH` executables right after a `%`/`$` sigil, instead of only
  offering user variables/builtins/keywords in that position. Resolved
  through the same `/env`-backed `PATH` a `%cmd`/`$cmd` would actually
  use, so completion never offers a name the command wouldn't resolve
  to; the fragment scan allows internal hyphens (`docker-compose`,
  `apt-get`), matching how the lexer itself reads an external command
  name.

## [0.4.4] - 2026-09-03

### Fixed

- Pane multiplexer: a shell pane's output now appears right after
  pressing Enter, instead of needing one more unrelated keypress to
  force a redraw. `widget.Terminal`'s pty output updates its internal
  screen state in a background goroutine, but that only became visible
  the next time the app happened to render a frame for some other
  reason — nothing was driving a periodic redraw while a shell pane
  was live.

## [0.4.3] - 2026-09-03

### Fixed

- `cd(path)`: a relative `path` is now resolved against the current
  virtual cwd (falling back to the real `os.Getwd()` before `cd()` has
  ever been called), instead of being checked against — and, on
  success, stored as — the real OS process's own unrelated working
  directory. Previously `cd("/a")` followed by `cd("b")` landed `pwd()`
  on the literal string `"b"` instead of `/a/b`.

## [0.4.2] - 2026-09-03

### Added

- `pwd()` builtin: reads the working directory `cd()` set directly from
  in-process state (`Env.Cwd()`), instead of needing to shell out to
  `%pwd`. Falls back to the real `os.Getwd()` before `cd()` has ever
  been called, so it's truthful from startup.

## [0.4.1] - 2026-09-02

### Fixed

- Lexer: a command name right after `%`/`$` that starts with a digit
  (e.g. `9ed`, `9term` — Plan-9-style tool names) is now recognized as
  the external-call sigil form instead of falling through to the `%`
  modulo operator, which produced `unexpected token %("%")` at
  statement start.

## [0.4.0] - 2026-09-01

### Added

- New `$cmd` syntax: runs an external command connected directly to the
  real terminal's stdin/stdout/stderr, with no job created and no output
  capture — for programs `%cmd` structurally can't support because it's
  always job-tracked (a job's streams are in-memory buffers, never a
  real TTY), like `vim`, `ssh`, or another interactive REPL. A dedicated
  statement (not an expression), so it can't appear inside a pipe or as
  a call argument. Available in the plain REPL (`-repl`) and scripts;
  refused with a clear error inside the pane multiplexer's kyu REPL
  pane, where it would race the TUI's own raw-mode stdin reader for
  every keystroke — use a shell pane (`+ shell`) there instead.
- kyu gains a loop construct: `while cond { body }`, plus `break` and
  `continue`. Previously the only way to iterate was recursion via a
  self-referencing closure. A nested loop's `break`/`continue` only
  affects its own innermost `while`.
- `cd(path)` sets the working directory `%cmd`/`$cmd` subprocesses run
  in. Per-session state (the same shape as `bind`/the namespace itself),
  not a real `os.Chdir()` — every pane in a TUI session shares one
  process, so a real chdir would silently redirect every pane at once.
  Propagates to job-tracked `%cmd`/`%cmd &` via a new per-job `cwd`
  namespace file, alongside the existing `argv`/`env`.
- Real environment-variable support: `getenv(name)`, `setenv(name,
  value)`, `unsetenv(name)`, backed by a new `/env` namespace (Plan 9's
  own convention — env vars as real, bind-able, browsable files, seeded
  from 9sh's own process environment at startup) rather than hidden
  shell state. `%cmd`/`%cmd &`/`$cmd` subprocesses all see the current
  `/env` contents, not just an inherited snapshot — including
  `setenv("PATH", ...)`, which actually changes which binary gets
  resolved (a new `pathresolve` package, since Go's own command lookup
  otherwise always uses 9sh's own real `PATH`, before any per-command
  environment override is applied), not just what a child process sees
  about its own environment.
- `-repl`'s Ctrl-C now interrupts the currently running `%cmd`/`$cmd`
  (a real `SIGINT`, same as a normal shell) instead of killing the whole
  9sh process — previously there was no signal handling at all, so
  Ctrl-C hit Go's default "terminate" disposition. Scoped to `-repl`
  only for now; the pane multiplexer's kyu REPL pane needs asynchronous
  evaluation first (a separate, larger change) before an interrupt
  affordance can work safely there.
- New data-pipeline builtins: `last`/`skip`/`reverse`/`uniq`/`flatten`,
  `sum`/`min`/`max`/`avg`, `any`/`all`, `to_json`/`from_json`, and
  string ops `split`/`trim`/`replace`/`contains`/`join`. All follow the
  existing "explicit args..., then the input" pipe-stage convention
  (`where`/`select`/`take`/...); `sum`/`avg` promote `Int`->`Float`
  using the same rule kyu's own `+` already does, not a separate
  numeric-coercion path. `to_json`/`from_json` reuse and add to the
  JSON<->kyu-value conversion already used internally for job status
  decoding.
- `glob(pattern)` — namespace-native, not `filepath.Glob` against the
  real OS filesystem, matching how `cd`/`/env` also avoid raw-OS
  operations in favor of the namespace itself (most of it, like `/jobs`
  or a remote mount, has no corresponding OS path at all to search).
  Single-directory matching only (no recursive `**`), always an
  explicit namespace path — kyu has no "current namespace directory"
  concept to resolve a bare `*.go` against.
- `unbind DST` — the inverse of `bind`, clearing whatever's bound
  there. New `ns.Namespace.Unbind`, plus a kyu-level statement (a
  namespace-mutating verb stays a keyword, matching `bind` itself, not
  an ordinary builtin function).
- Closures gain default parameters (`{ |a, b = 10| a + b }`), evaluated
  per-call so a later default may reference an earlier parameter
  (`{ |a, b = a| ... }`). No separate named-function syntax: `name :=
  { ... }` already supports self- and mutual recursion (a name is
  resolved when the closure is *called*, not frozen at its creation),
  so default params were the one real capability gap, not a missing
  `func` keyword.
- `format(template, values...)` — positional `{}` string
  interpolation as a builtin, not new string-literal syntax (no
  lexer/parser changes).
- `exit_code()` — bash's `$?`, tracking only the last foreground
  `%cmd`/`$cmd` (a backgrounded `%cmd &`'s exit code is already on its
  own job record). Spelled as a function, not literal `$?` syntax,
  since `$` is already `$cmd`'s own sigil.
- A script's own arguments are now visible as `args` (a `List` of
  `String`) in the script's environment — `9sh script.kyu foo bar`
  sees `args == ["foo", "bar"]`.
- The TUI's kyu REPL pane gains tab completion (`Tab` — variables,
  builtins, and keywords; fills the longest common match, cycles
  through candidates on repeated `Tab`), reverse history search
  (Ctrl+R, bash's reverse-i-search), and live syntax highlighting of
  the input line as you type (already-submitted transcript lines are
  untouched — a live editing aid, not a retroactive recolor). Scoped
  to the TUI pane only: `-repl`'s plain `bufio.Scanner` has no raw
  terminal mode and no way to intercept a single keystroke or
  re-render mid-line, so giving it the same features would mean
  building it a real line editor first — a separate, larger
  undertaking, not done here. The pane now claims Tab for its own use
  (`tui.RawKeyClaimer`) rather than the usual global pane-focus
  navigation — `Ctrl+\` releases focus back to that, the same trade
  shell panes already make for real tab-completion in a hosted shell.

### Changed

- `&&`/`||` now chain by real exit status when an operand is a bare
  `%cmd` call — `%cmd1 && %cmd2` runs `%cmd2` only if `%cmd1` exited 0
  (`||`: only if it didn't), matching a shell. Previously `%cmd`'s
  result (its stdout `Bytes`) was always truthy regardless of exit
  code, so `&&`/`||` silently ran unconditionally rather than actually
  gating on success/failure — every other operand kind (a stored
  `%cmd` result included, once captured in a variable) still uses
  ordinary value-truthiness, unchanged. The chain's own result is
  always a `Bool`; a command's actual output isn't surfaced through
  `&&`/`||` at all (chaining is for control flow, not visibility) — use
  `if exit_code() == 0 { %cmd2 }` for that. Fixing this also closed a
  parser gap: `%cmd`'s argument-list parsing didn't know `&&`/`||` end
  the argument list, so `%cmd && ...` used to fail to parse at all.

### Fixed

- Job records now back `stdout`/`stderr` as live, read-only fields
  (alongside the existing `status`/`wait`/`ctl`/`argv`/`env`), so
  `(%cmd &) | wait` gives access to both streams the same way a
  foreground `%cmd` already returns stdout.
- A bare foreground `%cmd`'s stderr is no longer silently dropped — it's
  now read back from the job and forwarded to 9sh's own stderr, the
  same as a direct-exec (no-namespace) `%cmd` already did.
- The REPL (both `-repl` and the TUI kyu pane) now prints a `%cmd`
  result's actual output instead of the `<N bytes>` summary used
  elsewhere — a bare `%cmd` at the prompt is exactly the case where real
  output is wanted.

## [0.3.1] - 2026-08-30

### Added

- `dial(addr)` now also accepts a local Unix-domain-socket path (a bare
  absolute path, or one prefixed `unix:`) alongside the existing
  `host:port` form, for binding a locally-run 9P server (9ed, for
  example) into the namespace with no TLS handshake and no `9auth`
  identity involved — the socket's own file permissions are the trust
  boundary, the same category `dirfs`'s local-directory bind already
  sits in. `bind`'s `MountHandle` routing is unchanged; the dispatch is
  entirely inside `remote.Dial`
  ([sandgorgon/9sh#2](https://github.com/sandgorgon/9sh/issues/2)).
- New `-listen-unix path` flag (`remote.ListenUnix`): serves this
  shell's own namespace over a local Unix socket, restricted to this
  process's own UID (`chmod 0600`, plus an `SO_PEERCRED` check on every
  accepted connection on Linux), instead of requiring the full
  mutual-TLS/`9auth` machinery `-listen` needs for a same-machine
  consumer — another local 9sh, or any 9P-aware app that just wants to
  open files 9sh has bound. Serves the namespace exactly as assembled,
  including anything reached through an existing remote `/n/<host>`
  bind — a same-UID connection is trusted the way ssh-agent/gpg-agent
  forwarding already is
  ([sandgorgon/9sh#3](https://github.com/sandgorgon/9sh/issues/3)).
- When `-listen-unix` is active, its socket path is exported into 9sh's
  own environment as `_9SH_UNIX_SOCK`, inherited by every job it spawns
  (mirrors `SSH_AUTH_SOCK`'s discovery pattern) — a job 9sh itself
  starts can dial straight back into its own parent's namespace with no
  well-known path or extra configuration.

### Fixed

- Dialing or listening on a Unix-socket path over `sockaddr_un`'s
  108-byte limit now fails with an actionable error up front, instead
  of a bare `connect: invalid argument` from the syscall layer.
- `-listen-unix`'s `SO_PEERCRED` check no longer breaks the
  `darwin/amd64`/`darwin/arm64` build (Go's `syscall` package doesn't
  expose it outside Linux); non-Linux platforms fall back to the
  socket file's own permissions as the trust boundary.

## [0.2.1] - 2026-08-30

### Fixed

- kyu: `ast.DefineStmt` now carries `NameTok`, the identifier's own
  token, alongside the existing `':='`-stamped `Tok` — the one
  statement kind that previously had no AST field a caller could use
  to recover its true source start (every other statement kind
  already exposed this via `Tok` itself or via its `Target` expr).
  Matters for any tool doing position-accurate reconstruction from a
  kyu `Program` (editors, formatters, linters, source maps).

## [0.2.0] - 2026-08-30

### Added

- Two more namespace-aware panes: a job viewer ("+ jobs", spanning
  `/jobs` and every host bound under `/n`) and a session-history
  viewer ("+ history", reading `~/.config/9/session` back).
- kyu: `+` now concatenates two strings. Still errors on a string plus
  any other kind — no implicit stringification.
- Pane management, substantially reworked. Panes are arranged in a
  real 2D layout tree, not a single vertical stack:
  - Any title bar: `x` closes, `d`/`r` start a two-step split (pick
    direction, then the new sibling's kind — `s`=shell, `k`=kyu,
    `b`=browse, `j`=jobs, `h`=history, any other key cancels), `z`
    zooms/un-zooms that pane to fill the whole content area (every
    other pane's process keeps running, just out of view), `+`/`-`
    resizes along the split axis down to one visible content line
    (smaller than that, minimize instead), click/Enter minimizes
    (vertical-axis siblings only — see Fixed), F1-F9 jump keyboard
    focus straight to pane 1-9.
  - The control strip's "+" buttons now split the last pane in
    document order (alternating direction each time) instead of
    always appending a flat root-level row, so repeated clicks tile in
    both dimensions the same way a manual split does.
  - Every expanded pane draws its own complete box-drawing frame —
    `┌─ title ─┐` / `│ content │` / `└────────┘`, title embedded in
    the top border line — instead of a bare title-bar row.
  - A "theme" control-strip button flips between light/dark at
    runtime, no restart needed.
  - A "help" control-strip button opens a built-in, scrollable
    keybinding reference (a modal — Esc, `?`, `q`, or a click outside
    it closes).
- kyu REPL pane: real line editing, not just append/backspace —
  cursor movement (character- and word-wise: `←`/`→`, Ctrl+`←`/`→`),
  Home/End (Ctrl+A/Ctrl+E), Ctrl+W/Ctrl+U/Ctrl+K (kill word backward /
  to line start / to line end), Up/Down history recall (only outside a
  multi-line continuation), bracketed-paste support, scrolling the
  transcript independently of the input line (PageUp/PageDown, mouse
  wheel), and copying (Ctrl+C for the whole transcript, Alt+C for just
  what's currently visible — see [README](README.md) for why Alt, not
  Ctrl+Shift).
- Session history: a job run via `@host{}` now gets a local-side
  "proxy" linking record too (host, remote job id, argv, exit/signal),
  alongside the ordinary history line the remote peer's own session
  repo already recorded for it on its side.
- Session history records now carry a `detached` field, reflecting
  what was already true of the recording behavior itself (a `ctl
  detach`'d job was always still recorded — detach only ever meant
  "don't tie this job's lifetime to whatever's watching it," not
  "stop tracking it").
- `remote`: a three-tier permission model instead of write-or-nothing
  — `auth.PermPropose` is now enough to `Write`/`Create`, while
  `Remove`/`WStat` still require full `auth.PermWrite`. New
  `ListenWithRootPerms`, scoping a distinct authorized-peers file to
  one exported namespace root (e.g. `/local`) instead of the one
  connection-wide file `Listen` alone still uses.

### Fixed

- A pane that's a child of a horizontal split can no longer be
  minimized — collapsing its width to one column used to garble the
  title sideways with nothing readable. Its title bar drops the
  `▾`/`▸` chevron to reflect this; a pane in a vertical stack (the
  default) is unaffected.
- The kyu REPL pane's soft cursor was invisible on some terminal color
  schemes (bare reverse-video against the terminal's own unset default
  colors doesn't reliably read as a block). Now an explicit, theme-
  independent block color.
- The control strip's own background color used to be the *same* RGB
  value as a *focused* pane's title bar in both default themes — so a
  focused pane's title bar was visually indistinguishable from the
  always-visible strip above it. Now a genuinely distinct color.
- `-` resize used to be a no-op from the very first press: every new
  pane started already at resize's own floor. Panes now start with
  real headroom to shrink, down to a real one-content-line minimum
  (going smaller than that is minimize's job now, not `-`).
- Real stale-content ghosting on window resize (maximize, restore from
  minimized, etc. in a real terminal): `tui`'s renderer reset its own
  bookkeeping of "what the terminal shows" to blank on a size change,
  but never actually erased the real terminal, so leftover pixels from
  a differently-sized earlier frame could stay visible wherever the
  new layout didn't happen to repaint over them — see
  [sandgorgon/tui#9](https://github.com/sandgorgon/tui/issues/9).

### Changed

- Bumped `github.com/sandgorgon/9p` to v0.7.0, which adds
  `Fid.OpenFile`/`Fid.CreateFile`
  ([sandgorgon/9p#4](https://github.com/sandgorgon/9p/issues/4)).
  `remote/client_fs.go`'s `clientFile` now uses them directly instead
  of discarding its own fid and re-walking the whole path from the
  attach root by string just to obtain an I/O-capable `*client.File`
  — one real `Twalk` round-trip saved per `Open`, and for `Create`,
  one entire extra walk-for-metadata avoided outright (a plain fid
  clone plus one `Tcreate`, not walk-create-walk).
- Bumped `github.com/sandgorgon/tui` to v0.1.10, which fixes
  [sandgorgon/tui#3](https://github.com/sandgorgon/tui/issues/3) — the
  reconciler now preserves a keyed subtree's retained state (a live
  Terminal's pty included) even when it moves to a new parent across
  frames, so splitting a pane that hosts a running shell no longer
  kills and restarts it.
- Bumped `github.com/sandgorgon/9p` to v0.6.0 (adds a `-net` flag to
  `cmd/9pc`; no change to anything 9sh imports).
- Bumped `github.com/sandgorgon/tui` further, to v0.1.12, picking up
  `App.FocusIndex()`/`SetFocus(int)`
  ([sandgorgon/tui#5](https://github.com/sandgorgon/tui/issues/5)) and
  `Run()` honoring an `Update`-triggered `tui.FocusMsg`/`SetFocusCmd`
  ([sandgorgon/tui#7](https://github.com/sandgorgon/tui/issues/7)) —
  together, the API the F1-F9 pane-jump feature above is built on.
- Bumped `github.com/sandgorgon/tui` to v0.1.13, fixing the resize
  ghosting noted above.

## [0.1.0] - 2026-08-29

The v1 build-order plan (Phases 0-6) is complete: a runnable shell with its
own language, job control, per-process namespaces, remote namespace/auth
bridge, and dotfiles sync — see the [README](README.md#status) for what
"complete" does and doesn't mean here.

### Added

- **kyu**: lexer, parser, tagged-union value model (`Null`/`Bool`/`Int`/
  `Float`/`String`/`Bytes`/`Path`/`Duration`/`Ref`/`Record`/`List`), and a
  tree-walking evaluator. Structured `|` pipes with `where`/`select`/
  `sort_by`/`group_by`/`each`/`take`/`first`/`count`; closures; `%cmd` for
  external/legacy binaries; in-stream errors (`ErrorVal`, falsy by default)
  with `?` for abort-on-first-error.
- **`/jobs`**: a synthetic namespace exposing job control as files
  (`clone`, `ctl`, `status`, `events`, `wait`, `argv`, `env`, `stdin`,
  `stdout`, `stderr`) over a real `github.com/sandgorgon/9p` server —
  native-inproc and native-subprocess job kinds, growable multi-reader
  output streams, `Tflush`-cancelable `wait`.
- **Namespaces**: a pure-bind namespace tree (no FUSE, no OS mount table),
  built by `ns.Namespace` — union directories with Plan-9's before/after/
  replace disposition. kyu gains `bind SRC, DST[, before|after|replace]`
  and `a + b` namespace unions. `%cmd args &` backgrounds a job into a
  live record whose fields (`status`, `wait`, `ctl`, `argv`, `env`) are
  namespace-file reads/writes, not a snapshot. `checkout(path, closure)`
  materializes a namespace subtree to a real scratch directory for tools
  that need a real seekable path (editors, compilers), writing back
  whatever changed.
  Every command — foreground or backgrounded — is tracked as a job, so
  session history (below) sees ordinary `%cmd` use, not just `&`.
- **TUI pane multiplexer** (`9sh`, no flags, in a real terminal): a
  title-bar-per-pane layout hosting real PTYs (`tui`'s embedded VT100/
  xterm emulator) alongside two native, namespace-aware pane kinds — a
  kyu REPL pane and a namespace browser. Minimize/restore keeps a pane's
  process alive. Falls back to a line-based REPL on non-terminal stdin,
  or a script argument for headless runs; all entry points share one
  evaluation environment.
- **Session history** (`~/.config/9/session`): every job's terminal
  status is appended to a day-sharded, 9vcs-backed log — one branch per
  host, checkpointed on idle/timeout/exit. Requires the `9vcs` binary on
  `PATH`; degrades gracefully (prints a warning, shell still starts)
  without it.
- **Remote namespace + auth bridge** (`remote`, `dial`, `@host { ... }`):
  mutual TLS built on [`github.com/sandgorgon/9auth`](https://github.com/sandgorgon/9auth) —
  one per-install Ed25519 identity, known-peers/TOFU trust on connect,
  a global `authorized-peers` allowlist gating every attach at the TLS
  handshake itself. `dial(addr)` connects and returns a mountable handle;
  `bind h, /n/host` grafts a remote peer's namespace in exactly like any
  local backend. `@host { %cmd & }` runs a block's job creation against
  the bound remote's own `/jobs` — proxy jobs require no separate
  protocol, they fall directly out of the namespace bind.
- **Dotfiles/namespace-recipe sync** (`~/.config/9/ns`): `common.ky`, then
  `hosts/<hostname>.ky`, run as ordinary kyu against the shared session
  environment at startup — bind rules, aliases, and env defaults land as
  if typed at the REPL. A broken file is reported and skipped, never
  fatal to starting the shell. Sync itself (`9vcs sync`/`clone`/`offer`)
  is always a manual, user-triggered action against the plain `9vcs`
  CLI — this repo never shells out to it.
- `-version`, printing the build-time version — matching 9vcs's own
  `-X main.version=...` release convention.

### Fixed

Found by actually driving a real build interactively (a pty-scripted
driver first, then a human at the keyboard) rather than only the
headless `tui.App` test harness — the pane multiplexer, 9sh's actual
default entry point in a real terminal, had never been touched by
either before this release:

- Initial keyboard focus landed on the control strip's first button,
  not the kyu-repl pane's content — typing immediately after launch
  went nowhere until several Tabs/a click.
- The control strip and every pane's title bar rendered as blank rows
  (`tui.Focusable`'s mandatory 1-cell border left zero room for a
  `layout.Length(1)` row's own content).
- The kyu-repl pane never drew a cursor at all — `tui.App` unconditionally
  hides the real terminal cursor; every focus-aware text-entry widget is
  expected to draw its own.
- The control strip's background only extended to "quit", not the full
  pane width.

The control strip also now has its own background color, distinct
from a pane's title bar.
