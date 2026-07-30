package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scaffoldly/tush/attach"
	"github.com/scaffoldly/tush/console"
	"github.com/scaffoldly/tush/shell"
)

// Markers are written so that their echo differs from their output, since the
// terminal echoes back what is typed.

// TestAttachesToHostShell is the whole product in one test: a user types into a
// local terminal, the command runs in the shell behind the tunnel, and the
// output comes back — over the same protocol kubectl attach speaks.
func TestAttachesToHostShell(t *testing.T) {
	host := startHost(t)
	local, screen := startClient(t, t.Context(), host)

	local.typeLine("echo hello-from-a-term''inal")
	screen.waitFor(t, "hello-from-a-terminal")
}

// TestSurvivesDetach checks that a client can leave and another arrive to find
// the same shell still running, with its state intact.
func TestSurvivesDetach(t *testing.T) {
	host := startHost(t)

	firstCtx, leave := context.WithCancel(context.Background())
	first, firstScreen := startClient(t, firstCtx, host)
	first.typeLine("FOO=bar")
	first.typeLine("echo fir''st:$FOO")
	firstScreen.waitFor(t, "first:bar")

	leave()
	time.Sleep(300 * time.Millisecond)

	second, secondScreen := startClient(t, t.Context(), host)
	second.typeLine("echo seco''nd:$FOO")
	secondScreen.waitFor(t, "second:bar")
}

// TestReportsShellExitStatus checks that the status the shell ended with
// reaches the client, which attach has no field of its own to carry.
func TestReportsShellExitStatus(t *testing.T) {
	host := startHost(t)
	local, screen, status := startClientWithStatus(t, t.Context(), host)

	local.typeLine("echo re''ady")
	screen.waitFor(t, "ready")
	local.typeLine("exit 7")

	select {
	case got := <-status:
		if got != 7 {
			t.Errorf("client exit status = %d, want 7; screen was %q", got, screen.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("client did not exit; screen was %q", screen.String())
	}
}

// startHost runs a console, a shell on it, and the attach endpoint, returning
// the URL a client would be given.
func startHost(t *testing.T) *url.URL {
	t.Helper()
	if shell.Find() == "" {
		t.Skip("no shell on this host")
	}

	con, err := console.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { con.Close() })

	stopped := make(chan struct{})
	server := attach.New(con, func(term string) (<-chan int, error) {
		sh, err := shell.New(t.Context(), con.TTY())
		if err != nil {
			return nil, err
		}
		status := make(chan int, 1)
		go func() {
			defer close(stopped)
			status <- sh.WithTerm(term).Run()
		}()
		return status, nil
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: server.Handler()}
	go srv.Serve(l)

	t.Cleanup(func() {
		srv.Close()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("shell did not stop when the test ended")
		}
	})

	return &url.URL{Scheme: "http", Host: l.Addr().String()}
}

// startClient attaches a client with pipes standing in for a terminal,
// returning something to type into and what the user would see.
func startClient(t *testing.T, ctx context.Context, host *url.URL) (*keyboard, *sink) {
	t.Helper()
	local, screen, _ := startClientWithStatus(t, ctx, host)
	return local, screen
}

func startClientWithStatus(t *testing.T, ctx context.Context, host *url.URL) (*keyboard, *sink, <-chan int) {
	t.Helper()
	localR, localW := io.Pipe()
	screen := &sink{}

	status := make(chan int, 1)
	go func() {
		status <- New(ctx).WithURL(host).WithStdio(localR, screen, screen).Run()
	}()

	// Give the attach time to be established, so that a caller which types
	// immediately is not writing into a session that does not exist yet.
	time.Sleep(300 * time.Millisecond)
	return newKeyboard(localW), screen, status
}

// keyboard types into the client without blocking the test: writing to a pipe
// nobody is reading would otherwise hang forever if the client has gone. Lines
// are queued and written by one goroutine, so they arrive in the order typed.
type keyboard struct {
	lines chan string
}

func newKeyboard(w io.Writer) *keyboard {
	k := &keyboard{lines: make(chan string, 64)}
	go func() {
		for line := range k.lines {
			fmt.Fprintln(w, line)
		}
	}()
	return k
}

func (k *keyboard) typeLine(line string) {
	k.lines <- line
}

// sink accumulates everything the user is shown, so a test can wait for output
// without racing the prompts interleaved with it.
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

func (s *sink) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %q on screen; got %q", want, s.String())
}
