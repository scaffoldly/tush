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
  var tools = document.getElementById("tools");
  var detachButton = document.getElementById("detach");
  var stopButton = document.getElementById("stop");

  // How long the Stop button stays armed before forgetting it was pressed.
  // Long enough to mean it, short enough that an armed button is not left
  // sitting there to be hit by accident later.
  var STOP_ARMED_MS = 4000;

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

  // Set when the session is being left on purpose, so that the close it causes
  // is reported as what it was rather than as the connection dropping.
  var farewell = null;

  // Set once the session has been stopped outright, which nothing recovers
  // from: the shell has gone and so has the tunnel.
  var stopped = false;

  // leave ends the session without ending the shell, which is the whole point
  // of the detach sequence: the far end sees a client go away and keeps
  // running, and Reconnect picks the same shell back up.
  function leave(reason) {
    farewell = reason;
    if (socket) {
      socket.close(1000, "detached");
    } else {
      offer(reason);
    }
  }

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

  // A trailing Ctrl+P, kept back until the next key says whether it began the
  // detach sequence or was a keystroke in its own right. This is what lets a
  // lone Ctrl+P still reach the shell as history-previous.
  var held = "";

  term.onData(function (data) {
    var typed = held + data;
    held = "";

    var split = splitDetach(typed);
    held = split.held;

    if (split.send) {
      send(config.channels.stdin, encoder.encode(split.send));
    }
    if (split.detach) {
      leave("Detached. The shell is still running.");
    }
  });

  // splitDetach separates what should reach the host from the detach sequence
  // and from one still in progress. A port of the function of the same name in
  // client/client.go — the two clients have to take the same keys out of the
  // stream, or the same chord would mean different things in a tab and a
  // terminal.
  function splitDetach(typed) {
    var prefix = String.fromCharCode(config.detach.prefix);
    var suffix = String.fromCharCode(config.detach.suffix);

    for (var i = 0; i < typed.length; i++) {
      if (typed[i] !== prefix) {
        continue;
      }
      if (i === typed.length - 1) {
        return { send: typed.slice(0, i), detach: false, held: prefix };
      }
      if (typed[i + 1] === suffix) {
        return { send: typed.slice(0, i), detach: true, held: "" };
      }
    }
    return { send: typed, detach: false, held: "" };
  }

  // Ctrl+P is the browser's print shortcut, and a print dialog opening halfway
  // through detaching would be its own problem. Returning true still lets the
  // terminal handle the key, which is what turns it into the byte above.
  term.attachCustomKeyEventHandler(function (e) {
    var chord = e.ctrlKey && !e.altKey && !e.metaKey;
    if (chord && e.type === "keydown" && (e.key === "p" || e.key === "q")) {
      e.preventDefault();
    }
    return true;
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

  detachButton.addEventListener("click", function () {
    leave("Detached. The shell is still running.");
  });

  // Stop takes two presses. It ends the session and the tunnel for everybody,
  // and the URL does not come back — too much to hang on a single click in the
  // corner of a window somebody is typing into.
  var armed = null;

  stopButton.addEventListener("click", function () {
    if (armed === null) {
      armed = setTimeout(disarm, STOP_ARMED_MS);
      stopButton.classList.add("arming");
      stopButton.textContent = "Really stop?";
      return;
    }
    disarm();
    halt();
  });

  function disarm() {
    if (armed !== null) {
      clearTimeout(armed);
      armed = null;
    }
    stopButton.classList.remove("arming");
    stopButton.textContent = "Stop";
  }

  // halt ends the session for everyone. The far end answers and then tears
  // itself down, so the socket closing is the expected outcome rather than a
  // failure, and there is nothing left to attach to afterwards.
  function halt() {
    stopButton.disabled = true;
    detachButton.disabled = true;

    fetch(config.stop, { method: "POST", cache: "no-store" }).then(
      function (response) {
        if (!response.ok) {
          stopButton.disabled = false;
          detachButton.disabled = false;
          leave("The session could not be stopped.");
          return;
        }
        farewell = null;
        finish(
          "Session stopped. The shell has ended and this URL is no longer live."
        );
        if (socket) {
          socket.close(1000, "stopped");
        }
      },
      function () {
        stopButton.disabled = false;
        detachButton.disabled = false;
        leave("The session could not be stopped.");
      }
    );
  }

  // finish shows the card with no way back, for the one ending that is final.
  function finish(reason) {
    stopped = true;
    tools.hidden = true;
    say(escapeHTML(reason));
    connect.disabled = true;
    connect.textContent = "Stopped";
    overlay.hidden = false;
  }

  function attach(attempt) {
    connect.disabled = true;
    connect.textContent = "Attaching...";

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
      tools.hidden = false;
      disarm();
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
      tools.hidden = true;

      // The session was ended outright. The card already says so, and there is
      // nothing left to attach to.
      if (stopped) {
        return;
      }

      // Left on purpose. Not a failure, and not something to retry.
      if (farewell !== null) {
        var said = farewell;
        farewell = null;
        held = "";
        offer(said);
        return;
      }

      // Somebody else holds the console. If this is a refreshed tab racing the
      // server's cleanup of its own previous socket, waiting briefly is enough.
      if (isBusy(reason) && attempt < BUSY_RETRIES) {
        setTimeout(function () {
          attach(attempt + 1);
        }, BUSY_BACKOFF_MS * (attempt + 1));
        return;
      }

      offer(humanise(reason) || "The connection closed.");
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

  // humanise turns what the protocol says into something worth reading.
  //
  // The wire format is kubelet's, and it describes the world as kubelet sees
  // it. A second person opening the URL was being told "Internal error
  // occurred: error attaching to container: console already has a client
  // attached" — which is not an internal error, has no container in it, and
  // buries the one fact that matters.
  function humanise(reason) {
    if (reason === null) {
      return null;
    }
    if (isBusy(reason)) {
      return "Somebody else is attached to this shell. Only one at a time.";
    }
    // Whatever else it says, it is not an internal error from where the reader
    // is sitting.
    return reason.replace(/^Internal error occurred:\s*/, "");
  }

  // offer puts the card back with what happened, so a session that ended is
  // visibly over rather than a terminal that has quietly stopped responding.
  function offer(reason) {
    say(escapeHTML(reason));
    connect.disabled = false;
    connect.textContent = "Attach";
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
