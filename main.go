package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cnuss/tush/client"
	"github.com/cnuss/tush/console"
	"github.com/cnuss/tush/pipeline"
	"github.com/cnuss/tush/queue"
	"github.com/cnuss/tush/shell"
	"github.com/cnuss/tush/tunnel"
)

var version = "dev"

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
		tun := tunnel.New(ctx, "tunnel.pizza")
		pipe := pipeline.New(ctx).WithListener(tun.Listener())
		fmt.Fprintf(os.Stderr, "%s\n", tun.URL())
		fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop the tunnel.\n")

		// The shell blocks on its first prompt until a client attaches, so the
		// URL is printed before this point.
		streams := pipe.Stdio()

		// The shell runs against a real terminal rather than the tunnel
		// directly, so that echo, line editing and full-screen programs work.
		con, err := console.Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", bin, err)
			os.Exit(1)
		}
		sh := shell.New(ctx).WithStdio(con.TTY(), con.TTY(), streams.Stderr)
		con.WithInterrupt(sh.Interrupt).Attach(ctx, streams.Stdin, streams.Stdout)

		status := sh.Run()
		con.Close()
		os.Exit(status)
	}

	target, err := url.Parse(arg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", bin, err)
		os.Exit(1)
	}

	// Client mode is a dumb terminal: it forwards local input to the host and
	// renders what comes back.
	pipe := pipeline.New(ctx).WithURL(target)
	cli := client.New(ctx).
		WithTerminal(pipe.Terminal()).
		WithStdio(os.Stdin, os.Stdout, os.Stderr)
	os.Exit(cli.Run())
}
