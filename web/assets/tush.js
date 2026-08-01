// The browser end of a tush session.
//
// This is the same client as client/client.go, in JavaScript: it runs no
// interpreter, forwards what is typed, and paints what comes back. The protocol
// details it needs — the endpoint, the subprotocol, the channel numbers — are
// not written here. They are rendered into the page from the Go constants that
// define the server's behaviour, because a copy of them here would drift, and
// the symptom of drift is a terminal that shows nothing at all.

(function () {
  "use strict";

  var config = JSON.parse(document.getElementById("tush-config").textContent);

  var screen = document.getElementById("screen");
  var overlay = document.getElementById("overlay");
  var message = document.getElementById("message");
  var connect = document.getElementById("connect");

  var encoder = new TextEncoder();
  var decoder = new TextDecoder();

  // How many times a busy console is retried before it reaches the overlay. A
  // refreshed tab can lose a race against the server noticing that its own
  // previous socket died, and that window is short.
  var BUSY_RETRIES = 2;
  var BUSY_BACKOFF_MS = 350;

  // The emulator comes from a CDN and is pinned by hash, so it can fail to
  // arrive three ways: the network blocked it, the CDN was down, or what
  // arrived did not match the hash and the browser refused to run it. All three
  // look identical from here, and all three must say so rather than leaving a
  // blank screen behind a button that does nothing.
  var Fit = window.FitAddon && window.FitAddon.FitAddon;
  if (typeof window.Terminal !== "function" || typeof Fit !== "function") {
    connect.disabled = true;
    say(
      '<span class="warn">The terminal could not be loaded.</span> Its library ' +
        "is fetched from a CDN, and either the network blocked it or what " +
        "arrived did not match the hash this page expects.<br /><br />" +
        "Attaching from a terminal still works:<br /><code>tush " +
        location.origin +
        "</code>"
    );
    return;
  }

  var term = new window.Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, "Cascadia Mono", monospace',
    fontSize: 14,
    scrollback: 5000,
    // The host replays its own scrollback on attach, so what matters is that
    // this matches a terminal the shell will recognise.
    theme: { background: "#0b0c0e", foreground: "#e6e8eb" },
  });

  var fit = new Fit();
  term.loadAddon(fit);
  term.open(screen);
  fit.fit();

  var socket = null;

  // Report the size whenever it changes, but only while attached: the host has
  // nowhere to put a size for a session that does not exist yet.
  term.onResize(function (size) {
    sendSize(size.cols, size.rows);
  });

  // Both, because they catch different things: the window changing shape, and
  // the element changing shape without the window doing so.
  window.addEventListener("resize", refit);
  if (window.ResizeObserver) {
    new window.ResizeObserver(refit).observe(screen);
  }

  function refit() {
    try {
      fit.fit();
    } catch (e) {
      // fit throws while the element has no layout, which happens during
      // teardown. Nothing to do about it and nothing to report.
    }
  }

  term.onData(function (data) {
    send(config.channels.stdin, encoder.encode(data));
  });

  // Input that is not valid UTF-8 text arrives here instead, as a string of
  // raw bytes. Dropping it would silently lose keys.
  term.onBinary(function (data) {
    var bytes = new Uint8Array(data.length);
    for (var i = 0; i < data.length; i++) {
      bytes[i] = data.charCodeAt(i) & 255;
    }
    send(config.channels.stdin, bytes);
  });

  connect.addEventListener("click", function () {
    attach(0);
  });

  function attach(attempt) {
    connect.disabled = true;
    connect.textContent = "Connecting...";

    var endpoint = new URL(config.endpoint, location.href);
    endpoint.protocol = endpoint.protocol === "https:" ? "wss:" : "ws:";

    var ws = new WebSocket(endpoint, [config.subprotocol]);
    ws.binaryType = "arraybuffer";

    // Why the shell ended, if it said. Kept out here because it arrives on the
    // error channel just before the socket closes, and the close is what
    // decides whether to show it or to retry.
    var reason = null;
    var keepAlive = null;

    ws.onopen = function () {
      socket = ws;

      // The host replays its scrollback on attach. Clearing first means a
      // reconnect shows that replay once, rather than appending it below the
      // copy already on screen from the previous connection.
      term.reset();

      overlay.hidden = true;
      term.focus();
      refit();

      // Explicitly, because onResize only fires on a change and the size may
      // well be the same one the terminal already had.
      sendSize(term.cols, term.rows);

      keepAlive = setInterval(function () {
        // Nothing at all — not even a channel — which the host skips without
        // touching the session. Its only purpose is to be traffic, so that an
        // intermediary does not drop a connection it thinks has gone quiet.
        ws.send(new Uint8Array(0));
      }, config.keepAliveSeconds * 1000);
    };

    ws.onmessage = function (event) {
      var frame = new Uint8Array(event.data);
      if (frame.length === 0) {
        return;
      }
      var payload = frame.subarray(1);
      switch (frame[0]) {
        case config.channels.stdout:
        case config.channels.stderr:
          term.write(payload);
          break;
        case config.channels.error:
          reason = describe(payload);
          break;
      }
    };

    ws.onclose = function () {
      if (keepAlive !== null) {
        clearInterval(keepAlive);
      }
      socket = null;

      // Somebody else holds the console. If this is a refreshed tab racing the
      // server's cleanup of its own previous socket, waiting briefly is enough.
      if (isBusy(reason) && attempt < BUSY_RETRIES) {
        setTimeout(function () {
          attach(attempt + 1);
        }, BUSY_BACKOFF_MS * (attempt + 1));
        return;
      }

      offer(reason || "The connection closed.");
    };

    ws.onerror = function () {
      // onclose always follows, and it is the one that knows whether to retry.
    };
  }

  function send(channel, payload) {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    var frame = new Uint8Array(payload.length + 1);
    frame[0] = channel;
    frame.set(payload, 1);
    socket.send(frame);
  }

  function sendSize(cols, rows) {
    // The field names are the protocol's, capitalised as Go marshals them.
    send(
      config.channels.resize,
      encoder.encode(JSON.stringify({ Width: cols, Height: rows }))
    );
  }

  // describe turns what arrived on the error channel into something worth
  // reading. The protocol wraps the host's own report in prose of its own, so
  // the message is taken whole rather than picked apart.
  function describe(payload) {
    try {
      var status = JSON.parse(decoder.decode(payload));
      if (status.status === "Success") {
        return "The shell exited.";
      }
      return status.message || "The session ended.";
    } catch (e) {
      return "The session ended.";
    }
  }

  function isBusy(reason) {
    return reason !== null && reason.indexOf(config.busy) !== -1;
  }

  // offer puts the card back with what happened, so a session that ended is
  // visibly over rather than a terminal that has quietly stopped responding.
  function offer(reason) {
    say(escapeHTML(reason));
    connect.disabled = false;
    connect.textContent = "Reconnect";
    overlay.hidden = false;
  }

  function say(html) {
    message.innerHTML = html;
  }

  // The reason comes from the far end, so it is text until proven otherwise.
  function escapeHTML(text) {
    var div = document.createElement("div");
    div.textContent = text;
    return div.innerHTML;
  }
})();
