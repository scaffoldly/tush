#!/usr/bin/env node
// The wrapper package's only executable: find the binary for this machine and
// become it.
//
// tush itself is a Go binary. What npm publishes is one package per platform,
// each carrying a single binary and marked with the os and cpu it belongs to,
// plus this wrapper listing all of them as optional dependencies. npm installs
// only the one that matches, so `npx @scaffoldly/tush` fetches one binary
// rather than five, and never runs a download script of its own.
"use strict";

const { spawnSync } = require("child_process");

// The platform packages are named for npm's own platform and architecture
// values, so the name is derived rather than looked up in a table here. A table
// would be a second list to keep in step with what is actually published, and
// the symptom of it drifting is an install that works and a command that does
// not.
const PACKAGE = `@scaffoldly/tush-${process.platform}-${process.arch}`;

function fail(message) {
  process.stderr.write(`tush: ${message}\n`);
  process.exit(1);
}

function binary() {
  // Windows has no pseudo-terminal and no controlling terminal in the form the
  // host side needs to give a shell job control, so there is no binary to find
  // rather than one that is missing.
  if (process.platform === "win32") {
    fail(
      "Windows is not supported: the host needs a pseudo-terminal and a " +
        "controlling terminal to give the shell job control, and Windows has " +
        "no equivalent of either."
    );
  }

  try {
    return require.resolve(`${PACKAGE}/bin/tush`);
  } catch (e) {
    fail(
      `no binary for ${process.platform}-${process.arch}.\n` +
        `  Expected it in ${PACKAGE}, which is an optional dependency of this package.\n` +
        "  If this platform should be supported, the install skipped optional\n" +
        "  dependencies — check for --no-optional, --ignore-optional, or an\n" +
        "  omit=optional setting.\n" +
        "  Otherwise see https://github.com/scaffoldly/tush/releases"
    );
  }
}

// What the user typed, so that tush can tell them what to type again. It prints
// a command to run on the other machine, and its own name is `tush` however it
// was reached — which is not a command anyone has when they got here through
// npx and installed nothing.
//
// npm sets npm_command=exec for `npx` and for `npm exec`, and nothing at all
// when a globally installed bin is run directly. So the absence of it means the
// command really is `tush`.
const invocation =
  process.env.npm_command === "exec" ? "npx @scaffoldly/tush" : "tush";

// spawnSync rather than execFileSync: execFileSync throws on a non-zero exit,
// and tush's exit status is meaningful — it carries the status the published
// shell ended with. Throwing would replace that with a stack trace.
const result = spawnSync(binary(), process.argv.slice(2), {
  stdio: "inherit",
  env: { ...process.env, TUSH_COMMAND: invocation },
});

if (result.error) {
  fail(String(result.error.message || result.error));
}

// Killed by a signal: end the same way rather than reporting a status the child
// never had. Ctrl+C reaches the child directly — it shares this terminal and
// process group — so this is about what the shell that ran npx sees afterwards.
if (result.signal) {
  process.kill(process.pid, result.signal);
}

process.exit(result.status === null ? 1 : result.status);
