.PHONY: all check fmt fmt-check vet build test e2e binary dist run dev dev-start dev-stop dev-log dev-url web-sri clean

# tush is pure Go. Forcing CGO off keeps every build identical across hosts and
# makes the binary static and dependency-free, so it can be dropped into a
# minimal image and still serve a shell.
export CGO_ENABLED = 0

# Release build inputs.
#   VERSION    — stamped into the binary (`tush version`); git describe by
#                default, overridable (the release workflow passes the tag).
#   GO_LDFLAGS — strip the symbol table (-s) and DWARF (-w) for a smaller
#                binary; -trimpath (on the build lines) drops host paths so
#                the build is reproducible.
#   PLATFORMS  — the release OS/arch matrix. No Windows: the host side needs a
#                pseudo-terminal and a controlling terminal to give the shell
#                job control, neither of which Windows has in this form.
BINARY = tush
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_LDFLAGS = -s -w -X main.version=$(VERSION)
PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# Where `make dev-start` keeps its log and process id, and how long it waits for
# the tunnel to start routing. Both files are gitignored.
DEV_LOG = .dev.log
DEV_PID = .dev.pid
DEV_TIMEOUT = 60

# Default: everything CI runs except the auto-bump release step.
all: fmt-check vet build test e2e

# The common pre-push checklist. Mirrors the CI matrix.
check: fmt-check vet test e2e

# gofmt the tree in place.
fmt:
	gofmt -w .

# Fail if anything in the tree is not gofmt-clean.
fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt found unformatted files:"; echo "$$out"; exit 1; fi

# Static analysis across every package.
vet:
	go vet ./...

# Build the whole module for the host platform.
build:
	go build ./...

# Unit tests. The console and client packages open real pseudo-terminals and
# run the host's shell, so these need a working tty but no network.
test:
	go test ./...

# End-to-end against a real tunnel. Skipped unless TUSH_E2E is set, so this is
# a fast no-op locally and in the CI cells that do not opt in. -count=1
# disables caching, and the timeout gives the idle case room: it sits quiet for
# minutes on purpose, to prove an idle session is not dropped.
e2e:
	go test -count=1 -v -timeout 20m ./e2e

# Build for the host platform, stamped and stripped.
binary:
	go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $(BINARY) .

# Cross-compile for every supported OS/arch into dist/, then write a SHA256SUMS
# manifest. Reproducible (-trimpath), stripped, static (CGO off) — the
# artifacts the release workflow attaches and signs.
dist:
	rm -rf dist
	mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=dist/$(BINARY)_$${os}_$${arch}; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(GO_LDFLAGS)" -o $$out . || exit 1; \
	done
	cd dist && { command -v sha256sum >/dev/null 2>&1 && sha256sum * || shasum -a 256 *; } > SHA256SUMS
	@echo "dist/ contents:" && ls -1 dist

# Publish a tunnel and serve a shell behind it.
run:
	go run .

# The same, set up for working on the browser page: the page is read from web/
# rather than from the embed, so edit-refresh-look needs no rebuild, and
# requests are logged, so a page that does not work says which half failed.
#
# Everything still goes through the tunnel. There is no local shortcut on
# purpose: the tunnel is the path a session actually takes, and testing past it
# tests something no user does.
dev: export TUSH_DEBUG = 1
dev: export TUSH_WEB_DIR = web
dev:
	go run .

# The same session, in the background, for when something else has to drive the
# browser while it runs.
#
# It runs the built binary rather than `go run .` on purpose. `go run` is a
# parent that execs the real program, so killing what you started leaves the
# actual server behind — which is how a stale process ends up holding a port
# and answering with an older build long after you thought you had stopped it.
# One binary means one pid, and $(DEV_PID) is that pid.
dev-start: export TUSH_DEBUG = 1
dev-start: export TUSH_WEB_DIR = web
dev-start: binary
	@$(MAKE) --no-print-directory dev-stop
	@$(CURDIR)/$(BINARY) > $(DEV_LOG) 2>&1 & echo $$! > $(DEV_PID)
	@for i in $$(seq 1 $(DEV_TIMEOUT)); do \
		grep -q "Press Ctrl+C" $(DEV_LOG) 2>/dev/null && break; \
		sleep 1; \
	done
	@grep -E "tunneled\.pizza" $(DEV_LOG) \
		|| { echo "did not come up within $(DEV_TIMEOUT)s; see $(DEV_LOG)"; exit 1; }

# Stop it, and sweep up anything left from an earlier run. The sweep matches
# this checkout's binary by absolute path, so it cannot reach another project's.
dev-stop:
	@if [ -f $(DEV_PID) ]; then kill $$(cat $(DEV_PID)) 2>/dev/null || true; rm -f $(DEV_PID); fi
	@pkill -f "^$(CURDIR)/$(BINARY)$$" 2>/dev/null || true

# What the background session has been asked for, and what it said.
dev-log:
	@tail -n $(or $(N),40) $(DEV_LOG) 2>/dev/null || echo "no $(DEV_LOG); run make dev-start"

# Just the URL, for pasting or scripting.
dev-url:
	@grep -oE 'https://[a-z0-9]+\.tunneled\.pizza/' $(DEV_LOG) 2>/dev/null | head -1 \
		|| { echo "no URL in $(DEV_LOG); run make dev-start"; exit 1; }

# Check that the third-party assets the browser page loads still hash to what
# web/assets.go records, and print the hash for each so a version bump can be
# recorded. This is what a browser does on every load; doing it here means a
# drift is found deliberately rather than as a page that stopped working.
#
# It touches the network, so it is opt-in like e2e rather than part of `all`: a
# mismatch is either a bad version bump or a CDN serving something it should
# not, and neither belongs in a routine unit-test run.
web-sri:
	TUSH_SRI=1 go test -count=1 -v -run TestAssetHashesStillMatch ./web

clean: dev-stop
	rm -rf dist $(BINARY) $(DEV_LOG) $(DEV_PID)
