//go:build unix

package client

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// watchResize calls notify whenever the terminal's window changes size.
func watchResize(ctx context.Context, notify func()) {
	changed := make(chan os.Signal, 1)
	signal.Notify(changed, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(changed)
		for {
			select {
			case <-changed:
				notify()
			case <-ctx.Done():
				return
			}
		}
	}()
}
