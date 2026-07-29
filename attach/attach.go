// Package attach serves a shell's terminal over the Kubernetes remote command
// protocol, the one kubelet speaks and kubectl attach talks to.
//
// The protocol multiplexes standard input, output and terminal resizes over a
// single connection, which is why tush does not have to invent framing of its
// own, and why the window size a client is actually using can reach the shell.
package attach

import (
	"context"
	"io"
	"net/http"
	"time"

	"k8s.io/cri-streaming/pkg/streaming/remotecommand"

	"github.com/cnuss/tush/console"
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

// Server hands out a console over the remote command protocol.
type Server struct {
	console *console.Console
}

func New(c *console.Console) *Server {
	return &Server{console: c}
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
		remotecommand.ServeAttach(w, r, s,
			"tush", "", "",
			opts,
			idleTimeout,
			remotecommand.DefaultStreamCreationTimeout,
			remotecommand.SupportedStreamingProtocols,
		)
	})
	return mux
}

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

	go s.follow(ctx, resize)

	err := s.console.Attach(ctx, in, out)
	if ctx.Err() != nil {
		// The client went away, which is how sessions normally end here: the
		// shell stays running for whoever attaches next.
		return nil
	}
	return err
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
