# tush: **TU**nnel **SH**ell

[![CI](https://github.com/scaffoldly/tush/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/scaffoldly/tush/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/scaffoldly/tush/badge)](https://scorecard.dev/viewer/?uri=github.com/scaffoldly/tush)
[![Release](https://img.shields.io/github/v/release/scaffoldly/tush)](https://github.com/scaffoldly/tush/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

`tush` publishes a shell over a tunnel and hands you back a URL. Anyone who
opens that URL with `tush` gets an interactive terminal on the machine that
published it — your own shell, your own configuration, job control and all.

The session belongs to the machine, not to the connection. Close your laptop,
lose your wifi, or detach on purpose, and the shell keeps running; reattach and
you land back where you were, with what it printed while you were gone.

## Features

- 🐚 **Your shell, not an emulation** — runs `$SHELL` on a real pseudo-terminal, so your prompt, aliases, completions, history and colours are the ones you already have.
- 🔌 **Detach and come back** — the shell outlives its clients. Reattach and the recent screen is replayed, so you arrive somewhere recognisable instead of a blank terminal.
- ⌨️ **A real terminal** — job control works: Ctrl+C interrupts, Ctrl+Z suspends, `fg` resumes. `vim`, `top` and other full-screen programs draw correctly and follow your window size.
- 🌐 **No inbound ports** — the tunnel is outbound-only, so it works from behind NAT, on hotel wifi, or inside a container.
- 🎲 **Unguessable URLs** — hostnames are opaque rather than memorable, so the address is not something anyone stumbles onto.
- 🤝 **A protocol, not a bespoke wire format** — speaks the same remote command protocol kubelet serves and `kubectl attach` talks to.
- 🪶 **One static binary** — pure Go, no cgo, nothing to install on the far end.

## Getting Started

### Install

**Homebrew** (macOS and Linux):

```sh
brew tap scaffoldly/tap
brew install tush
```

**Go**:

```sh
go install github.com/scaffoldly/tush@latest
```

**Binaries** — signed archives for macOS and Linux are attached to every
[release](https://github.com/scaffoldly/tush/releases).

### Use

Publish a shell:

```sh
tush
```

It waits for the tunnel to start routing, then prints the command to run on
any other machine:

```
Attach from another machine with:

    tush https://<hostname>.tunneled.pizza/

Anyone with that URL gets a shell as you.
Press Ctrl+C to stop the tunnel.
```

You are now on a shell on the first machine. Press **Ctrl+P Ctrl+Q** to detach
and leave it running, or `exit` to end it — which also stops the tunnel.

`tush version` and `tush help` do what they look like.

## How it works

The host opens a pseudo-terminal, runs your shell on it, and serves it over the
Kubernetes remote command protocol — the one kubelet exposes and `kubectl
attach` speaks. Clients _attach_ to a shell that is already running rather than
starting one, which is what lets a session outlive the connection that created
it.

For the packages, the invariants, and the things that bite, see
[CONTRIBUTING.md](./CONTRIBUTING.md).

## Security

**There is no authentication. The URL is the credential.** Anyone who has it
gets a shell as the user who published it, so treat it like a password: share
it deliberately, and end the session when you are done. Gating access belongs
to the tunnel provider rather than to this binary — see
[CONTRIBUTING.md](./CONTRIBUTING.md#no-authentication-by-design) for why.

Release archives are signed with keyless cosign and carry SLSA build
provenance; verification steps are in
[CONTRIBUTING.md](./CONTRIBUTING.md#releases).

## Platforms

macOS and Linux, on x86-64 and ARM. Windows is not supported: the host needs a
pseudo-terminal and a controlling terminal to give the shell job control, and
Windows has no equivalent of either.

## Roadmap

Tracked as [issues](https://github.com/scaffoldly/tush/issues). The larger
ones:

- [#2](https://github.com/scaffoldly/tush/issues/2) — `tush [command]`, so `tush k9s` tunnels k9s rather than only ever a shell.
- [#3](https://github.com/scaffoldly/tush/issues/3) — attach from a browser at the tunnel URL, with nothing to install.

## Contributing

[CONTRIBUTING.md](./CONTRIBUTING.md) covers the layout, the dev loop, and how a
change reaches a release. It is written for humans and AI agents alike; agents
should start at [CLAUDE.md](./CLAUDE.md).

## License

[MIT](./LICENSE)
