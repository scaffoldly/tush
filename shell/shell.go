package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// exitInterrupted is the conventional status for a shell killed by a signal.
const exitInterrupted = 130

type Shell struct {
	ctx context.Context
	ros []interp.RunnerOption

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	mu            sync.Mutex
	cancelCommand context.CancelFunc
	interrupted   bool
}

func New(ctx context.Context) *Shell {
	return &Shell{ctx: ctx}
}

// WithStdio sets the streams the shell runs against. The shell needs them
// directly as well as via [interp.StdIO]: the parser reads from stdin and
// prompts are written to stdout, and a Runner does not expose the streams it
// was configured with.
func (s *Shell) WithStdio(stdin io.Reader, stdout, stderr io.Writer) *Shell {
	s.stdin, s.stdout, s.stderr = stdin, stdout, stderr
	return s
}

// WithRunnerOption adds configuration applied after the shell's own, so a
// caller can set things like the environment, working directory, or a custom
// exec handler.
func (s *Shell) WithRunnerOption(ro interp.RunnerOption) *Shell {
	s.ros = append(s.ros, ro)
	return s
}

// Run runs the shell and reports the status it ended with.
func (s *Shell) Run() int {
	if s.stdin == nil || s.stdout == nil || s.stderr == nil {
		return 1
	}

	err := s.runAll()
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	if s.ctx.Err() != nil {
		return exitInterrupted
	}
	if err != nil {
		fmt.Fprintln(s.stderr, err)
		return 1
	}
	return 0
}

func (s *Shell) runAll() error {
	stdin, closeStdin, err := s.stdinFile()
	if err != nil {
		return err
	}
	defer closeStdin()

	opts := append([]interp.RunnerOption{
		interp.Interactive(true),
		interp.StdIO(stdin, s.stdout, s.stderr),
		s.interruptible(),
	}, s.ros...)

	r, err := interp.New(opts...)
	if err != nil {
		return err
	}
	return s.runInteractive(r, stdin)
}

// stdinFile adapts the shell's input to an [os.File]. [interp.StdIO] hands any
// other kind of reader to a goroutine that drains it, which would swallow the
// input the parser needs; a real pipe instead lets the parser and the
// interpreter share one descriptor, the way a terminal is shared.
func (s *Shell) stdinFile() (*os.File, func(), error) {
	if f, ok := s.stdin.(*os.File); ok {
		return f, func() {}, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	go func() {
		io.Copy(pw, s.stdin)
		pw.Close()
	}()
	return pr, func() { pr.Close() }, nil
}

func (s *Shell) runInteractive(r *interp.Runner, stdin io.Reader) error {
	for {
		// A parse error ends the sequence, so recovering means starting a new
		// one. The parser is rebuilt with it, discarding whatever it had
		// buffered from the bad line.
		parser := syntax.NewParser()
		mistyped := false

		fmt.Fprintf(s.stdout, "$ ")
		for stmts, err := range parser.InteractiveSeq(stdin) {
			if err != nil {
				// A syntax error is a typo, not a reason to end someone's
				// session: report it and read again.
				fmt.Fprintf(s.stderr, "%v\n", err)
				mistyped = true
				break
			}
			if parser.Incomplete() {
				fmt.Fprintf(s.stdout, "> ")
				continue
			}
			for _, stmt := range stmts {
				if done, err := s.exec(r, stmt); done {
					return err
				}
			}
			fmt.Fprintf(s.stdout, "$ ")
		}

		if !mistyped {
			// Input ended for good.
			return nil
		}
	}
}

// Interrupt stops the command that is running, if any, leaving the shell
// itself alive to prompt again. It does nothing when the shell is idle.
func (s *Shell) Interrupt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelCommand == nil {
		return
	}
	s.interrupted = true
	s.cancelCommand()
}

// interruptible runs commands under a context of the shell's own, so that an
// interrupt reaches the running command alone.
//
// The obvious approach — cancelling the context passed to [interp.Runner.Run] —
// does not work: the interpreter treats a cancelled run as fatal and marks
// itself exited, which would end the session rather than the command. Giving
// only the exec handler a cancellable context leaves the interpreter none the
// wiser, and the default handler already signals the process it started.
func (s *Shell) interruptible() interp.RunnerOption {
	return interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			s.mu.Lock()
			s.cancelCommand = cancel
			s.mu.Unlock()
			defer func() {
				s.mu.Lock()
				s.cancelCommand = nil
				s.mu.Unlock()
			}()

			err := next(ctx, args)
			if ctx.Err() != nil && s.ctx.Err() == nil {
				// Report an interrupted command the way a signalled one
				// reports itself. Any other error would be taken as the
				// handler failing, which the interpreter treats as fatal.
				return interp.ExitStatus(exitInterrupted)
			}
			return err
		}
	})
}

// exec runs one statement, reporting whether the shell should stop. A command
// that merely exits non-zero is normal and says nothing; a genuine failure is
// worth reporting to the user.
func (s *Shell) exec(r *interp.Runner, stmt *syntax.Stmt) (done bool, err error) {
	runErr := r.Run(s.ctx, stmt)

	s.mu.Lock()
	interrupted := s.interrupted
	s.interrupted = false
	s.mu.Unlock()

	if r.Exited() || s.ctx.Err() != nil {
		return true, runErr
	}
	if interrupted {
		fmt.Fprintf(s.stdout, "^C\n")
		return false, nil
	}
	var status interp.ExitStatus
	if runErr != nil && !errors.As(runErr, &status) {
		fmt.Fprintf(s.stderr, "%v\n", runErr)
	}
	return false, nil
}
