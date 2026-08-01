package attach

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/scaffoldly/tush/console"
	"github.com/scaffoldly/tush/shell"
)

// The protocol prefixes every message with the channel it belongs to.
const (
	channelStdin  = 0
	channelStdout = 1
	channelResize = 4
)

// TestResizeReachesTheShell checks that a window size reported over the
// protocol reaches the terminal, which is what full-screen programs read to
// decide what shape to draw.
func TestResizeReachesTheShell(t *testing.T) {
	conn, screen := attachTo(t, startHost(t))

	size, err := json.Marshal(struct{ Width, Height uint16 }{Width: 100, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, channelResize, size)
	time.Sleep(200 * time.Millisecond)

	// The marker is written so its echo differs from its output; a terminal
	// echoes the typed line back on the same stream.
	send(t, conn, channelStdin, []byte("stty si''ze\n"))
	screen.waitFor(t, "40 100")
}

// TestTermReachesTheShell checks that the terminal type a client reports is the
// one the shell runs with, which is what programs consult to decide what the
// display can do.
func TestTermReachesTheShell(t *testing.T) {
	conn, screen := attachTo(t, startHost(t)+"&"+ParamTerm+"=tush-test-term")

	send(t, conn, channelStdin, []byte("echo te''rm:$TERM\n"))
	screen.waitFor(t, "term:tush-test-term")
}

// TestSecondClientRefused checks that a client cannot attach on top of another
// and steal half its session.
func TestSecondClientRefused(t *testing.T) {
	host := startHost(t)
	attachTo(t, host)

	conn, _, err := websocket.Dial(t.Context(), host, &websocket.DialOptions{
		Subprotocols: []string{"v4.channel.k8s.io"},
	})
	if err != nil {
		// Refused outright is a fine way to say no.
		return
	}
	defer conn.CloseNow()

	// Otherwise the session must end promptly rather than sharing the console.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.Read(t.Context()); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("second client was left attached alongside the first")
	}
}

// TestReleasesAfterAbruptDisconnect checks that a client which vanishes without
// closing cleanly — killed, or its network pulled — does not leave the console
// claimed against everyone who comes after it.
func TestReleasesAfterAbruptDisconnect(t *testing.T) {
	host := startHost(t)

	conn, _, err := websocket.Dial(t.Context(), host, &websocket.DialOptions{
		Subprotocols: []string{"v4.channel.k8s.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, channelStdin, []byte("echo att''ached\n"))
	time.Sleep(300 * time.Millisecond)

	// Drop the connection without a closing handshake, the way a killed
	// process would.
	conn.CloseNow()

	// The next client should get in promptly rather than waiting out a socket
	// timeout.
	deadline := time.Now().Add(10 * time.Second)
	for {
		next, _, err := websocket.Dial(t.Context(), host, &websocket.DialOptions{
			Subprotocols: []string{"v4.channel.k8s.io"},
		})
		if err == nil {
			next.CloseNow()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("console still claimed by the departed client: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRefusalIsQuiet checks that turning a second client away writes nothing to
// the terminal the publisher is sitting at.
//
// The streaming library is kubelet's, and logs the way kubelet does: to stderr,
// addressed to whoever runs a cluster. Here that is somebody's shell. A second
// person opening the URL while one is attached is contention working correctly,
// and it arrived on the publisher's screen as "Unhandled Error" followed by a
// socket-receive error — for an outcome that is not an error and that the
// client is already told about over the protocol.
func TestRefusalIsQuiet(t *testing.T) {
	host := startHost(t)

	first, _ := attachTo(t, host)
	defer first.CloseNow()

	// Let the first client take the console before crowding it.
	time.Sleep(300 * time.Millisecond)

	said := captureStderr(t, func() {
		second, _, err := websocket.Dial(t.Context(), host, &websocket.DialOptions{
			Subprotocols: []string{"v4.channel.k8s.io"},
		})
		if err == nil {
			// Read until the far end gives up on us, which is when the refusal
			// has been fully processed and anything it logs has been written.
			for {
				if _, _, err := second.Read(t.Context()); err != nil {
					break
				}
			}
			second.CloseNow()
		}
		time.Sleep(300 * time.Millisecond)
	})

	if said != "" {
		t.Errorf("refusing a second client wrote to the publisher's terminal:\n%s", said)
	}
}

// captureStderr returns whatever the process writes to stderr while f runs.
//
// The real descriptor is swapped rather than a logger reconfigured, because
// what matters is what reaches the terminal, whichever library decided to write
// it and however it got hold of the stream.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()

	read := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		read <- buf.String()
	}()

	f()

	w.Close()
	out := <-read
	r.Close()
	return out
}

// startHost runs a console, a shell on it, and the attach endpoint, returning
// the WebSocket address a client would dial.
func startHost(t *testing.T) string {
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
	server := New(con, func(term string) (<-chan int, error) {
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

	srv := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("shell did not stop when the test ended")
		}
	})

	return "ws" + strings.TrimPrefix(srv.URL, "http") +
		Path + "?" + ParamStdin + "=1&" + ParamStdout + "=1&" + ParamTTY + "=1"
}

func attachTo(t *testing.T, url string) (*websocket.Conn, *sink) {
	t.Helper()
	conn, _, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{
		Subprotocols: []string{"v4.channel.k8s.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	screen := &sink{}
	go func() {
		for {
			_, frame, err := conn.Read(t.Context())
			if err != nil {
				return
			}
			if len(frame) > 1 && frame[0] == channelStdout {
				screen.Write(frame[1:])
			}
		}
	}()

	// Give the attach time to be established before anything is typed.
	time.Sleep(300 * time.Millisecond)
	return conn, screen
}

func send(t *testing.T, conn *websocket.Conn, channel byte, payload []byte) {
	t.Helper()
	frame := append([]byte{channel}, payload...)
	if err := conn.Write(t.Context(), websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
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
