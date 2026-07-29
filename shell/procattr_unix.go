//go:build unix

package shell

import "syscall"

// controllingTTY asks for the child to lead a new session and adopt its
// standard input as the controlling terminal. Ctty names the descriptor as the
// child will see it, which is 0 because the terminal is passed as its stdin.
func controllingTTY() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
}
