// Package attach serves a shell's terminal over the Kubernetes remote command
// protocol, the one kubelet speaks and kubectl attach talks to.
//
// The protocol multiplexes standard input, output and terminal resizes over a
// single connection, which is why tush does not have to invent framing of its
// own, and why the window size a client is actually using can reach the shell.
package attach

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"k8s.io/cri-streaming/pkg/streaming/remotecommand"

	"github.com/scaffoldly/tush/console"
)

// Path is where the attach endpoint is served.
const Path = "/attach"

// idleTimeout is how long a connection may go without traffic before it is
// dropped. A terminal is idle whenever nobody is typing, so this is generous.
const idleTimeout = 4 * time.Hour

// The names the protocol gives its query parameters, kept here so the client
// and the server agree without either importing the other.
const (
	ParamStdin  = remotecommand.ExecStdinParam
	ParamStdout = remotecommand.ExecStdoutParam
	ParamStderr = remotecommand.ExecStderrParam
	ParamTTY    = remotecommand.ExecTTYParam
)

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

// Handler serves the attach endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
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
	return mux
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
