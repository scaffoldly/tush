// Package attach serves a shell's terminal over the Kubernetes remote command
// protocol, the one kubelet speaks and kubectl attach talks to.
//
// The protocol multiplexes standard input, output and terminal resizes over a
// single connection, which is why tush does not have to invent framing of its
// own, and why the window size a client is actually using can reach the shell.
package attach

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/cri-streaming/pkg/streaming/remotecommand"
	"k8s.io/klog/v2"

	"github.com/scaffoldly/tush/console"
)

// Path is where the attach endpoint is served.
const Path = "/attach"

// idleTimeout is how long a connection may go without traffic before it is
// dropped. A terminal is idle whenever nobody is typing, so this is generous.
const idleTimeout = 4 * time.Hour

// Subprotocol is the version of the remote command protocol tush speaks: the
// binary framing, with exit codes reported on the error channel.
const Subprotocol = "v4.channel.k8s.io"

// The names the protocol gives its query parameters, kept here so the client
// and the server agree without either importing the other.
const (
	ParamStdin  = remotecommand.ExecStdinParam
	ParamStdout = remotecommand.ExecStdoutParam
	ParamStderr = remotecommand.ExecStderrParam
	ParamTTY    = remotecommand.ExecTTYParam
)

// The protocol prefixes every message with the channel it belongs to. They live
// here for the same reason the parameter names do: every client has to agree
// with the server about them, and tush now has two clients — the terminal one
// and the page served to a browser, which is handed these values rather than
// repeating them in JavaScript where they could drift unnoticed.
const (
	ChannelStdin  = 0
	ChannelStdout = 1
	ChannelStderr = 2
	ChannelError  = 3
	ChannelResize = 4
)

// KeepAlive is how often a client should send a byte of nothing when the user
// is not typing. A terminal is idle most of the time, and an intermediary will
// drop a connection it believes has gone quiet — Cloudflare's edge does so
// after about a hundred seconds, long before idleTimeout would.
//
// A WebSocket ping would be the natural thing, but the server side speaks
// golang.org/x/net/websocket, which has no ping and would never answer one.
const KeepAlive = 30 * time.Second

// ParamTerm carries the client's terminal type. It is tush's own addition: the
// protocol has no channel for the environment, and a shell needs to know what
// kind of display it is drawing to.
const ParamTerm = "term"

// Starter starts the shell for a session, on the terminal the server was given,
// reporting the status it eventually ends with.
type Starter func(term string) (status <-chan int, err error)

// ExitError reports that the shell ended, and with what status.
//
// Attach has no channel for exit codes — only exec does, and exec would mean a
// new shell per connection, which is the opposite of what this is for. So the
// status travels in the message the protocol does carry, and tush's own client
// reads it back out.
type ExitError struct {
	Code int
}

// ExitedFormat is how that status is written, and how a client reads it back.
const ExitedFormat = "shell exited with status %d"

func (e ExitError) Error() string {
	return fmt.Sprintf(ExitedFormat, e.Code)
}

// Quiet the logging the streaming library inherits from kubelet, which writes
// to stderr — the terminal the user is sitting at.
//
// It reports things that are not errors here. A second person opening the URL
// while somebody is attached is contention working as intended, and refusing
// them is the correct outcome, but it arrives as "Unhandled Error" on the
// publisher's screen. A client that closes its tab produces a socket-receive
// error on every normal disconnect. Neither is actionable, both are addressed
// to whoever runs a cluster rather than whoever published a shell, and the
// client is already told what happened over the protocol.
//
// Real failures still reach the user: they travel on the error channel to the
// client, and the ones that end a session come back through Exited.
func init() {
	var flags flag.FlagSet
	klog.InitFlags(&flags)

	// Both are needed, and the second is the one that is easy to miss:
	// stderrthreshold sends anything at or above its level to stderr whatever
	// logtostderr says, and it defaults to ERROR — precisely what is being
	// silenced here. Setting only logtostderr changes nothing.
	flags.Set("logtostderr", "false")
	flags.Set("stderrthreshold", "FATAL")

	// Belt to those braces: whatever klog decides to write later has nowhere
	// to go. Nothing today depends on it.
	klog.SetOutput(io.Discard)

	// Errors also reach this handler rather than only klog, and the default one
	// writes to stderr too — it is what produced the "Unhandled Error" line.
	runtime.ErrorHandlers = []runtime.ErrorHandler{
		func(_ context.Context, err error, msg string, keysAndValues ...any) {},
	}
}

// Server hands out a console over the remote command protocol.
type Server struct {
	console *console.Console
	start   Starter

	mu     sync.Mutex
	status <-chan int

	exited   chan int
	exitOnce sync.Once
}

func New(c *console.Console, start Starter) *Server {
	return &Server{
		console: c,
		start:   start,
		exited:  make(chan int, 1),
	}
}

// Exited reports the status the shell ended with, once it has. Nothing arrives
// until a client has attached at least once, because until then there is no
// shell.
func (s *Server) Exited() <-chan int {
	return s.exited
}

// Handler serves the attach endpoint. It is the endpoint alone rather than
// something routed, because the caller now serves a browser page beside it and
// so owns the routing.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opts, err := remotecommand.NewOptions(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// The terminal type has to reach AttachContainer, which is handed only
		// a context.
		ctx := context.WithValue(r.Context(), termKey{}, r.FormValue(ParamTerm))

		remotecommand.ServeAttach(w, r.WithContext(ctx), s,
			"tush", "", "",
			opts,
			idleTimeout,
			remotecommand.DefaultStreamCreationTimeout,
			remotecommand.SupportedStreamingProtocols,
		)
	})
}

type termKey struct{}

// AttachContainer connects one client to the console. The pod, uid and
// container names the protocol carries mean nothing here — there is only ever
// the one shell — but they are part of the interface kubelet implements.
func (s *Server) AttachContainer(
	ctx context.Context,
	_, _, _ string,
	in io.Reader,
	out, errOut io.WriteCloser,
	tty bool,
	resize <-chan remotecommand.TerminalSize,
) error {
	if out != nil {
		defer out.Close()
	}
	if errOut != nil {
		defer errOut.Close()
	}

	term, _ := ctx.Value(termKey{}).(string)
	status, err := s.shell(term)
	if err != nil {
		return err
	}

	go s.follow(ctx, resize)

	attached := make(chan error, 1)
	go func() { attached <- s.console.Attach(ctx, in, out) }()

	select {
	case code := <-status:
		// The shell ended. Tell this client what became of it, and remember for
		// anyone waiting on the session as a whole.
		s.exitOnce.Do(func() { s.exited <- code })
		return ExitError{Code: code}
	case err := <-attached:
		if ctx.Err() != nil {
			// The client went away, which is how sessions normally end here:
			// the shell stays running for whoever attaches next.
			return nil
		}
		return err
	}
}

// shell starts the shell if this is the first client, and reports where its
// status will arrive. Starting it on the first attach rather than at boot is
// what lets it inherit that client's terminal type, and means its opening
// prompt is written while somebody is there to see it.
func (s *Server) shell(term string) (<-chan int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != nil {
		return s.status, nil
	}
	status, err := s.start(term)
	if err != nil {
		return nil, err
	}
	s.status = status
	return status, nil
}

// follow applies the window sizes a client reports, so that full-screen
// programs draw to the shape of the terminal the user is actually looking at.
func (s *Server) follow(ctx context.Context, resize <-chan remotecommand.TerminalSize) {
	for {
		select {
		case size, ok := <-resize:
			if !ok {
				return
			}
			s.console.Resize(size.Height, size.Width)
		case <-ctx.Done():
			return
		}
	}
}
