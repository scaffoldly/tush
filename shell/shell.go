// Package shell runs the user's own shell on a terminal.
//
// tush does not interpret anything itself. Handing the session to a real shell
// on a real terminal is what makes it behave like one: the user's own shell
// with their configuration, job control, line editing, history, and every
// program that expects a terminal to be there.
package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// exitInterrupted is the conventional status for a shell killed by a signal.
const exitInterrupted = 130

// fallbackShells are tried in order when SHELL says nothing useful.
var fallbackShells = []string{"bash", "sh"}

// ErrNoShell is returned when the host has no shell to run at all, as in a
// minimal container image.
var ErrNoShell = errors.New("no shell found: set SHELL or install one")

// Find returns the shell to hand the user, preferring their own, and "" when
// the host has none.
func Find() string {
	if env := os.Getenv("SHELL"); env != "" {
		if path, err := exec.LookPath(env); err == nil {
			return path
		}
	}
	for _, name := range fallbackShells {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// Shell is the user's shell, running on a terminal.
type Shell struct {
	ctx  context.Context
	path string
	tty  *os.File
	term string
}

// New prepares a shell to run on tty. It fails when the host has no shell.
func New(ctx context.Context, tty *os.File) (*Shell, error) {
	path := Find()
	if path == "" {
		return nil, ErrNoShell
	}
	return &Shell{ctx: ctx, path: path, tty: tty}, nil
}

// Path is the shell that will be run.
func (s *Shell) Path() string {
	return s.path
}

// WithTerm sets the terminal type to advertise, as the client's TERM. Full
// screen programs consult it to decide what the display can do.
func (s *Shell) WithTerm(term string) *Shell {
	s.term = term
	return s
}

// Run runs the shell to completion and reports the status it ended with.
func (s *Shell) Run() int {
	cmd := exec.CommandContext(s.ctx, s.path, "-i")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.tty, s.tty, s.tty
	cmd.Env = s.env()

	// The shell leads a new session and takes the terminal as its own. That is
	// what gives it job control: the kernel delivers Ctrl+C and Ctrl+Z to
	// whichever job it has in the foreground, and it can move jobs there.
	cmd.SysProcAttr = controllingTTY()

	err := cmd.Run()
	if s.ctx.Err() != nil {
		return exitInterrupted
	}
	if err == nil {
		return 0
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		// Killed by a signal rather than exiting on its own.
		return exitInterrupted
	}
	fmt.Fprintf(os.Stderr, "tush: %v\n", err)
	return 1
}

// env is the environment for the shell: the host's, with the client's terminal
// type substituted. Without a usable TERM, programs fall back to the dumbest
// possible display.
func (s *Shell) env() []string {
	term := s.term
	if term == "" {
		if term = os.Getenv("TERM"); term == "" {
			term = "xterm-256color"
		}
	}

	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if len(kv) >= 5 && kv[:5] == "TERM=" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM="+term)
}
