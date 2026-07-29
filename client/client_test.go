package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnuss/tush/pipeline"
	"github.com/cnuss/tush/shell"
)

// TestDrivesHostShell is the whole product in one test: a user types into a
// local terminal, the command runs in the shell behind the tunnel, and the
// output comes back.
func TestDrivesHostShell(t *testing.T) {
	ctx := t.Context()
	l := listen(t)

	host := pipeline.New(ctx).WithListener(l)
	hostIO := host.Stdio()
	go shell.New(ctx).WithStdio(hostIO.Stdin, hostIO.Stdout, hostIO.Stderr).Run()

	local, screen := startClient(t, ctx, tunnelURL(l))

	fmt.Fprintln(local, "echo hello-from-terminal")
	screen.waitFor(t, "hello-from-terminal")

	// Host stderr reaches the terminal too, on its own connection.
	fmt.Fprintln(local, "echo oops >&2")
	screen.waitFor(t, "oops")
}

// TestExitsWhenHostGoes checks that the terminal stops when the host is gone,
// rather than waiting forever for a reconnect it cannot make.
func TestExitsWhenHostGoes(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(context.Background())
	l := listen(t)

	host := pipeline.New(hostCtx).WithListener(l)
	hostIO := host.Stdio()
	go shell.New(hostCtx).WithStdio(hostIO.Stdin, hostIO.Stdout, hostIO.Stderr).Run()

	ctx := t.Context()
	pipe := pipeline.New(ctx).WithURL(tunnelURL(l))
	localR, localW := io.Pipe()
	screenR, screenW := io.Pipe()

	status := make(chan int, 1)
	go func() {
		status <- New(ctx).
			WithTerminal(pipe.Terminal()).
			WithStdio(localR, screenW, screenW).
			Run()
	}()

	screen := drain(t, screenR)
	fmt.Fprintln(localW, "echo connected")
	screen.waitFor(t, "connected")

	stopHost()

	select {
	case got := <-status:
		if got != 0 {
			t.Errorf("client exit status = %d, want 0", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not exit after the host went away")
	}
}

// TestInterrupted checks that a cancelled context stops the client with the
// conventional signal status.
func TestInterrupted(t *testing.T) {
	ctx, interrupt := context.WithCancel(context.Background())
	l := listen(t)

	pipe := pipeline.New(ctx).WithURL(tunnelURL(l))
	localR, _ := io.Pipe()
	screenR, screenW := io.Pipe()
	drain(t, screenR)

	status := make(chan int, 1)
	go func() {
		status <- New(ctx).
			WithTerminal(pipe.Terminal()).
			WithStdio(localR, screenW, screenW).
			Run()
	}()

	interrupt()

	select {
	case got := <-status:
		if got != exitInterrupted {
			t.Errorf("client exit status = %d, want %d", got, exitInterrupted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not stop when interrupted")
	}
}

// startClient wires a client to the tunnel with pipes standing in for a
// terminal, returning what to type into and what the user would see.
func startClient(t *testing.T, ctx context.Context, u *url.URL) (io.Writer, *sink) {
	t.Helper()
	pipe := pipeline.New(ctx).WithURL(u)
	localR, localW := io.Pipe()
	screenR, screenW := io.Pipe()

	go New(ctx).
		WithTerminal(pipe.Terminal()).
		WithStdio(localR, screenW, screenW).
		Run()

	return localW, drain(t, screenR)
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func tunnelURL(l net.Listener) *url.URL {
	return &url.URL{Scheme: "http", Host: l.Addr().String()}
}

// sink accumulates everything a stream produces, so a test can wait for output
// without racing the prompts interleaved with it.
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
	t.Fatalf("never saw %q on screen; got %q", want, s.String())
}
