package console

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnuss/tush/shell"
)

// TestEchoesInput checks that the kernel line discipline is in play: what the
// user types comes back without the shell doing anything, which is what makes
// typing visible to a remote terminal in raw mode.
func TestEchoesInput(t *testing.T) {
	con := open(t)
	in, screen := attach(t, con)

	go shell.New(t.Context()).WithStdio(con.TTY(), con.TTY(), io.Discard).Run()

	fmt.Fprint(in, "hello")
	screen.waitFor(t, "hello")
}

// TestShellSeesATerminal checks that the shell and its commands really are on a
// terminal, which is what full-screen programs test for.
//
// The marker is written so that its echo differs from its output. The line
// discipline echoes the typed line back, so a marker that reads the same either
// way would be satisfied by the echo alone and prove nothing.
func TestShellSeesATerminal(t *testing.T) {
	con := open(t)
	in, screen := attach(t, con)

	go shell.New(t.Context()).WithStdio(con.TTY(), con.TTY(), io.Discard).Run()

	fmt.Fprintln(in, "test -t 1 && echo on-a-''tty")
	screen.waitFor(t, "on-a-tty")
}

// TestInterruptsRunningCommand checks that Ctrl+C stops the command that is
// running without ending the session.
func TestInterruptsRunningCommand(t *testing.T) {
	con := open(t)
	inR, inW := io.Pipe()
	screenR, screenW := io.Pipe()
	screen := drain(t, screenR)

	sh := shell.New(t.Context()).WithStdio(con.TTY(), con.TTY(), io.Discard)
	con.WithInterrupt(sh.Interrupt).Attach(t.Context(), inR, screenW)
	go sh.Run()

	fmt.Fprintln(inW, "echo RE''ADY")
	screen.waitFor(t, "READY")

	fmt.Fprintln(inW, "sleep 5")
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	inW.Write([]byte{interruptKey})
	fmt.Fprintln(inW, "echo BA''CK")
	screen.waitFor(t, "BACK")

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("command ran to completion instead of being interrupted, after %v", elapsed)
	}
}

// TestResize checks that the window size programs see can be changed.
func TestResize(t *testing.T) {
	con := open(t)
	in, screen := attach(t, con)

	if err := con.Resize(40, 100); err != nil {
		t.Fatal(err)
	}

	go shell.New(t.Context()).WithStdio(con.TTY(), con.TTY(), io.Discard).Run()

	fmt.Fprintln(in, "stty size")
	screen.waitFor(t, "40 100")
}

func open(t *testing.T) *Console {
	t.Helper()
	con, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { con.Close() })
	return con
}

// attach wires the console to pipes standing in for the tunnel, returning what
// to type into and what the remote terminal would show.
func attach(t *testing.T, con *Console) (io.Writer, *sink) {
	t.Helper()
	inR, inW := io.Pipe()
	screenR, screenW := io.Pipe()
	con.Attach(t.Context(), inR, screenW)
	return inW, drain(t, screenR)
}

type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func drain(t *testing.T, r io.Reader) *sink {
	t.Helper()
	s := &sink{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.buf.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *sink) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %q on the console; got %q", want, s.String())
}
