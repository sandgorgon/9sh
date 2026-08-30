# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project intends to follow [Semantic Versioning](https://semver.org/)
once a first tagged release is cut.

## [Unreleased]

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
