// Package e2e exercises tush over a real tunnel.
//
// These tests are skipped unless TUSH_E2E is set, because they need the network
// and a tunnel provider. They cover only what a loopback listener cannot: the
// public URL's scheme, TLS terminated at the edge with plain HTTP behind it,
// and a WebSocket upgrade surviving the trip. Everything else is tested against
// a local listener, where it is faster and deterministic.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cnuss/tush/attach"
	"github.com/cnuss/tush/client"
	"github.com/cnuss/tush/console"
	"github.com/cnuss/tush/shell"
	"github.com/cnuss/tush/tunnel"
)

// provider is the tunnel provider to publish through.
const provider = "tunnel.pizza"

// TestOverARealTunnel attaches to a shell published on a real tunnel and runs a
// command through it.
func TestOverARealTunnel(t *testing.T) {
	host := publish(t)
	local, screen := attachClient(t, t.Context(), host)

	// The marker is written so its echo differs from its output, since the
	// terminal echoes back what is typed.
	local.typeLine("echo over-a-real-tun''nel")
	screen.waitFor(t, "over-a-real-tunnel")
}

// TestSurvivesDetachOverARealTunnel checks that the session outlives a client
// across the real network, not just a loopback connection.
func TestSurvivesDetachOverARealTunnel(t *testing.T) {
	host := publish(t)

	firstCtx, leave := context.WithCancel(context.Background())
	first, firstScreen := attachClient(t, firstCtx, host)
	first.typeLine("FOO=bar")
	first.typeLine("echo fir''st:$FOO")
	firstScreen.waitFor(t, "first:bar")

	leave()
	time.Sleep(time.Second)

	second, secondScreen := attachClient(t, t.Context(), host)
	second.typeLine("echo seco''nd:$FOO")
	secondScreen.waitFor(t, "second:bar")
}

// TestIdleSessionSurvives checks that a session left quiet is still usable
// afterwards. The edge may drop a WebSocket it considers idle long before
// tush's own timeout, which would strand a user mid-session.
func TestIdleSessionSurvives(t *testing.T) {
	idle := 3 * time.Minute
	if testing.Short() {
		t.Skip("idle test takes minutes")
	}

	host := publish(t)
	local, screen := attachClient(t, t.Context(), host)
	local.typeLine("echo be''fore")
	screen.waitFor(t, "before")

	t.Logf("idling for %s", idle)
	time.Sleep(idle)

	local.typeLine("echo af''ter")
	screen.waitFor(t, "after")
}

// publish runs a console, a shell on it, and the attach endpoint behind a real
// tunnel, returning the public URL.
func publish(t *testing.T) *url.URL {
	t.Helper()
	if os.Getenv("TUSH_E2E") == "" {
		t.Skip("set TUSH_E2E=1 to run tests against a real tunnel")
	}
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

	tun := tunnel.New(t.Context(), provider)
	srv := &http.Server{Handler: server.Handler()}
	go srv.Serve(tun.Listener())
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
		}
	})

	raw := tun.URL()
	t.Logf("published at %s", raw)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// attachClient attaches a client with pipes standing in for a terminal.
func attachClient(t *testing.T, ctx context.Context, host *url.URL) (*keyboard, *sink) {
	t.Helper()
	localR, localW := io.Pipe()
	screen := &sink{}

	go client.New(ctx).WithURL(host).WithStdio(localR, screen, screen).Run()

	// Give the attach time to cross the network before anything is typed.
	time.Sleep(3 * time.Second)
	return newKeyboard(localW), screen
}

// keyboard types without blocking the test: writing to a pipe nobody is reading
// would hang forever if the client has gone. One goroutine drains the queue, so
// lines arrive in the order typed.
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

// sink accumulates everything the user is shown.
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
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("never saw %q on screen; got %q", want, s.String())
}
