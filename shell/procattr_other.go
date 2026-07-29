//go:build !unix

package shell

import "syscall"

// controllingTTY has nothing to ask for where sessions and controlling
// terminals do not exist. The shell still runs; it simply has no job control.
func controllingTTY() *syscall.SysProcAttr {
	return nil
}
