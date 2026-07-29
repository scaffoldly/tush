package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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

// exec runs one statement, reporting whether the shell should stop. A command
// that merely exits non-zero is normal and says nothing; a genuine failure is
// worth reporting to the user.
func (s *Shell) exec(r *interp.Runner, stmt *syntax.Stmt) (done bool, err error) {
	runErr := r.Run(s.ctx, stmt)
	if r.Exited() || s.ctx.Err() != nil {
		return true, runErr
	}
	var status interp.ExitStatus
	if runErr != nil && !errors.As(runErr, &status) {
		fmt.Fprintf(s.stderr, "%v\n", runErr)
	}
	return false, nil
}
