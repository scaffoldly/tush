package progress

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPlainWhenNotATerminal checks that a step going somewhere other than a
// terminal prints one plain line. Animation frames and carriage returns in a
// log file or a CI transcript are noise.
func TestPlainWhenNotATerminal(t *testing.T) {
	var out bytes.Buffer
	r := New(&out)
	r.Step("Publishing a shell...")
	r.Stop()

	got := out.String()
	if want := "Publishing a shell...\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if strings.ContainsRune(got, '\r') {
		t.Errorf("output carries a carriage return: %q", got)
	}
	for _, frame := range frames {
		if strings.ContainsRune(got, frame) {
			t.Errorf("output carries animation frame %q: %q", frame, got)
		}
	}
}

// TestStopIsSafeWithoutAStep checks that stopping when nothing is running is a
// no-op rather than a panic on a closed channel.
func TestStopIsSafeWithoutAStep(t *testing.T) {
	var out bytes.Buffer
	r := New(&out)
	r.Stop()
	r.Stop()

	if out.Len() != 0 {
		t.Errorf("stopping without a step wrote %q", out.String())
	}
}

// TestStepEndsThePrevious checks that steps replace one another, so a sequence
// of them cannot leave two animations running against the same stream.
func TestStepEndsThePrevious(t *testing.T) {
	var out bytes.Buffer
	r := New(&out)
	r.Step("first...")
	r.Step("second...")
	r.Stop()

	got := out.String()
	if want := "first...\nsecond...\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestAnimatesOnATerminal checks the other branch: against a terminal a step
// animates in place and leaves the line blank, so what prints next starts on a
// clean line.
func TestAnimatesOnATerminal(t *testing.T) {
	tty, screen := terminal(t)

	r := New(tty)
	r.Step("Working")
	time.Sleep(3 * tick)
	r.Stop()

	// Stop blanks the line it drew on, so the last thing written returns the
	// cursor to the start of an empty line. Wait for it: the writes cross a
	// pseudo-terminal, so they arrive slightly after the call returns.
	got := screen.await(t, func(s string) bool { return strings.HasSuffix(s, "\r") })

	if !strings.Contains(got, "Working") {
		t.Errorf("never announced the step: %q", got)
	}
	if !strings.ContainsRune(got, frames[0]) {
		t.Errorf("never animated: %q", got)
	}
	if tail := got[strings.LastIndex(got[:len(got)-1], "\r"):]; strings.Contains(tail, "Working") {
		t.Errorf("left the step on screen: %q", got)
	}
}

// terminal returns a pseudo-terminal to write to and a sink holding whatever
// was written to it, since animation only runs against a real terminal.
func terminal(t *testing.T) (*os.File, *sink) {
	t.Helper()
	master, tty, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	t.Cleanup(func() {
		tty.Close()
		master.Close()
	})

	s := &sink{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				s.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return tty, s
}

type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

// await waits for what has been written to satisfy want, since writes cross a
// pseudo-terminal and arrive a moment after the call that made them returns.
func (s *sink) await(t *testing.T, want func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := s.String()
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting on the terminal; saw %q", got)
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}
