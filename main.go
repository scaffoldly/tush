package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/scaffoldly/tush/attach"
	"github.com/scaffoldly/tush/client"
	"github.com/scaffoldly/tush/console"
	"github.com/scaffoldly/tush/debug"
	"github.com/scaffoldly/tush/progress"
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

// edgeTimeout bounds how long to wait for a freshly minted hostname to start
// routing before handing the URL over anyway.
const edgeTimeout = 30 * time.Second

// shutdownGrace bounds how long to wait for the shell to die after a stop was
// asked for, before giving up and going anyway.
const shutdownGrace = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := queue.New(os.Args)
	bin := filepath.Base(args.Shift())
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
	})

	report := progress.New(os.Stderr)
	report.Step("Requesting tunnel...")

	tun := tunnel.New(ctx, provider)
	server := &http.Server{Handler: logged(routes(srv, stop))}
	go server.Serve(tun.Listener())
	go func() {
		<-ctx.Done()
		server.Close()
	}()

	// A hostname is minted before the edge routes to it, so a URL handed over
	// the moment it exists is one the first client may fail to reach. Wait for
	// the round trip to land here before calling it ready.
	address := tun.URL()
	report.Step("Connecting to tunnel...")
	routed := waitForEdge(ctx, address)
	report.Stop()

	fmt.Fprintf(os.Stderr, "Attach from another machine with:\n\n")
	fmt.Fprintf(os.Stderr, "    %s %s\n\n", bin, address)
	fmt.Fprintf(os.Stderr, "or open that URL in a browser.\n")
	if !routed {
		fmt.Fprintf(os.Stderr, "The tunnel is not answering yet; it may need another moment.\n")
	}
	fmt.Fprintf(os.Stderr, "Anyone with that URL gets a shell as you.\n")
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop the tunnel.\n")

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

// waitForEdge reports whether the published address reaches this process.
//
// The attach endpoint refuses a request that selects no streams, so a 400 is
// proof the request arrived here rather than stopping at the edge, which
// answers on its own — a 530 or a 1033 — until the hostname propagates. A
// timeout is not fatal: the URL is still correct, and saying so beats waiting
// on it indefinitely.
func waitForEdge(ctx context.Context, address string) bool {
	probe, err := url.Parse(address)
	if err != nil {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, edgeTimeout)
	defer cancel()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.JoinPath(attach.Path).String(), nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == http.StatusBadRequest {
				return true
			}
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}
