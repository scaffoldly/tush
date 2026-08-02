// Package debug holds the switches that make tush easier to see into, and
// nothing else. Each is off unless its variable is set, and none of them
// changes what a session does.
//
// They are separate on purpose. Logging is worth having against a real tunnel,
// where serving the page from a working directory that may not exist would be
// a crash; folding them into one switch means the less safe of them decides
// where the other can be used.
//
// There is deliberately no way to serve on a local port. Everything goes
// through the tunnel, which is the path a session actually takes, and a
// shortcut past it tests something users never do. Serving locally belongs to
// the tunnel library rather than here — see cnuss/libtunnel.
package debug

import (
	"fmt"
	"os"
)

const (
	// envLog turns on request logging. Any non-empty value counts, matching how
	// TUSH_E2E is read. Safe anywhere: it writes lines and changes nothing.
	envLog = "TUSH_DEBUG"

	// envWebDir is a directory to read the browser page from instead of the
	// copy embedded in the binary. It exists so that editing the page and
	// refreshing the browser is the whole loop, with no rebuild.
	envWebDir = "TUSH_WEB_DIR"
)

// Logging reports whether to write diagnostic lines.
func Logging() bool {
	return os.Getenv(envLog) != ""
}

// WebDir is where to read the browser page from, or empty to use the copy built
// into the binary.
func WebDir() string {
	return os.Getenv(envWebDir)
}

// Logf writes a line to stderr when logging is on, and does nothing otherwise.
//
// To stderr rather than stdout, because stdout may be piped somewhere that
// expects only what tush deliberately prints.
func Logf(format string, args ...any) {
	if !Logging() {
		return
	}
	fmt.Fprintf(os.Stderr, "tush: "+format+"\n", args...)
}
