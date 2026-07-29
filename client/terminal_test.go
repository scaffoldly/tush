//go:build unix

package client

import (
	"context"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// These tests give the client a terminal of its own rather than a pipe, which
// is the only way to reach the parts that only run against one: raw mode, the
// detach key, and reporting the window size.

// TestDetachKeyLeavesShellRunning checks that Ctrl+D ends the client and no
// more than the client — the shell it was attached to carries on.
func TestDetachKeyLeavesShellRunning(t *testing.T) {
	host := startHost(t)
	screen, status := startClientOnTerminal(t, t.Context(), host)

	screen.typeLine("echo att''ached")
	screen.waitFor(t, "attached")

	screen.press(detachKey)

	select {
	case code := <-status:
		if code != 0 {
			t.Errorf("client exit status = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("client did not detach; screen was %q", screen.String())
	}

	// The shell should still be there for whoever attaches next.
	next, nextScreen := startClient(t, t.Context(), host)
	next.typeLine("echo still-ali''ve")
	nextScreen.waitFor(t, "still-alive")
}

// TestReportsWindowSizeAtConnect checks that the size of the user's terminal
// reaches the shell when the session starts.
func TestReportsWindowSizeAtConnect(t *testing.T) {
	host := startHost(t)
	screen, _ := startClientOnTerminal(t, t.Context(), host, func(master *os.File) {
		if err := pty.Setsize(master, &pty.Winsize{Rows: 41, Cols: 101}); err != nil {
			t.Fatal(err)
		}
	})

	screen.typeLine("stty si''ze")
	screen.waitFor(t, "41 101")
}

// TestFollowsWindowSize checks that a window resized mid-session reaches the
// shell, which it only does because the client watches for the signal.
func TestFollowsWindowSize(t *testing.T) {
	host := startHost(t)
	screen, _ := startClientOnTerminal(t, t.Context(), host)

	screen.typeLine("echo re''ady")
	screen.waitFor(t, "ready")

	if err := pty.Setsize(screen.master, &pty.Winsize{Rows: 33, Cols: 99}); err != nil {
		t.Fatal(err)
	}
	// The kernel signals the terminal's foreground process group; in a test the
	// client shares this process, so the signal is raised directly.
	syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	time.Sleep(300 * time.Millisecond)

	screen.typeLine("stty si''ze")
	screen.waitFor(t, "33 99")
}

// terminal is a client running against a real pseudo-terminal, driven from the
// other end of it: writing is typing, reading is what the user sees.
type terminal struct {
	master *os.File
	*sink
}

func (s *terminal) typeLine(line string) {
	s.master.WriteString(line + "\n")
}

func (s *terminal) press(key byte) {
	s.master.Write([]byte{key})
}

func startClientOnTerminal(t *testing.T, ctx context.Context, host *url.URL, before ...func(*os.File)) (*terminal, <-chan int) {
	t.Helper()
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tty.Close()
		master.Close()
	})
	for _, prepare := range before {
		prepare(master)
	}

	screen := &terminal{master: master, sink: &sink{}}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				screen.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	status := make(chan int, 1)
	go func() {
		status <- New(ctx).WithURL(host).WithStdio(tty, tty, tty).Run()
	}()

	// Give the attach time to be established before anything is typed.
	time.Sleep(400 * time.Millisecond)
	return screen, status
}
