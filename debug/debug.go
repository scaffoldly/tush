// Package debug holds the switches that make tush easier to see into, and
// nothing else. Each is off unless its variable is set, and none of them
// changes what a session does.
//
// They are separate on purpose. Logging is useful against a real tunnel, where
// serving the page from a working directory that may not exist would be a
// crash and a second listener would be a surprise; the dev loop for the page
// wants all three. Folding them into one switch means the least safe of them
// decides where the others can be used.
package debug

import (
	"fmt"
	"os"
)

const (
	// envLog turns on request logging. Any non-empty value counts, matching how
	// TUSH_E2E is read. Safe anywhere: it writes lines and changes nothing.
	envLog = "TUSH_DEBUG"

	// envListen is an address to serve on in addition to the tunnel. Naming the
	// address is the opt-in; there is no default, because a listener nobody
	// asked for is a shell on a port nobody expected.
	envListen = "TUSH_LISTEN"

	// envWebDir is a directory to read the browser page from instead of the
	// copy embedded in the binary. It exists so that editing the page and
	// refreshing the browser is the whole loop, with no rebuild.
	envWebDir = "TUSH_WEB_DIR"
)

// Logging reports whether to write diagnostic lines.
func Logging() bool {
	return os.Getenv(envLog) != ""
}

// Listen is an extra address to serve on, or empty for none.
//
// Anything is accepted, including a wildcard, because someone testing from
// another device on their network has a real reason to want one. It is off
// unless asked for, which is what keeps that from being a default.
func Listen() string {
	return os.Getenv(envListen)
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
