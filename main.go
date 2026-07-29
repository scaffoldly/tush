package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cnuss/tush/attach"
	"github.com/cnuss/tush/client"
	"github.com/cnuss/tush/console"
	"github.com/cnuss/tush/queue"
	"github.com/cnuss/tush/shell"
	"github.com/cnuss/tush/tunnel"
)

var version = "dev"

// exitInterrupted is the conventional status for a process killed by a signal.
const exitInterrupted = 130

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

	tun := tunnel.New(ctx, "tunnel.pizza")
	http := &http.Server{Handler: srv.Handler()}
	go http.Serve(tun.Listener())
	go func() {
		<-ctx.Done()
		http.Close()
	}()

	fmt.Fprintf(os.Stderr, "%s\n", tun.URL())
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop the tunnel.\n")

	select {
	case status := <-srv.Exited():
		return status
	case <-ctx.Done():
		return exitInterrupted
	}
}
