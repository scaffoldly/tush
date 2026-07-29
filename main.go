package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cnuss/tush/queue"
	"github.com/cnuss/tush/shell"
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
		fmt.Printf("%s: %s\n", bin, version)
		os.Exit(0)
	case "":
		shell := shell.New(ctx)
		os.Exit(shell.Run())
	}

	fmt.Printf("%s: unimplemented\n", bin)
}
