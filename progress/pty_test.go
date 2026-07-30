//go:build unix

package progress

import (
	"os"

	"github.com/creack/pty"
)

// openPTY gives the animation branch a real terminal to write to.
func openPTY() (master, tty *os.File, err error) { return pty.Open() }
