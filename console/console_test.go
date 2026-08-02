package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scaffoldly/tush/shell"
)

// Markers are written so that their echo differs from their output. A terminal
// echoes the typed line back, so a marker that read the same either way would
// be satisfied by the echo alone and prove nothing.

// TestRunsCommands checks that a shell on the console runs what it is told.
func TestRunsCommands(t *testing.T) {
	con := open(t)
	runShell(t, con)
	in, screen := attach(t, con)

	fmt.Fprintln(in, "echo he''llo")
	screen.waitFor(t, "hello")
}

// TestSurvivesDetach is the point of attaching rather than executing: a client
// can leave and another arrive to find the same shell, with its state intact.
func TestSurvivesDetach(t *testing.T) {
	con := open(t)
	runShell(t, con)

	firstCtx, leave := context.WithCancel(context.Background())
	first, firstScreen := attachWith(t, con, firstCtx)
	fmt.Fprintln(first, "FOO=bar")
	fmt.Fprintln(first, "echo fir''st:$FOO")
	firstScreen.waitFor(t, "first:bar")

	leave()
	time.Sleep(200 * time.Millisecond)

	second, secondScreen := attach(t, con)
	fmt.Fprintln(second, "echo seco''nd:$FOO")
	secondScreen.waitFor(t, "second:bar")
}

// TestReplaysScrollback checks that a client arriving late is shown what the
// shell said before it got there, rather than a blank screen.
func TestReplaysScrollback(t *testing.T) {
	con := open(t)
	runShell(t, con)

	firstCtx, leave := context.WithCancel(context.Background())
	first, firstScreen := attachWith(t, con, firstCtx)
	first.Write([]byte("echo remem''bered\n"))
	firstScreen.waitFor(t, "remembered")

	leave()
	time.Sleep(200 * time.Millisecond)

	_, secondScreen := attach(t, con)
	secondScreen.waitFor(t, "remembered")
}

// TestScrollbackResumesAtALine checks that dropping the oldest output does not
// cut an escape sequence in half. Cutting by byte count alone once left a
// client's first line reading "[0m-padding": the escape had lost its opening
// byte and printed as text.
func TestScrollbackResumesAtALine(t *testing.T) {
	line := []byte("ANSI-\x1b[31mred\x1b[0m-padding\n")

	for _, chunk := range []int{len(line), 4 << 10, scrollback * 2} {
		t.Run(fmt.Sprintf("written %d bytes at a time", chunk), func(t *testing.T) {
			con := &Console{}
			var batch []byte
			for len(batch) < chunk {
				batch = append(batch, line...)
			}
			for written := 0; written < scrollback*2; written += len(batch) {
				con.remember(batch)
			}

			if len(con.history) == 0 {
				t.Fatal("nothing was remembered")
			}
			if !bytes.HasPrefix(con.history, []byte("ANSI-")) {
				start := con.history
				if len(start) > 40 {
					start = start[:40]
				}
				t.Errorf("scrollback resumes mid-line at %q", start)
			}
			if len(con.history) > scrollback {
				t.Errorf("scrollback grew to %d bytes, over the %d limit", len(con.history), scrollback)
			}
		})
	}
}

// TestReplayClearsStyle checks that a client is not handed the formatting that
// was in force before it arrived.
func TestReplayClearsStyle(t *testing.T) {
	con := &Console{}
	con.remember([]byte("\x1b[31mstill red\n"))

	var screen bytes.Buffer
	con.claim(&screen)
	if !bytes.HasPrefix(screen.Bytes(), sgrReset) {
		t.Errorf("replay does not start by clearing style: %q", screen.Bytes())
	}
}

// TestNewClientEvictsTheOld checks that a console has one client at a time and
// that it is the newest one: arriving takes the console rather than being
// refused, and the client that had it is told its attachment is over.
func TestNewClientEvictsTheOld(t *testing.T) {
	con := open(t)
	runShell(t, con)

	first, firstScreen := attach(t, con)
	_, secondScreen := attach(t, con)

	select {
	case err := <-secondScreen.done:
		t.Fatalf("the newcomer's attachment ended with %v; it should have taken the console", err)
	case <-time.After(300 * time.Millisecond):
		// Still attached, which is the point.
	}

	// The first client's attachment ends, and says why.
	select {
	case err := <-firstScreen.done:
		if err != ErrEvicted {
			t.Errorf("evicted client ended with %v, want %v", err, ErrEvicted)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the evicted client was never told it had lost the console")
	}

	// And it can no longer type into the session it lost. What it types would
	// land in the shell the newcomer now holds, so that is the screen to watch:
	// the evicted client is no longer being shown anything itself.
	fmt.Fprintln(first, "echo st''rayinput")
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(secondScreen.String(), "strayinput") {
		t.Errorf("an evicted client still reached the terminal:\n%s", secondScreen.String())
	}
}

// TestResize checks that the window size programs see can be changed, which is
// what full-screen programs need in order to draw the right shape.
func TestResize(t *testing.T) {
	con := open(t)
	runShell(t, con)
	in, screen := attach(t, con)

	if err := con.Resize(40, 100); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(in, "stty size")
	screen.waitFor(t, "40 100")
}

// TestJobControl checks that Ctrl+C reaches the running command through the
// terminal itself, with nothing in tush delivering it.
func TestJobControl(t *testing.T) {
	con := open(t)
	runShell(t, con)
	in, screen := attach(t, con)

	fmt.Fprintln(in, "echo re''ady")
	screen.waitFor(t, "ready")

	fmt.Fprintln(in, "sleep 5")
	time.Sleep(500 * time.Millisecond)

	start := time.Now()
	in.Write([]byte{0x03})
	fmt.Fprintln(in, "echo ba''ck")
	screen.waitFor(t, "back")

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("command ran to completion instead of being interrupted, after %v", elapsed)
	}
}

func open(t *testing.T) *Console {
	t.Helper()
	con, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	// Registered first so it runs last: the shell holds the terminal as its own
	// descriptor and must be gone before it is closed.
	t.Cleanup(func() { con.Close() })
	return con
}

// runShell starts a shell on the console and waits, at the end of the test, for
// it to stop.
func runShell(t *testing.T, con *Console) {
	t.Helper()
	if shell.Find() == "" {
		t.Skip("no shell on this host")
	}
	sh, err := shell.New(t.Context(), con.TTY())
	if err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		sh.Run()
	}()
	t.Cleanup(func() {
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("shell did not stop when the test ended")
		}
	})
}

func attach(t *testing.T, con *Console) (io.Writer, *sink) {
	t.Helper()
	return attachWith(t, con, t.Context())
}

// attachWith attaches pipes standing in for a client, returning what to type
// into and what that client would see.
func attachWith(t *testing.T, con *Console, ctx context.Context) (io.Writer, *sink) {
	t.Helper()
	inR, inW := io.Pipe()
	screen := &sink{done: make(chan error, 1)}
	go func() { screen.done <- con.Attach(ctx, inR, screen) }()

	// Give the attach time to claim the console, so that a caller which types
	// immediately is not writing into a terminal nobody is watching.
	time.Sleep(50 * time.Millisecond)
	return inW, screen
}

// sink accumulates everything a client is shown, so a test can wait for output
// without racing the prompts interleaved with it.
type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer

	// done carries how this client's attachment ended, so a test can tell being
	// evicted from simply going away.
	done chan error
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
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
