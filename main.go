package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	buildinfo "runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/scaffoldly/tush/attach"
	"github.com/scaffoldly/tush/client"
	"github.com/scaffoldly/tush/console"
	"github.com/scaffoldly/tush/debug"
	"github.com/scaffoldly/tush/queue"
	"github.com/scaffoldly/tush/shell"
	"github.com/scaffoldly/tush/tunnel"
	"github.com/scaffoldly/tush/web"
)

var version = "dev"

// exitInterrupted is the conventional status for a process killed by a signal.
const exitInterrupted = 130

// provider mints the tunnel this publishes through.
const provider = "tunnel.pizza"

// shutdownGrace bounds how long to wait for the shell to die after a stop was
// asked for, before giving up and going anyway.
const shutdownGrace = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := queue.New(os.Args)
	bin := invocation(args.Shift())
	arg := args.Shift()

	switch arg {
	case "version":
		fmt.Fprintf(os.Stdout, "%s: %s\n", bin, version)
		os.Exit(0)
	case "help":
		fmt.Fprintf(os.Stdout, "Usage: %s [URL]\n", bin)
		fmt.Fprintf(os.Stdout, "  If no URL is provided, a new tunnel will be created.\n")
		os.Exit(0)
	case "":
		os.Exit(host(ctx, bin))
	}

	target, err := url.Parse(arg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", bin, err)
		os.Exit(1)
	}
	os.Exit(client.New(ctx).
		WithURL(target).
		WithStdio(os.Stdin, os.Stdout, os.Stderr).
		Run())
}

// envCommand lets a launcher say how tush was actually invoked.
const envCommand = "TUSH_COMMAND"

// invocation is what to call tush when telling the user what to run.
//
// The command tush prints is meant to be typed on another machine, so it has to
// be one that exists there. Its own name is `tush` however it was reached,
// which is wrong for the two ways of running it that install nothing:
// `npx @scaffoldly/tush`, where there is no `tush` on the far end either, and
// `go run`, where the binary is in a build directory that will be gone shortly.
//
// A launcher that knows better says so through the environment; otherwise this
// works it out from where the binary is.
func invocation(argv0 string) string {
	if name := os.Getenv(envCommand); name != "" {
		// Printed to a terminal, so reduce it to characters that only draw
		// themselves. It is the caller's own environment, but a command hint is
		// no place to find escape sequences.
		return strings.Map(func(r rune) rune {
			if unicode.IsGraphic(r) || r == ' ' {
				return r
			}
			return -1
		}, name)
	}

	if underGoRun(argv0) {
		return "go run " + modulePath() + "@latest"
	}
	return filepath.Base(argv0)
}

// underGoRun reports whether this binary was built by `go run`, which compiles
// into a temporary directory and deletes it afterwards. The binary is named
// tush either way, so the name alone cannot tell that apart from an install.
//
// There is no API that answers this — the build info a Go binary carries says
// how it was built, not how it was launched — so it is read from where the
// executable ended up.
func underGoRun(argv0 string) bool {
	dir := filepath.Dir(argv0)
	return filepath.Base(dir) == "exe" && strings.Contains(dir, "go-build")
}

// modulePath is where tush can be fetched from, taken from the build info the
// binary already carries rather than written out again. This module has been
// renamed once; a copy here would have survived that rename and started telling
// people to install something that no longer exists.
func modulePath() string {
	if info, ok := buildinfo.ReadBuildInfo(); ok && info.Main.Path != "" {
		return info.Main.Path
	}
	return "github.com/scaffoldly/tush"
}

// host publishes a tunnel and serves the shell behind it. The shell runs on a
// pseudo-terminal that outlives any one client, so a client that drops can
// reconnect to the session it left.
func host(ctx context.Context, bin string) int {
	if shell.Find() == "" {
		fmt.Fprintf(os.Stderr, "%s: %v\n", bin, shell.ErrNoShell)
		return 1
	}

	// Stopping from the browser winds down the same way Ctrl+C does — the
	// cancelled context kills the shell and closes the server — rather than
	// being a second teardown path that has to be kept correct separately.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopped := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopped) }) }

	con, err := console.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", bin, err)
		return 1
	}
	defer con.Close()

	// The shell starts with the first client rather than now, so that it takes
	// that client's terminal type and writes its opening prompt to somebody.
	srv := attach.New(con, func(term string) (<-chan int, error) {
		sh, err := shell.New(ctx, con.TTY())
		if err != nil {
			return nil, err
		}
		status := make(chan int, 1)
		go func() { status <- sh.WithTerm(term).Run() }()
		return status, nil
	}).WithCommand(shell.Find())

	tun := tunnel.New(ctx, provider)
	server := &http.Server{Handler: logged(routes(srv, stop))}
	go server.Serve(tun.Listener())

	go func() {
		hostname := <-tun.Hostname()
		if hostname == "" {
			fmt.Fprintf(os.Stderr, "Failed to get tunnel hostname\n")
			cancel()
			return
		}
		fmt.Fprintf(os.Stderr, "Attaching %s to %s...\n", hostname, srv.CMD())
		url := <-tun.URL()
		if url == "" {
			fmt.Fprintf(os.Stderr, "Failed to get tunnel URL\n")
			cancel()
			return
		}
		fmt.Fprintf(os.Stderr, "\nAttached:\n")
		fmt.Fprintf(os.Stderr, "🌎 %s\n\n", url)
		fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop.\n")
		<-ctx.Done()
		server.Close()
	}()

	select {
	case status := <-srv.Exited():
		return status
	case <-stopped:
		fmt.Fprintf(os.Stderr, "\nStopped from the browser.\n")
		// Cancelling kills the shell; wait for it rather than closing the
		// terminal out from under a process still holding it.
		cancel()
		select {
		case <-srv.Exited():
		case <-time.After(shutdownGrace):
		}
		return 0
	case <-ctx.Done():
		return exitInterrupted
	}
}

// routes puts the attach endpoint and the browser page behind one address.
//
// They are two views of the same session, so which one a request gets is
// decided here rather than inside either. The page is the catch-all: a browser
// that has landed anywhere under this URL wants a terminal.
func routes(srv *attach.Server, stop func()) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(attach.Path, srv.Handler())
	mux.Handle("/", web.Handler(stop))
	return mux
}

// logged reports what was asked for, when logging is on. Without it, a page
// that does not work is indistinguishable between never having been fetched and
// having been fetched and then failed.
func logged(next http.Handler) http.Handler {
	if !debug.Logging() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		debug.Logf("%s %s", r.Method, r.URL.RequestURI())
		next.ServeHTTP(w, r)
	})
}
