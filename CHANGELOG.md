# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project intends to follow [Semantic Versioning](https://semver.org/)
once a first tagged release is cut.

## [Unreleased]

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
