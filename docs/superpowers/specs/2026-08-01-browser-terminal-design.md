# Serve a browser terminal at the tunnel URL

Design for [issue #3](https://github.com/scaffoldly/tush/issues/3).

## Problem

Attaching to a published shell needs `tush <url>` installed on the far end.
Opening the same URL in a browser should give a working terminal instead, with
nothing to install.

The wire format already allows it. The attach endpoint speaks
`v4.channel.k8s.io`: binary frames, each prefixed with a channel byte. A
browser speaks that natively — `WebSocket` with a subprotocol, `ArrayBuffer`
frames — so no server-side protocol change is needed. What is missing is a page
to serve at `/` and a terminal emulator on it.

## Decisions

### xterm.js, from a CDN, pinned by subresource integrity

The emulator is [xterm.js](https://xtermjs.org) — `@xterm/xterm` 6.0.0 and
`@xterm/addon-fit` 0.11.0, both MIT. It is what VS Code, ttyd, GoTTY, Wetty and
code-server all use, and the only mature option; `hterm` is the other, and is
effectively ChromeOS-only now.

`@xterm/addon-attach` exists and wires a WebSocket straight into a terminal, but
it assumes the socket carries raw terminal bytes. Ours carries channel-prefixed
frames, so the addon cannot be used and the glue is written by hand instead —
roughly what `client/client.go` already does, in about eighty lines.

The library is loaded from `cdn.jsdelivr.net` at an **exact** version, with a
**`sha384` integrity attribute and `crossorigin="anonymous"` on every tag**.
The browser hashes what it receives and refuses to execute or apply anything
that does not match, so a compromised or coerced CDN cannot inject script into
a page that grants a shell. `crossorigin` is not optional: without it the
browser cannot read a cross-origin response well enough to check it, and
silently skips enforcement.

The hashes are of these exact bytes, verified as npm's published artifacts by
confirming that jsdelivr and unpkg serve them byte-identically:

| Asset | Bytes | `integrity` |
| --- | --- | --- |
| `@xterm/xterm@6.0.0/lib/xterm.js` | 488663 | `sha384-f/1U6Z9wM4D71a5eRXEZnyOTMOvjqxr2XLwh+Go1OvIl3L3tOcvUrzudnhbECwl4` |
| `@xterm/xterm@6.0.0/css/xterm.css` | 7112 | `sha384-n2n7twoohnW+d3myBKaUgl7DSiwidw6MkQy9oesGzkPpMjejKRR3XlnD+5yCdtBD` |
| `@xterm/addon-fit@0.11.0/lib/addon-fit.js` | 1521 | `sha384-txoiwu4RR2GD3qySbaj+BbzibkLbSJRcfqGYMu6z1EqHil4A2dyBiBW5dlacG6OR` |

The version must be pinned exactly rather than to a range: a floating specifier
would serve different bytes on the next publish and fail the integrity check,
taking the page down.

**UMD, not ESM.** `integrity` is only honoured on classic `<script src>` and
`<link rel=stylesheet>` tags. An ES module `import` specifier carries no
integrity at all, and the import-map `integrity` field that would supply one is
Chrome-127-and-later only. Since the requirement is a verified hash, the UMD
builds are the only correct choice; they expose `Terminal` and `FitAddon` as
globals. The extra 144 KB over the ESM build costs nothing here, because none
of it lands in the binary.

**What the CDN still costs.** A network that blocks `cdn.jsdelivr.net`, or a
jsdelivr outage, means no terminal. This is a real regression against embedding
and is accepted deliberately: the CLI path is unaffected, and the page says so
plainly when the library fails to load rather than showing a blank screen.

Only `tush`'s own glue and page are embedded, so the binary grows by a few
kilobytes rather than 350.

**Not React, and no `go:generate` build step.** The decisive reason is that
`go install github.com/scaffoldly/tush@latest` is a documented install path,
and `go:generate` does not run during `go build` or `go install`. Build output
would therefore have to be committed regardless, so the generate step buys
nothing at install time while adding a Node toolchain requirement for CI and
for contributors, plus a large committed diff on every asset bump. The page is
one `<div>`, one button and one WebSocket; a component framework earns nothing
here.

### The page does not attach until a human acts

`GET /` returns a static page. It opens no WebSocket and starts no shell. Only
pressing **Connect** does that.

This matters because a URL that yields a shell by being pasted into a browser
is a materially different exposure from one that needs a binary and a
deliberate command. Link unfurlers, chat previews and history sync all fetch
URLs without a human involved. None of them may open a session.

The property already holds on the server side — `attach.Server.shell()` starts
the shell lazily on the first attach rather than at boot — and this change must
not break it. It gets an explicit test rather than an assumption.

### One client at a time is unchanged

`console.Attach` returns `ErrBusy` when a client is already attached, and that
reaches the browser on the error channel. Evicting an incumbent would be a
semantic change and is out of scope.

## Architecture

### Layout

```
web/
  web.go            Handler(), the embed, the template, security headers
  assets.go         the CDN URLs and their integrity hashes, in one place
  web_test.go
  sri_test.go       the opt-in check of those hashes against the CDN
  index.html        the page, as an html/template
  assets/
    tush.js         the glue
    icon.svg        the page's icon
```

Nothing from xterm.js is vendored. `assets.go` holds each CDN URL beside its
`sha384` hash, and `index.html` renders them into the tags, so a version bump
is one edit in one file and cannot leave a URL and a hash out of step.

### Routes

The mux moves to `main.go`, which is the only place that knows about both
halves:

| Path           | Served by                                        |
| -------------- | ------------------------------------------------ |
| `/`            | `web.Handler()` — the page                       |
| `/assets/…`    | `web.Handler()` — the embedded files             |
| `/favicon.ico` | `web.Handler()` — the icon, explicitly           |
| `POST /stop`   | `web.Handler()` — ends the session; POST only    |
| `/attach`      | `attach.Server` — unchanged                      |

`attach.Server.Handler()` stops building its own mux and returns just the
endpoint handler. `main.go` routes. Go's `ServeMux` prefers the more specific
`/attach` pattern over the `/` catch-all, and `waitForEdge`'s 400 probe against
`/attach` is unaffected.

Anything under `/assets/` that is not a known file is a 404. Any other path
under `/` returns the page, since a browser landing on a stray path should
still get a terminal rather than a mystery error.

### The wire contract is rendered, not duplicated

The contract lives in Go constants: `attach.Path`, `ParamStdin`, `ParamStdout`,
`ParamTTY`, `ParamTerm`, the subprotocol string, and the channel bytes. If the
JavaScript hardcodes them they drift silently, and the failure is a terminal
that shows nothing with no error anywhere.

So `index.html` renders them into a non-executable block that `tush.js` reads:

```html
<script type="application/json" id="tush-config">
  {"endpoint":"/attach?input=1&output=1&tty=1&term=xterm-256color",
   "subprotocol":"v4.channel.k8s.io",
   "channels":{"stdin":0,"stdout":1,"stderr":2,"error":3,"resize":4},
   "keepAliveSeconds":30}
</script>
```

`application/json` rather than JavaScript so that no inline script executes and
the policy can stay at `script-src 'self'`. The browser resolves scheme and
host from `location`, so the endpoint is rendered as a path and query only.

This requires a small refactor. The channel constants currently sit unexported
in `client/client.go`. They move to `attach`, exported, next to the parameter
names — which already carry the comment *"kept here so the client and the
server agree without either importing the other."* `client` and `web` then both
read them from there.

### The glue

`web/assets/tush.js` mirrors `client/client.go` one for one:

| `client/client.go`                    | `tush.js`                                                   |
| ------------------------------------- | ----------------------------------------------------------- |
| `websocket.Dial` with the subprotocol | `new WebSocket(url, [subprotocol])`, `binaryType = "arraybuffer"` |
| `forward` → channel 0                 | `term.onData` → UTF-8 encode → send on channel 0             |
| `render` → `switch frame[0]`          | `new Uint8Array(e.data)`, `term.write(f.subarray(1))`        |
| `reportSize` → channel 4              | `FitAddon` + `term.onResize` → channel 4                     |
| `keepAlive`, 30 s empty frame         | `setInterval`, `ws.send(new Uint8Array(0))`                  |
| `exitStatus` on channel 3             | the same JSON parsed, shown on the overlay                   |

`term.onBinary` is also wired to channel 0: xterm emits it for the few inputs
that are not valid UTF-8 text, and dropping them would silently lose keys.

One deliberate difference from the CLI client: **`TERM=xterm-256color`**, which
is what xterm.js honestly implements, where the CLI forwards whatever the
user's terminal claims.

**Ctrl+P Ctrl+Q detaches**, as it does from the terminal client. This was
originally left out, on the reasoning that closing the tab already detaches and
that binding a chord in a browser fights the browser. That was wrong on both
counts: the muscle memory comes from the CLI and its absence reads as a bug,
and detaching to the card is better than closing the tab because reconnecting
is then one click.

`splitDetach` is ported to JavaScript rather than reimplemented, and the bytes
come from `attach.DetachPrefix`/`DetachSuffix` rather than being written out
again, so the same chord means the same thing in a tab and in a terminal. The
subtlety worth keeping is that a trailing Ctrl+P is *held* until the next key
decides what it meant — that is what leaves a lone Ctrl+P working as
history-previous. Ctrl+P is also the browser's print shortcut, so the page
suppresses that default while still letting the terminal have the key.

Keys the browser reserves — Ctrl+W, Ctrl+N, Cmd+W — cannot be delivered. That
is a limitation of the medium, not something to work around.

### The page

The terminal mounts and fits on load but stays blank. A centred card sits over
it carrying what connecting means and the button.

```
┌────────────────────────────────────────┐
│                                        │
│      ┌──────────────────────────┐      │
│      │  tush                    │      │
│      │                          │      │
│      │  A shell on someone's    │      │
│      │  machine. Anyone with    │      │
│      │  this URL gets it.       │      │
│      │                          │      │
│      │     [   Connect   ]      │      │
│      └──────────────────────────┘      │
│                                        │
└────────────────────────────────────────┘
```

Mounting the terminal before connecting rather than after means it is already
at its final size when the shell writes its opening prompt. Swapping the
layout at click time would race that first paint against a reflow.

The button says **Attach**, which is what the protocol calls it and what the
terminal client does, rather than Connect or Reconnect — one word for one act,
whether it is the first time or the fourth.

While attached, two buttons sit in the top-right corner over the terminal,
faded until pointed at, because the shell should keep the whole window and a
permanent pair of controls in the corner covers output. They are hidden when
nothing is attached: neither does anything then, and a Stop button on a page
nobody has connected from is an invitation to press it.

**Detach** leaves the shell running, the same as the chord. **Stop** ends the
session and the tunnel for everyone, and takes two presses — the first arms it,
and it disarms itself after four seconds. One click in the corner of a window
somebody is typing into is not enough for something with no way back.

Stop is not a capability the URL did not already carry: anyone who can attach
can type `exit`, which stops the tunnel identically. What matters is that it
cannot fire without a person. It is a `POST`, and that is the whole guard —
unfurlers, crawlers, prefetchers and syncing history issue GETs against URLs
nobody deliberately visited, and a `GET` route would hand a kill switch to all
of them. A `GET` falls through to the page instead.

The teardown reuses the Ctrl+C path rather than being a second one to keep
correct: the stop cancels the context, which kills the shell and closes the
server exactly as a signal does. The handler answers and flushes before
triggering it, since the teardown closes every connection including the one
carrying the reply.

The overlay returns, with the reason and an **Attach** button, when the socket
closes or the error channel reports the shell exited. After a Stop it returns
with no way back, because there is nothing left to attach to.

If xterm.js never loads — a blocked CDN, an outage, or an integrity mismatch —
the Attach button is disabled and the card says the terminal library could not
be loaded and that `tush <url>` still works. That is checked by testing for the
`Terminal` global once the page has loaded, which catches all three causes,
since a failed integrity check leaves the script unexecuted exactly as a failed
request does. The failure must never be a blank screen with a dead button.

A busy result is retried twice with a short backoff before it reaches the
overlay. A refreshed tab can genuinely lose a race against the server noticing
that the previous socket died, and the retry covers that window without
changing the one-client-at-a-time semantics.

## Security

The URL remains the only credential. **No authentication is added** — that is a
boundary owned by the tunnel provider, per
[CONTRIBUTING.md](../../../CONTRIBUTING.md#no-authentication-by-design). What
follows is hardening, and exists because a browser leaks URLs in ways a CLI
does not.

Response headers on `/`:

- **`Referrer-Policy: no-referrer`.** The URL is the capability. Any outbound
  request carrying a `Referer` header hands it to a third party.
- **`Cache-Control: no-store`.** A capability page must not sit in a shared
  cache.
- **`X-Robots-Tag: noindex, nofollow, noarchive`**, with the matching meta tag.
  A crawler that indexes this URL publishes the shell.
- **`Content-Security-Policy: default-src 'none'; script-src 'self'
  https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline'
  https://cdn.jsdelivr.net; img-src 'self' data:; connect-src 'self'; base-uri
  'none'; form-action 'none'; frame-ancestors 'none'`.** Script and style come
  from the binary or from the one pinned CDN origin, and nothing else — in
  particular `connect-src 'self'` means the page cannot talk to any host but
  the tunnel, so nothing it loads can exfiltrate the session. `unsafe-inline`
  is needed for styles only, because xterm.js writes its own; no inline script
  exists.
- **`X-Frame-Options: DENY`** alongside `frame-ancestors`, for older agents. A
  shell in an iframe is clickjacking bait.

Subresource integrity is what makes the CDN acceptable, and it is doing real
work: `script-src` permits jsdelivr as an *origin*, so without the hashes any
content jsdelivr served would run. The hash is the actual control; the CSP
entry only makes the request legal. Every tag therefore carries both
`integrity` and `crossorigin="anonymous"`, and a missing `crossorigin` is a
silent failure — the browser cannot inspect an opaque cross-origin response, so
it skips the check rather than failing closed. That is what the tests below
guard.

`Referrer-Policy: no-referrer` is load-bearing rather than tidy for the same
reason: the page now makes requests to a third-party origin, and the default
policy would send the tunnel URL — the capability itself — to jsdelivr in the
`Referer` header of each one.

The page carries no Open Graph tags and a generic `<title>`, so an unfurler has
nothing worth previewing and the preview cannot leak the hostname into a chat
transcript any more than the URL already does.

`web/assets/tush.js` is served with a long `Cache-Control: max-age` and no
`no-store`: it is static code and carries nothing session-specific.

## The dev loop

Three separate variables, each named for the one thing it does. They were
briefly a single `TUSH_DEBUG`, and splitting them was not tidiness: folding
them together meant the least safe of them decided where the others could be
used. Logging is worth having against a real tunnel, and it must not drag in a
working directory that may not exist or a listener nobody asked for.

**`TUSH_DEBUG`** — log each request to stderr, one line. Any non-empty value
counts, matching how `TUSH_E2E` is read. Safe anywhere, including against a
real tunnel: it writes lines and changes nothing. It is what distinguishes "the
browser never fetched the emulator" from "it fetched it and the WebSocket
failed", which is otherwise guesswork.

**`TUSH_LISTEN=<addr>`** — also serve on that address, printed next to the
tunnel URL. Naming the address is the opt-in and there is no default, because
the premise of `tush` is that a shell is reachable only through a URL somebody
chose to hand out, and a listener that appeared by default would quietly add a
second way in. Failing to bind is reported and not fatal — the tunnel is the
real path and must still come up.

**`TUSH_WEB_DIR=<dir>`** — read the page from that directory instead of from
the binary, so editing `index.html` or `tush.js` and refreshing the browser is
the whole loop. The directory is opened as an `os.Root`, which a name
containing `..` or a symlink pointing outward cannot escape; the names come
from a URL, so making that impossible by construction beats checking the string
carefully.

`make dev` sets all three. `make run` stays as it was.

`make web-sri` refetches each pinned CDN URL and checks that the recorded hash
still matches, and logs the hash it found so a version bump can be recorded. It
touches the network, so it is opt-in like `make e2e` rather than part of the
default target — a mismatch is either a bad bump or a CDN serving something it
should not, and neither belongs in a routine unit-test run.

## Two things the library brought with it

**The streaming library logs like kubelet.** It is kubelet's code, and it
writes to stderr — which here is the terminal the publisher is sitting at. A
second person opening the URL while somebody is attached is contention working
as intended, and it arrived on the publisher's screen as `"Unhandled Error"`
followed by a socket-receive error on every normal disconnect. Neither is
actionable, both are addressed to whoever runs a cluster, and the client is
already told what happened over the protocol.

Silencing it takes three things, and the middle one is the trap:
`logtostderr=false` alone changes nothing, because `stderrthreshold` defaults
to `ERROR` and overrides it. `runtime.ErrorHandlers` is a separate path again,
and is what produced the `"Unhandled Error"` line. Real failures are unaffected:
they travel to the client on the error channel, and the ones that end a session
come back through `Exited`.

**The icon is a file, not a data: URI.** Inlining it was the obvious way to
avoid a request, and browsers were observed resolving the URI as a relative
path — asking for `/data:image/svg+xml,…` and then for `/favicon.ico` anyway,
so it cost two requests rather than none. An HTML-parser probe confirmed the
markup was correct, so this is browser behaviour rather than a rendering bug,
and it was not worth chasing further.

The cost worth avoiding was never the request. It was that `/favicon.ico`
reached the catch-all and was answered with the entire page, for a request that
wanted an image and discarded the result. Both that path and `/assets/icon.svg`
now serve a few hundred cacheable bytes.

## Testing

Committed Go tests:

- `GET /` returns 200, `text/html`, and every security header above.
- The rendered configuration carries the **actual** `attach.Path`, parameter
  names, subprotocol and channel numbers, so the test fails if the Go and
  JavaScript sides drift apart.
- `web/assets/tush.js` serves with the right content type; an unknown asset
  path is a 404.
- **Every CDN tag carries a `sha384-` integrity value and
  `crossorigin="anonymous"`, and every CDN URL is pinned to an exact version.**
  Each of the three is a silent security failure on its own — a missing
  `crossorigin` disables checking without any visible symptom — so the rendered
  HTML is asserted on directly rather than the constants behind it.
- **`GET /` starts no shell.** This is the load-bearing unfurler property.
- **An abruptly dropped client releases the console**, so a reconnect succeeds
  rather than meeting `ErrBusy` forever. Already covered by
  `TestReleasesAfterAbruptDisconnect`, whose polling loop also shows the release
  is not instant — which is what the page's busy retry exists for.
- **Refusing a second client writes nothing to the publisher's terminal.** The
  test swaps the real stderr descriptor rather than reconfiguring a logger,
  because what matters is what reaches the terminal, whichever library decided
  to write it.
- **Neither way of asking for an icon renders the page.**
- **Only a `POST` ends the session**, a cross-origin one is refused, and a
  `POST` does end it — so that the first check is guarding a live route rather
  than a missing one.
- **The two clients agree on the detach chord**, which nothing else would catch:
  detaching is a client-side concern the server never sees.

Per [CLAUDE.md](../../../CLAUDE.md), each of these is confirmed able to fail
before it is reported as passing. That is not a formality here: an earlier
version of the icon test asserted the page contained `data:image/svg+xml,` and
passed for as long as the icon was broken, because it never checked the thing
that was wrong.

**What Go cannot test:** whether xterm.js renders, whether keystrokes arrive,
whether `vim` looks right. That is covered in two stages. First, manual
verification — the tunnel URL is handed over and driven in a real browser, and
the design iterates on what that finds. Only once it behaves is automating it
with Playwright worth discussing, and it would stay a local verification step
rather than a CI job, since adding Node to CI is the cost this design set out
to avoid.

That first stage earned its place. Driving it in a browser is what found the
klog noise and the icon, neither of which any Go test was going to reach, and
it is what exercised the two contention paths: refreshes reattached cleanly,
and the busy ladder was seen firing at +421 ms and +782 ms before the card
appeared. A colleague opening the URL from another network covered the full
remote path.

Any browser-driven test must use markers whose echo differs from their output,
for the reason in [terminal echo makes tests
lie](../../../CONTRIBUTING.md#terminal-echo-makes-tests-lie).

## Documentation changes

- `main.go` says the URL can also be opened in a browser. Without it the
  feature is invisible.
- `README.md` gains the browser as a way to attach, and the security section
  notes that the URL now yields a shell to anything with a browser.
- `CONTRIBUTING.md` gains `web/` in the "Where to find things" table, a note on
  the dev loop and its three variables, and one new entry under "Conventions
  that bite":
  **subresource integrity needs `crossorigin`**. Dropping that attribute, or
  loosening a pinned version to a range, disables hash checking with no visible
  symptom — the page keeps working perfectly right up until the CDN serves
  something else. It belongs with the other invariants whose breakage is
  invisible until it is expensive.

## Out of scope

- Authentication of any kind.
- Evicting an attached client in favour of a new one.
- Any control that grants more than the URL already does. Stop is in because
  `exit` was already equivalent; anything that is not would need the boundary
  reopening rather than a button.
- Copy and paste affordances, themes, font settings, a toolbar.
- Mobile and touch keyboard support.
- Committing a Playwright suite or adding Node to CI.
