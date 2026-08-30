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

## Status

Pre-1.0 (`v0.1.0`). The full v1 build-order plan (namespace core, jobs,
kyu, the TUI pane multiplexer, session history, remote namespace/auth,
dotfiles sync) is implemented and covered by real tests — real 9P
traffic over Unix sockets and TCP, real subprocess execution, real
mutual-TLS handshakes between distinct identities, `-race` clean
throughout, and every phase additionally exercised through the actual
built binary, not just `go test`.

The pane multiplexer has now had real hands-on use, not just headless
`tui.App` tests — several real bugs (invisible keyboard focus on
launch, blank control-strip/title-bar chrome, an invisible cursor) were
found and fixed this way, not caught by any automated test. Two more
namespace-aware panes (a job viewer, a session-history viewer) have
landed since. Treat 9sh as ready for hands-on testing, not as a
finished daily driver — the kyu language in particular is young enough
that rough edges are still expected.

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
