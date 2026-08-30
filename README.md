# 9sh

[![CI](https://github.com/sandgorgon/9sh/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/sandgorgon/9sh/actions/workflows/ci.yml?query=branch%3Amaster)

A Plan-9-flavored Linux login shell: per-process namespaces, structured
pipes, and a small scripting language called **kyu**, built on top of
[`9p`](https://github.com/sandgorgon/9p), [`9vcs`](https://github.com/sandgorgon/9vcs),
[`9auth`](https://github.com/sandgorgon/9auth), and
[`tui`](https://github.com/sandgorgon/tui). It's meant to be the actual
shell you work in day to day, not a compositor that just hosts `bash` in
a pane.

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

Run it with no arguments in a real terminal to get the pane multiplexer
(the default, primary way to use it); pipe something into stdin or pass
`-repl` for a plain line-based REPL; pass a script path to run headlessly.

## Quick start

```
9sh> bind /local, /work
9sh> %ls "-la" "/work" | %grep "go"
9sh> j := %sleep "5" &
9sh> j.status.state
"running"
9sh> j | wait
```

- `bind SRC, DST[, before|after|replace]` grafts something onto the
  namespace — a local directory, a job-control tree, a dialed remote
  peer's whole namespace, all the same mechanism.
- `%cmd` calls out to an ordinary Linux binary; a `%cmd ... &` job is a
  live record — `j.status`, `j.ctl = "stop"`, `j | wait` all read/write
  through to real namespace files, not a snapshot.
- `|` is a structured pipe by default (`where`/`select`/`sort_by`/
  `group_by`/`each`/...), not raw bytes — `%` is the sigil that marks
  "this call is bytes, not structured data."

Run a job on another 9sh, over mutual TLS, with no separate remote-job
protocol:

```
9sh> h := dial("otherhost:2049")
9sh> bind h, /n/otherhost
9sh> @otherhost { %sh "-c" "echo hi" & } | wait
```

`@host { ... }` re-roots job creation at the bound remote's own `/jobs`
for the block — a "proxy job" is nothing more than that.

Drop `common.ky` / `hosts/<hostname>.ky` under `~/.config/9/ns` and 9sh
runs them at startup, against the same environment, for persistent bind
rules/aliases/env defaults — see [Design](#design).

## Using the pane multiplexer

Run `9sh` with no arguments in a real terminal and you land in the pane
multiplexer — 9sh's primary, default way to work, not a fallback. It
starts with one kyu REPL pane; everything else is built up from there.
The same reference is built into 9sh itself: click **help** in the
control strip (or Tab/Shift-Tab to it and press Enter) any time.

### The control strip

The always-visible top row:

| Button | Does |
|---|---|
| `+ shell` / `+ kyu` / `+ browse` / `+ jobs` / `+ history` | Add a pane of that kind |
| `help` | Open the built-in keybinding reference |
| `theme` | Flip light/dark, live, no restart |
| `quit` | Quit 9sh |

`+` always splits the last pane in document order (see below), not
"whatever's focused" — `tui`, the TUI toolkit 9sh is built on, doesn't
give application code a way to ask "what currently has focus," only to
set it, so this is a deliberate, deterministic choice, not a
limitation you're expected to work around.

### Every pane's title bar

| Key | Does |
|---|---|
| `x` | Close this pane |
| `d` / `r` | Split down / right — then pick the new sibling's kind: `s`=shell, `k`=kyu, `b`=browse, `j`=jobs, `h`=history (anything else cancels) |
| `z` | Zoom this pane to fill the whole content area, or un-zoom it back — every other pane's process keeps running the whole time, just out of view |
| `+` / `-` | Resize along the split axis, down to one visible content line — smaller than that, minimize instead |
| click / Enter | Minimize/restore (only along a vertical split — collapsing a horizontal sibling's *width* to one column would garble its title sideways, so those can't minimize; the chevron drops accordingly) |
| `F1`-`F9` | Jump keyboard focus straight to pane 1-9 (each title bar shows its own `[F#]` once assigned) |

Repeated `+` clicks and `d`/`r` splits both build a genuine 2D tiling
tree (alternating direction on `+`, your choice on `d`/`r`) — there's
no single-axis "everything stacks one way" limitation.

### Inside a kyu REPL pane

Once the pane's *content* has focus (not just its title bar — Tab or
click into it):

| Key | Does |
|---|---|
| `Enter` | Submit, or keep editing if brackets are still open |
| `←`/`→`, Ctrl+`←`/`→` | Move the cursor by character / by word |
| `Home`/`End`, Ctrl+A/Ctrl+E | Jump to the start/end of the current line |
| `Backspace`/`Delete` | Delete before/after the cursor |
| Ctrl+W | Delete the word before the cursor |
| Ctrl+U / Ctrl+K | Delete to line start / delete to line end |
| `↑`/`↓` | Recall previous/next submitted input (only outside a multi-line continuation) |
| `PageUp`/`PageDown`, mouse wheel | Scroll the transcript, independent of the input line |
| Ctrl+C | Copy the whole transcript |
| Alt+C | Copy only what's currently visible on screen |
| paste | Inserts at the cursor |

Ctrl+C is "copy all," not the Ctrl+Shift+C you might expect from a
desktop terminal: most terminal emulators (this one's own standing
test target, gnome-terminal/VTE, included) send the identical byte for
Ctrl+C and Ctrl+Shift+C on a plain letter key — only a kitty-keyboard-
protocol-aware terminal can tell them apart, which isn't something to
assume. Alt+C for "just the visible part" sidesteps that ambiguity
entirely.

No undo/redo, and no multi-line-aware history recall (Up/Down inside
an open multi-line continuation navigate lines, not history) —
deliberate scope cuts for a REPL input line, not a general text
editor.

### Inside a shell pane

Once the pane's content has focus, Tab reaches the hosted shell
directly — real completion, not pane navigation. **Ctrl+\\** releases
focus back to navigating panes/title bars/the control strip.

### Mouse

Click a pane's content to focus it; click a title bar to minimize/
restore it; click any control-strip or title-bar button the same way
you'd press its key. Mouse wheel scrolls a kyu REPL pane's transcript.

## Status

Pre-1.0 (`v0.2.0`). The full v1 build-order plan (namespace core, jobs,
kyu, the TUI pane multiplexer, session history, remote namespace/auth,
dotfiles sync) is implemented and covered by real tests — real 9P
traffic over Unix sockets and TCP, real subprocess execution, real
mutual-TLS handshakes between distinct identities, `-race` clean
throughout, and every phase additionally exercised through the actual
built binary, not just `go test`.

The pane multiplexer has had substantial real hands-on use, not just
headless `tui.App` tests — several real bugs (invisible keyboard focus
on launch, blank control-strip/title-bar chrome, an invisible cursor,
stale content surviving a window resize, a resize floor with no room
to actually shrink) were found and fixed this way, not caught by any
automated test. Pane management has grown well past the original v1
scope: a real 2D tiling tree (not a single-axis stack), maximize/zoom,
per-pane box-drawing frames, a runtime theme toggle, and a full kyu
REPL line editor (cursor movement, history recall, kill commands,
paste, independent scrolling, clipboard copy) are all in place, plus a
built-in help screen — see [Using the pane multiplexer](#using-the-pane-multiplexer).
Two more namespace-aware panes (a job viewer, a session-history viewer)
round out the design doc's original differentiator list.

Getting close to usable as an actual daily driver, not just ready for
hands-on testing — but the kyu language itself is still young enough
that rough edges are expected there. Proxy-job (`@host{}`) session
recording now has a local-side linking record (host, remote job id,
argv, exit/signal, alongside the ordinary entry the remote peer's own
session repo already logged for it); the remote-namespace ACL model
gained a `propose` permission tier (enough to write/create, short of
remove/wstat) and `ListenWithRootPerms`, scoping a distinct
authorized-peers file to one exported root instead of only the single
global allowlist `Listen` alone still uses.

## Design

- **No FUSE.** Namespace transparency is userspace-only, through three
  interop tiers: anything linking `9p`'s client gets full transparency
  for free; classic Unix pipeline tools need nothing beyond stdin/
  stdout; tools needing a real seekable path use `checkout` to
  materialize a subtree to a scratch directory and write back on close.
- **Structured pipes.** Records and tables flow through `|` by default
  (nushell/PowerShell-style); `%` marks a call into legacy/external
  Bytes-land.
- **One identity, one trust decision.** `9auth`'s per-install Ed25519
  identity and TOFU peer trust cover 9vcs sync, remote namespace mounts,
  and proxy jobs alike — mutual TLS authenticates once at the transport
  layer, so `Tattach`'s `uname` is never a client-asserted string.
- Package doc comments throughout (`ns`, `job`, `kyu/eval`, `remote`,
  `session`, `dotfiles`, `pane`) go into the "why," not just the "what,"
  for anyone picking a subsystem apart.

## Testing

```
go build ./...
go vet ./...
go test -race ./...
```

## License

MIT — see [`LICENSE`](LICENSE).
