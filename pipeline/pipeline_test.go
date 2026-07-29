package pipeline

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestSeam checks that bytes flow both directions through the interconnect,
// with stderr arriving on its own connection.
func TestSeam(t *testing.T) {
	ctx := t.Context()

	l := listen(t)
	server := New(ctx).WithListener(l)
	client := New(ctx).WithURL(tunnelURL(l))

	serverStdio, clientStdio := server.Stdio(), client.Stdio()

	// client -> server, over the stdio connection
	go fmt.Fprintln(clientStdio.Stdout, "ping")
	if got := readLine(t, serverStdio.Stdin); got != "ping" {
		t.Errorf("server read %q, want %q", got, "ping")
	}

	// server -> client, over the stdio connection
	go fmt.Fprintln(serverStdio.Stdout, "pong")
	if got := readLine(t, clientStdio.Stdin); got != "pong" {
		t.Errorf("client read %q, want %q", got, "pong")
	}

	// server stderr arrives on the separate stderr connection, unmixed with
	// what went over stdout.
	go fmt.Fprintln(serverStdio.Stderr, "boom")
	if got := readLine(t, client.Terminal().Stderr); got != "boom" {
		t.Errorf("client stderr read %q, want %q", got, "boom")
	}
}

// TestReconnect is the point of the stable interconnect: a client can drop and
// a new one attach, while the consumer's streams carry on untouched.
func TestReconnect(t *testing.T) {
	ctx := t.Context()

	l := listen(t)
	server := New(ctx).WithListener(l)
	server.halfOpenTimeout = 100 * time.Millisecond
	serverStdio := server.Stdio()

	firstCtx, dropFirst := context.WithCancel(context.Background())
	first := New(firstCtx).WithURL(tunnelURL(l))

	go fmt.Fprintln(serverStdio.Stdout, "before")
	if got := readLine(t, first.Stdio().Stdin); got != "before" {
		t.Fatalf("first client read %q, want %q", got, "before")
	}

	// The first client goes away.
	dropFirst()
	time.Sleep(300 * time.Millisecond)

	// A new client attaches to the same, still-running consumer.
	second := New(t.Context()).WithURL(tunnelURL(l))
	secondStdio := second.Stdio()

	go fmt.Fprintln(serverStdio.Stdout, "after")
	if got := readLine(t, secondStdio.Stdin); got != "after" {
		t.Errorf("second client read %q, want %q", got, "after")
	}

	// And input still reaches the consumer, which never saw an EOF.
	go fmt.Fprintln(secondStdio.Stdout, "still here")
	if got := readLine(t, serverStdio.Stdin); got != "still here" {
		t.Errorf("server read %q, want %q", got, "still here")
	}
}

// TestSessionMismatch checks that once one client has claimed a session, a
// second client cannot attach to the still-unconnected half of it.
func TestSessionMismatch(t *testing.T) {
	ctx := t.Context()

	l := listen(t)
	New(ctx).WithListener(l)
	base := "ws://" + l.Addr().String()

	// First client claims the session on the stdio path only.
	conn, _, err := websocket.Dial(ctx, base+"/"+pathStdio+"?"+paramSession+"=aaaa", nil)
	if err != nil {
		t.Fatalf("first client dial: %v", err)
	}
	defer conn.CloseNow()

	// A different client tries the still-free stderr path.
	if _, _, err := websocket.Dial(ctx, base+"/"+pathStderr+"?"+paramSession+"=bbbb", nil); err == nil {
		t.Fatal("second client attached to a claimed session, want rejection")
	}

	// The original client can still complete its own session.
	conn2, _, err := websocket.Dial(ctx, base+"/"+pathStderr+"?"+paramSession+"=aaaa", nil)
	if err != nil {
		t.Fatalf("first client stderr dial: %v", err)
	}
	conn2.CloseNow()
}

// TestHalfOpenTimeout checks that a client which claims a session and then
// stalls — holding one path open without ever connecting the second — is
// eventually released, letting a later client claim the server. A client that
// disconnects outright is a different case, handled promptly by endSession.
func TestHalfOpenTimeout(t *testing.T) {
	ctx := t.Context()

	l := listen(t)
	p := New(ctx)
	p.halfOpenTimeout = 100 * time.Millisecond
	p.WithListener(l)
	base := "ws://" + l.Addr().String()

	// A client claims the session on one path and then stalls, holding the
	// connection open but never completing the session.
	stalled, _, err := websocket.Dial(ctx, base+"/"+pathStdio+"?"+paramSession+"=aaaa", nil)
	if err != nil {
		t.Fatalf("stalled client dial: %v", err)
	}
	defer stalled.CloseNow()

	// Before the timeout, the session still belongs to the vanished client.
	if _, _, err := websocket.Dial(ctx, base+"/"+pathStderr+"?"+paramSession+"=bbbb", nil); err == nil {
		t.Fatal("second client attached before the half-open timeout elapsed")
	}

	time.Sleep(300 * time.Millisecond)

	// After it, a fresh client gets a whole session.
	client := New(ctx).WithURL(tunnelURL(l))
	go fmt.Fprintln(client.Stdio().Stdout, "ping")
	if got := readLine(t, p.Stdio().Stdin); got != "ping" {
		t.Errorf("server read %q, want %q", got, "ping")
	}
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

// readLine reads one line, failing the test rather than hanging if the
// interconnect never delivers it.
func readLine(t *testing.T, r io.Reader) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 256)
		n, err := r.Read(buf)
		line := string(buf[:n])
		for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
			line = line[:len(line)-1]
		}
		ch <- result{line, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read: %v", res.err)
		}
		return res.line
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a line")
		return ""
	}
}
