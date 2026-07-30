# CLAUDE.md

Guidance for Claude Code and other agents working in this repository.

## Start here

Read [CONTRIBUTING.md](./CONTRIBUTING.md) first. It is the single source for
the layout, the architecture, the dev loop, the release process, and — most
importantly — the [conventions that
bite](./CONTRIBUTING.md#conventions-that-bite). Everything an agent needs is
there; this file only adds what is specific to working here as an agent, and
deliberately does not restate it.

[README.md](./README.md) is what `tush` is and how to use it.

## What has actually gone wrong here

This codebase has a history of failures that look like success. When working
here, the following are worth more than speed:

**Verify before claiming.** Several bugs in this repo were "fixed" by a change
that could not have fixed them, and several tests passed while measuring
nothing. Before reporting a fix, make the test fail without it. Before
reporting a cause, reproduce it.

**A green test is not evidence on its own.** Terminal echo means a test can
pass without the shell ever running — see [terminal echo makes tests
lie](./CONTRIBUTING.md#terminal-echo-makes-tests-lie). If a test passes
immediately, confirm it can fail.

**Probe rather than theorise.** Two confident diagnoses here were wrong: that
`os.File.Fd()` blocking mode was breaking shutdown, and that scrollback replay
was re-triggering terminal queries. Both were disproved in minutes by a
throwaway probe. Cheap experiments beat plausible reasoning about ttys,
signals, and escape sequences.

**Say what is untested.** Much of this system can only be judged by a human
looking at a terminal — whether a reattached `vim` looks right, whether output
is garbled. Report what was verified and how, and name what was not.

## Boundaries

**Do not add authentication.** Its absence is a deliberate boundary owned by
the tunnel provider, not an oversight — see [no authentication by
design](./CONTRIBUTING.md#no-authentication-by-design). Raise a design change
rather than adding it.

**Do not weaken the invariants** in the conventions section to make something
pass. Each one is there because breaking it produced a real, hard-to-diagnose
failure.

**Do not commit or push unless asked.** Releases are automatic on push to
`main` — see [releases](./CONTRIBUTING.md#releases) — so a push publishes.

## Style

Comments and commit messages explain **why**, not what. The subtleties in this
repo — why the scrollback trims at a line, why the client sends empty frames,
why the shell claims a controlling terminal — are invisible in the code and
expensive to rediscover. Match the surrounding prose: full sentences, no
bullet-point shorthand, no restating the diff.
