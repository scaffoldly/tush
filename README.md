# tush: **TU**nnel **SH**ell

[![CI](https://github.com/scaffoldly/tush/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/scaffoldly/tush/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/scaffoldly/tush/badge)](https://scorecard.dev/viewer/?uri=github.com/scaffoldly/tush)
[![Release](https://img.shields.io/github/v/release/scaffoldly/tush)](https://github.com/scaffoldly/tush/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

`tush` publishes a shell over a tunnel and hands you back a URL. Anyone who
opens that URL — with `tush`, or in a browser — gets an interactive terminal on
the machine that published it: your own shell, your own configuration, job
control and all.

The session belongs to the machine, not to the connection. Close your laptop,
lose your wifi, or detach on purpose, and the shell keeps running; reattach and
you land back where you were, with what it printed while you were gone.

## Features

- 🐚 **Your shell, not an emulation** — runs `$SHELL` on a real pseudo-terminal, so your prompt, aliases, completions, history and colours are the ones you already have.
- 🌍 **Nothing to install on the far end** — open the URL in a browser and you get the same terminal. Whoever you sent it to needs no binary, no Go, no package manager.
- 🔌 **Detach and come back** — the shell outlives its clients. Reattach and the recent screen is replayed, so you arrive somewhere recognisable instead of a blank terminal.
- ⌨️ **A real terminal** — job control works: Ctrl+C interrupts, Ctrl+Z suspends, `fg` resumes. `vim`, `top` and other full-screen programs draw correctly and follow your window size.
- 🌐 **No inbound ports** — the tunnel is outbound-only, so it works from behind NAT, on hotel wifi, or inside a container.
- 🎲 **Unguessable URLs** — hostnames are opaque rather than memorable, so the address is not something anyone stumbles onto.
- 🤝 **A protocol, not a bespoke wire format** — speaks the same remote command protocol kubelet serves and `kubectl attach` talks to.
- 🪶 **One static binary** — pure Go, no cgo, no runtime to install.

## Getting Started

### Install

**Homebrew** (macOS and Linux):

```sh
brew tap scaffoldly/tap
brew install tush
```

**npx** — no install at all:

```sh
npx @scaffoldly/tush
```

Node is the only thing needed, and it is only a launcher: npm fetches the
prebuilt binary for your platform and runs it. Nothing is compiled, and no
install script downloads anything.

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

or open that URL in a browser.

Anyone with that URL gets a shell as you.
Press Ctrl+C to stop the tunnel.
```

You are now on a shell on the first machine. Press **Ctrl+P Ctrl+Q** to detach
and leave it running, or `exit` to end it — which also stops the tunnel.

### From a browser

Opening the URL gives a page with a terminal on it and nothing connected. It
stays that way until you press **Connect**: fetching the page must be inert,
because plenty of things fetch URLs without a person involved.

**Ctrl+P Ctrl+Q** detaches, the same as it does from the terminal client, and
leaves you on the card with an Attach button. Closing the tab detaches too, and
so does the **Detach** button in the corner.

Beside it is **Stop**, which ends the session and the tunnel for everyone — the
URL does not come back. It takes two presses. It is not a power the URL did not
already carry: anyone who can attach can type `exit`, which does the same
thing. The button only makes it obvious.

One client at a time, whichever kind, and the newest one wins. A browser tab and
a `tush` client contend for the same shell; whoever arrives last gets it, and
whoever had it is told so and can attach again. Nobody is turned away — losing
the shell this way costs you nothing but the trip back, and anyone who could
take it could equally have typed `exit`.

`tush version` and `tush help` do what they look like.

## How it works

The host opens a pseudo-terminal, runs your shell on it, and serves it over the
Kubernetes remote command protocol — the one kubelet exposes and `kubectl
attach` speaks. Clients _attach_ to a shell that is already running rather than
starting one, which is what lets a session outlive the connection that created
it.

A browser speaks that protocol natively, so the page served at the same URL is
another client rather than another interface: the same frames, the same
channels, a terminal emulator instead of yours.

For the packages, the invariants, and the things that bite, see
[CONTRIBUTING.md](./CONTRIBUTING.md).

## Security

**There is no authentication. The URL is the credential.** Anyone who has it
gets a shell as the user who published it, so treat it like a password: share
it deliberately, and end the session when you are done. Gating access belongs
to the tunnel provider rather than to this binary — see
[CONTRIBUTING.md](./CONTRIBUTING.md#no-authentication-by-design) for why.

Because the URL now yields a shell to a browser as well, it is worth being
blunt about what that means: pasting it somewhere is closer to pasting a
password than to pasting a link, and a chat client that previews links or a
history that syncs between devices carries it further than you intended.

What the page does about it: it attaches nothing until you press Attach, so
fetching it — by an unfurler, a crawler, a preview — opens no session, and
ending the session takes a `POST`, so nothing that merely follows a link can do
it either. It
carries no link preview metadata worth showing, asks not to be indexed, is
served `no-store` so it does not sit in a shared cache, and sends no referrer,
so the URL is not handed to anything the page loads. None of that is
authentication; it only stops the URL escaping by accident.

The terminal emulator is loaded from a CDN and pinned by hash, so a CDN that
served something other than what was published cannot put script on a page that
grants a shell.

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

## Contributing

[CONTRIBUTING.md](./CONTRIBUTING.md) covers the layout, the dev loop, and how a
change reaches a release. It is written for humans and AI agents alike; agents
should start at [CLAUDE.md](./CLAUDE.md).

## License

[MIT](./LICENSE)
