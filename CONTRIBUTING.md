# Contributing

This document is for everyone working on `tush` — humans and AI agents alike.
It covers the layout, the local dev loop, the conventions that bite, and how a
change gets from an issue to a release.

What `tush` is and how to use it lives in [README.md](./README.md); this
document does not repeat it.

## Where to find things

Deep-link by filename; line numbers will drift.

| Topic                                     | Source                                                                     |
| ----------------------------------------- | -------------------------------------------------------------------------- |
| CLI entrypoint, host/client dispatch      | [`main.go`](./main.go)                                                     |
| Pseudo-terminal, scrollback, attach/detach | [`console/console.go`](./console/console.go)                              |
| Remote command protocol endpoint          | [`attach/attach.go`](./attach/attach.go)                                   |
| Running the user's shell on the terminal  | [`shell/shell.go`](./shell/shell.go)                                       |
| Session leader + controlling terminal     | [`shell/procattr_unix.go`](./shell/procattr_unix.go)                       |
| Terminal client (raw mode, detach, resize) | [`client/client.go`](./client/client.go)                                   |
| Window-change signal                      | [`client/winsize_unix.go`](./client/winsize_unix.go)                       |
| Tunnel publication                        | [`tunnel/tunnel.go`](./tunnel/tunnel.go)                                   |
| Argument queue                            | [`queue/queue.go`](./queue/queue.go)                                       |
| Live tests against a real tunnel          | [`e2e/e2e_test.go`](./e2e/e2e_test.go)                                     |
| Build / lint / test commands              | [`Makefile`](./Makefile)                                                   |
| CI matrix                                 | [`.github/workflows/ci.yml`](./.github/workflows/ci.yml)                   |
| Release (build + sign + publish)          | [`.github/workflows/release.yaml`](./.github/workflows/release.yaml)       |
| OpenSSF Scorecard scan                    | [`.github/workflows/scorecard.yml`](./.github/workflows/scorecard.yml)     |
| Homebrew formula template                 | [scaffoldly/homebrew-tap](https://github.com/scaffoldly/homebrew-tap)      |

## Architecture

A session has two halves that never share a process.

**The host** (`tush` with no arguments) opens a pseudo-terminal
([`console`](./console/console.go)), runs the user's shell on it
([`shell`](./shell/shell.go)), and serves that terminal over the Kubernetes
remote command protocol ([`attach`](./attach/attach.go)) on a tunnel listener
([`tunnel`](./tunnel/tunnel.go)).

**The client** (`tush <url>`) is a dumb terminal
([`client`](./client/client.go)). It runs no interpreter: it puts the local
terminal in raw mode, forwards keystrokes, and paints what comes back.

Three decisions explain most of the code:

1. **Attach, not exec.** The shell is started once and clients attach to what
   is already running. That is what lets a session outlive its connection —
   `ServeExec` would start a fresh shell per connection, which is the opposite
   of the point.

2. **A real terminal, not a pipe.** The shell runs on a pty and becomes a
   session leader with that pty as its controlling terminal
   ([`procattr_unix.go`](./shell/procattr_unix.go)). That is the whole reason
   job control works; without it the kernel has no foreground process group to
   deliver Ctrl+C to.

3. **The protocol carries the multiplexing.** Standard input, output and window
   resizes share one connection, each message tagged with its channel. tush
   invents no framing of its own, and gets the client's real window size for
   free.

## Local development

```sh
make            # fmt-check, vet, build, test, e2e — everything CI runs
make check      # the same, minus the build
make test       # unit tests
make binary     # a stamped ./tush to run by hand
make dist       # cross-compiled release artifacts + SHA256SUMS
```

`make test` needs a working tty and a shell, but no network: the `console` and
`client` tests open real pseudo-terminals and run `$SHELL`.

`make e2e` publishes real tunnels and is skipped unless `TUSH_E2E` is set:

```sh
TUSH_E2E=1 make e2e
```

One of those cases sits idle for three minutes on purpose, to prove an idle
session is not dropped. `TUSH_E2E_IDLE` overrides how long.

## Conventions that bite

These are the ones that have cost real time. Each is load-bearing.

### Terminal echo makes tests lie

The line discipline echoes typed input back on the same stream the output
arrives on. A test that types `echo marker` and waits for `marker` therefore
passes on the **echo alone**, whether or not the shell ever ran. This has
produced false positives here more than once.

Write markers so the echo and the output differ, using quoting that vanishes on
execution:

```go
in.typeLine("echo he''llo")   // echoes as he''llo, prints hello
screen.waitFor(t, "hello")
```

Put the quotes in the literal text, never inside a variable reference:
`echo fir''st:$FOO` works; `echo first:$F''OO` expands `$F` and breaks.

### Protocol parameters are `"1"`, not `"true"`

`remotecommand.NewOptions` compares against the literal string `"1"`. Sending
`"true"` yields a 400 whose body reads *"you must specify at least 1 of stdin,
stdout, stderr"* — which looks like a missing parameter rather than a wrong
value.

With `tty=1` the server disables the stderr channel by design, so everything
arrives on stdout.

### The scrollback resumes at a line, not a byte

Dropping the oldest output by byte count cuts wherever it lands, including the
middle of an escape sequence — which then prints as text, or worse leaves the
terminal in a mode whose closing bytes were discarded. `console.remember` trims
at a line boundary because escape sequences do not span lines. Keep it that
way.

### One client at a time, and output is never held

The console has a single reader fanning output out to whichever client is
attached; a second concurrent attach gets `ErrBusy`. Output produced while
nobody is attached is **dropped**, not buffered — otherwise a shell left
running with no client would fill the terminal's buffer and block on its own
output. The scrollback is what recovers it on reattach.

### The client keeps the connection warm

A terminal sends nothing for minutes at a time and intermediaries drop
connections they believe are idle — Cloudflare's edge after about a hundred
seconds. The client sends an empty frame every thirty seconds. A WebSocket ping
would be the natural thing, but the server side speaks
`golang.org/x/net/websocket`, which has no ping and would never answer one.

### A freshly minted tunnel is not immediately routable

The edge answers on its own (530, 1033) until the hostname propagates. The e2e
harness polls the attach endpoint until a 400 comes back — only this process
refuses a request that selects no streams, so anything else is still the edge.
Do not replace that with a sleep.

### No authentication, by design

The URL is the capability. Anyone holding it gets a shell as the publishing
user. Gating access belongs to the tunnel provider, not to this binary —
building it in here would duplicate what that layer owns. Treat its absence as
a boundary, not a defect, and raise a design change rather than adding auth
alongside the endpoint.

## Testing

Prove behavior end to end rather than asserting on internals: the suites drive
real pseudo-terminals, real shells and (for `e2e`) real tunnels. `client`'s
terminal-backed tests allocate a pty for the *client* too, which is the only
way to reach raw mode, the detach sequence and window-size reporting — none of
which run against a pipe.

When a test passes on the first try in this repo, check that it can fail. More
than one green test here has been measuring the echo rather than the shell.

## Releases

Pushing to `main` releases. The workflow bumps the patch version from the
latest tag, creates a draft, cross-compiles for macOS and Linux, and signs each
archive with keyless cosign plus SLSA build provenance. A commit whose message
contains `[skip release]` alone on a line is not released; a manual `vX.Y.Z`
tag releases as itself, for minor and major bumps.

Verify an archive:

```sh
cosign verify-blob --bundle tush_darwin_arm64.zip.sigstore \
  --certificate-identity-regexp '^https://github.com/scaffoldly/tush/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  tush_darwin_arm64.zip
```

The Homebrew formula updates itself: [the
tap](https://github.com/scaffoldly/homebrew-tap) polls this repo's latest
release hourly and re-renders `Formula/tush.rb`. Nothing here pushes to it.

## Sending a change

- Keep the working tree `gofmt` clean; CI fails on anything else.
- Explain **why** in comments and commit messages. What the code does is
  visible; the reason it does it that way is not, and most of the subtleties
  above were invisible until something broke.
- Add a test that would have caught the bug, and confirm it fails without the
  fix.
- CI runs on macOS and Linux across x86-64 and ARM. The live tier runs on one
  cell, so a green run does not mean the tunnel path was exercised everywhere.
