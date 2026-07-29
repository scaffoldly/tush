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
	con, err := console.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", bin, err)
		return 1
	}

	sh, err := shell.New(ctx, con.TTY())
	if err != nil {
		con.Close()
		fmt.Fprintf(os.Stderr, "%s: %v\n", bin, err)
		return 1
	}

	tun := tunnel.New(ctx, "tunnel.pizza")
	srv := &http.Server{Handler: attach.New(con).Handler()}
	go srv.Serve(tun.Listener())
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	fmt.Fprintf(os.Stderr, "%s\n", tun.URL())
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop the tunnel.\n")

	status := sh.Run()
	con.Close()
	return status
}
