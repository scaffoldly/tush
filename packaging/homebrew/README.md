# Homebrew tap

Install the prebuilt release binary from the Scaffoldly tap:

```sh
brew tap scaffoldly/tap
brew install tush
```

The tap updates itself hourly from the latest public `scaffoldly/tush`
release. `formula.rb.tmpl` is the canonical formula shape and `render.sh`
fills its version and per-architecture checksums from release archives.
