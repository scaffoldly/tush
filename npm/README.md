# npm packaging

What makes `npx @scaffoldly/tush` work.

## Shape

npm distributes a precompiled binary as **one package per platform**, each
marked with the `os` and `cpu` it belongs to, plus a **wrapper** listing them
all as `optionalDependencies`:

```
@scaffoldly/tush                  the wrapper: shim.js and nothing else
@scaffoldly/tush-darwin-arm64     {"os":["darwin"],"cpu":["arm64"]} + the binary
@scaffoldly/tush-darwin-x64
@scaffoldly/tush-linux-x64
@scaffoldly/tush-linux-arm64
@scaffoldly/tush-linux-arm
```

The package manager installs only the one that matches, so a user fetches one
binary rather than five, and `npx` needs nothing else.

## Why not download from GitHub releases

That is the obvious approach and it is the one
[esbuild moved away from](https://github.com/evanw/esbuild/pull/1621). A
`postinstall` script that fetches a binary breaks behind proxies and custom
registries, offline, on read-only filesystems, and anywhere install scripts are
disabled — which is increasingly everywhere, because they are a supply-chain
vector.

It would also be at odds with the rest of this project. The browser page pins
its CDN assets by hash precisely because *"a third party able to inject script
into this page can take the shell it grants"*. An unverified download that then
executes is the same risk with higher stakes. Packages get lockfile pinning,
integrity hashes, and npm provenance tied to the workflow that built them.

## The two halves, and the seam between them

- **`shim.js`** is the wrapper's only executable. It resolves the binary and
  becomes it.
- **`generate/`** builds the packages from whatever binaries are in `dist/`.

They agree on one thing: a platform package is named
`@scaffoldly/tush-<platform>-<arch>`, using npm's own names. The shim
**derives** that name rather than looking it up in a table, so there is no
second list to fall out of step — and `TestShimNamesThePackagesThatAreBuilt`
fails if the shim stops deriving it.

The platforms themselves come from what is in `dist/`, not from a list kept
here. The build matrix already exists in the Makefile and the release workflow;
a third copy would be the one nobody remembers.

## Running it

```sh
make npm-packages             # into dist/npm, using the version from git describe
make npm-packages VERSION=v1.2.3
```

The release workflow does the same thing with the binaries it just built,
attested and signed, then publishes. Nothing rebuilds downstream: the binary
inside the npm package is the same one inside the archive on the release.

## Telling the user what to type

tush prints a command to run on the *other* machine. Its own name is `tush`
however it was reached, which is wrong for anyone who got here through npx and
installed nothing.

So the shim sets **`TUSH_COMMAND`**, which `invocation()` in
[`main.go`](../main.go) prefers over the program's own name. It says
`npx @scaffoldly/tush` only when npm reports `npm_command=exec` — set by `npx`
and `npm exec`, and absent when a globally installed bin runs directly, where
`tush` really is the right answer.

`go run` is handled separately and without help: the binary sits in a
`go-build…/exe/` directory that is about to be deleted, so tush recognises that
and says `go run <module>@latest` instead, taking the module path from its own
build info.

## Things that bite

- **`spawnSync`, not `execFileSync`.** The latter throws on a non-zero exit, and
  tush's exit status is meaningful — it carries the status the published shell
  ended with.
- **The executable bit.** CI artifacts do not preserve it, and npm ships a file
  exactly as it finds it. A binary without `+x` installs fine and then cannot
  run.
- **No `os` field on the wrapper.** It installs everywhere on purpose, so that
  an unsupported platform gets the shim's explanation instead of npm's
  `EBADPLATFORM`.
