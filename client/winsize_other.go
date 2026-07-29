//go:build !unix

package client

import "context"

// watchResize has no window-change signal to listen for here. The size sent at
// the start of the session is the only one the shell will hear about.
func watchResize(ctx context.Context, notify func()) {}
