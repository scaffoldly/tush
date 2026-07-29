package shell

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
)

// TestRunsCommands drives the shell over plain in-memory pipes, with no tunnel
// involved.
func TestRunsCommands(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	sh := New(t.Context()).WithStdio(stdinR, stdoutW, stdoutW)
	go sh.Run()

	out := drain(t, stdoutR)
	fmt.Fprintln(stdinW, "echo hello")
	out.waitFor(t, "hello")
}

// TestRecoversFromSyntaxError checks that a typo reports and re-prompts rather
// than ending the session.
func TestRecoversFromSyntaxError(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	sh := New(t.Context()).WithStdio(stdinR, stdoutW, stdoutW)
	go sh.Run()

	out := drain(t, stdoutR)
	fmt.Fprintln(stdinW, "echo (")
	out.waitFor(t, "must be followed by")

	fmt.Fprintln(stdinW, "echo still-alive")
	out.waitFor(t, "still-alive")
}

// TestExitStatus checks that the exit builtin's status reaches the caller.
func TestExitStatus(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	status := make(chan int, 1)
	sh := New(t.Context()).WithStdio(stdinR, stdoutW, stdoutW)
	go func() { status <- sh.Run() }()

	drain(t, stdoutR)
	fmt.Fprintln(stdinW, "exit 3")

	select {
	case got := <-status:
		if got != 3 {
			t.Errorf("exit status = %d, want 3", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not exit")
	}
}

// TestOverTunnel runs the host path end to end: a shell behind a pipeline
// listener, driven by a client that dialled the tunnel.
func TestOverTunnel(t *testing.T) {
	ctx := t.Context()
	l := listen(t)

	host := pipeline.New(ctx).WithListener(l)
	streams := host.Stdio()
	go New(ctx).WithStdio(streams.Stdin, streams.Stdout, streams.Stderr).Run()

	client := pipeline.New(ctx).WithURL(tunnelURL(l))
	clientIO := client.Stdio()

	out := drain(t, clientIO.Stdin)
	fmt.Fprintln(clientIO.Stdout, "echo hi-from-tunnel")
	out.waitFor(t, "hi-from-tunnel")
}

// TestSurvivesReconnect is the payoff of the stable interconnect: a client can
// drop and another attach, finding the same shell with its state intact.
func TestSurvivesReconnect(t *testing.T) {
	ctx := t.Context()
	l := listen(t)

	host := pipeline.New(ctx).WithListener(l)
	streams := host.Stdio()
	go New(ctx).WithStdio(streams.Stdin, streams.Stdout, streams.Stderr).Run()

	// The first client sets a variable and confirms the shell is live.
	firstCtx, dropFirst := context.WithCancel(context.Background())
	first := pipeline.New(firstCtx).WithURL(tunnelURL(l))
	firstIO := first.Stdio()

	firstOut := drain(t, firstIO.Stdin)
	fmt.Fprintln(firstIO.Stdout, "FOO=bar")
	fmt.Fprintln(firstIO.Stdout, "echo first:$FOO")
	firstOut.waitFor(t, "first:bar")

	dropFirst()
	time.Sleep(300 * time.Millisecond)

	// The second client finds the same shell, variable and all.
	second := pipeline.New(ctx).WithURL(tunnelURL(l))
	secondIO := second.Stdio()

	secondOut := drain(t, secondIO.Stdin)
	fmt.Fprintln(secondIO.Stdout, "echo second:$FOO")
	secondOut.waitFor(t, "second:bar")
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
	t.Fatalf("never saw %q in output; got %q", want, s.String())
}
