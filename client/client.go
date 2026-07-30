// Package client is the terminal end of a tush session.
//
// It runs no interpreter: it forwards what the user types to the host and
// renders what comes back, speaking the Kubernetes remote command protocol so
// that the host is talking to something kubectl attach could stand in for.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/term"

	"github.com/scaffoldly/tush/attach"
)

// exitInterrupted is the conventional status for a client killed by a signal.
const exitInterrupted = 130

// subprotocol is the version of the remote command protocol tush speaks: the
// binary framing, with exit codes reported on the error channel.
const subprotocol = "v4.channel.k8s.io"

// The protocol prefixes every message with the channel it belongs to.
const (
	channelStdin  = 0
	channelStdout = 1
	channelStderr = 2
	channelError  = 3
	channelResize = 4
)

// shellExited marks where, in a message the protocol has wrapped in prose of
// its own, the host's report of the shell's status begins.
var shellExited, shellExitedFormat = func() (string, string) {
	whole := fmt.Sprintf(attach.ExitedFormat, 0)
	return whole[:strings.LastIndex(whole, "0")], attach.ExitedFormat
}()

// keepAliveInterval is how often to send a byte of nothing when the user is not
// typing. A terminal is idle most of the time, and an intermediary will drop a
// connection it believes has gone quiet — Cloudflare's edge does so after about
// a hundred seconds, long before tush's own timeout would.
const keepAliveInterval = 30 * time.Second

// detachPrefix and detachSuffix are Ctrl+P then Ctrl+Q, the sequence docker
// attach uses. A raw terminal forwards Ctrl+C to the host rather than stopping
// the client, so there has to be some other way out.
//
// A pair rather than a single key, because any single key worth pressing is
// already spoken for: Ctrl+D is end of input and half-page-scroll, and some
// terminals swallow it before it arrives at all. Only the pair is taken; a
// lone Ctrl+P reaches the host like anything else.
const (
	detachPrefix = 0x10
	detachSuffix = 0x11
)

// Client is a dumb terminal attached to a shell on the far end of a tunnel.
type Client struct {
	ctx context.Context
	url *url.URL

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func New(ctx context.Context) *Client {
	return &Client{ctx: ctx}
}

// WithURL sets the tunnel to attach to.
func (c *Client) WithURL(u *url.URL) *Client {
	c.url = u
	return c
}

// WithStdio sets the local streams the user sees and types into.
func (c *Client) WithStdio(stdin io.Reader, stdout, stderr io.Writer) *Client {
	c.stdin, c.stdout, c.stderr = stdin, stdout, stderr
	return c
}

// Run attaches to the host and stays until the shell ends, the user detaches,
// or the context is cancelled.
func (c *Client) Run() int {
	if c.url == nil || c.stdin == nil || c.stdout == nil || c.stderr == nil {
		return 1
	}

	conn, _, err := websocket.Dial(c.ctx, c.endpoint().String(), &websocket.DialOptions{
		Subprotocols: []string{subprotocol},
	})
	if err != nil {
		fmt.Fprintf(c.stderr, "tush: %v\n", err)
		return 1
	}
	defer conn.CloseNow()
	// A terminal session is one long message per keystroke and per screen
	// update, but a program painting a full screen can produce a large one.
	conn.SetReadLimit(32 << 20)

	restore, raw, err := c.makeRaw()
	if err != nil {
		fmt.Fprintf(c.stderr, "tush: %v\n", err)
		return 1
	}
	defer restore()
	if raw {
		fmt.Fprintf(c.stderr, "Press Ctrl+P Ctrl+Q to detach.\r\n")
		c.reportSize(conn)
		// The window can be resized at any point in the session, and the shell
		// only learns of it if the client says so.
		watchResize(c.ctx, func() { c.reportSize(conn) })
	}

	detached := make(chan struct{})
	var once sync.Once
	detach := func() { once.Do(func() { close(detached) }) }
	go c.forward(conn, detach)
	go c.keepAlive(conn)

	status := make(chan int, 1)
	go func() { status <- c.render(conn) }()

	select {
	case code := <-status:
		return code
	case <-detached:
		conn.Close(websocket.StatusNormalClosure, "detached")
		return 0
	case <-c.ctx.Done():
		return exitInterrupted
	}
}

// endpoint is the attach URL for the tunnel, as a WebSocket address.
func (c *Client) endpoint() *url.URL {
	u := c.url.JoinPath(attach.Path)
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	q := u.Query()
	q.Set(attach.ParamStdin, "1")
	q.Set(attach.ParamStdout, "1")
	q.Set(attach.ParamTTY, "1")
	if term := os.Getenv("TERM"); term != "" {
		q.Set(attach.ParamTerm, term)
	}
	u.RawQuery = q.Encode()
	return u
}

// render paints what the host sends and reports the status it ended with.
func (c *Client) render(conn *websocket.Conn) int {
	for {
		_, frame, err := conn.Read(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				return exitInterrupted
			}
			return 0
		}
		if len(frame) == 0 {
			continue
		}
		payload := frame[1:]
		switch frame[0] {
		case channelStdout:
			c.stdout.Write(payload)
		case channelStderr:
			c.stderr.Write(payload)
		case channelError:
			return exitStatus(payload)
		}
	}
}

// forward sends what the user types to the host, watching for the sequence that
// means detach.
func (c *Client) forward(conn *websocket.Conn, detach func()) {
	buf := make([]byte, 4096)

	// held is a trailing Ctrl+P, kept back until the next key says whether it
	// began the sequence or was just a keystroke.
	var held []byte

	for {
		n, err := c.stdin.Read(buf)
		if n > 0 {
			var send []byte
			var detached bool
			send, detached, held = splitDetach(append(held, buf[:n]...))

			if len(send) > 0 {
				if err := c.send(conn, channelStdin, send); err != nil {
					return
				}
			}
			if detached {
				detach()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// splitDetach separates what should reach the host from the detach sequence and
// from an escape still in progress. A Ctrl+P at the very end is held back, since
// only the next key can say what it meant.
func splitDetach(typed []byte) (send []byte, detach bool, held []byte) {
	for i := 0; i < len(typed); i++ {
		if typed[i] != detachPrefix {
			continue
		}
		if i == len(typed)-1 {
			return typed[:i], false, []byte{detachPrefix}
		}
		if typed[i+1] == detachSuffix {
			return typed[:i], true, nil
		}
	}
	return typed, false, nil
}

// keepAlive keeps the connection warm while the user is not typing. The frame
// carries nothing at all — not even a channel — which the host skips without
// touching the session; its only purpose is to be traffic.
func (c *Client) keepAlive(conn *websocket.Conn) {
	tick := time.NewTicker(keepAliveInterval)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			if err := conn.Write(c.ctx, websocket.MessageBinary, nil); err != nil {
				return
			}
		case <-c.ctx.Done():
			return
		}
	}
}

// send writes one frame on the given channel.
func (c *Client) send(conn *websocket.Conn, channel byte, payload []byte) error {
	frame := make([]byte, 0, len(payload)+1)
	frame = append(frame, channel)
	frame = append(frame, payload...)
	return conn.Write(c.ctx, websocket.MessageBinary, frame)
}

// reportSize tells the host how big the user's terminal is, so that full-screen
// programs draw to the right shape.
func (c *Client) reportSize(conn *websocket.Conn) {
	f, ok := c.stdin.(*os.File)
	if !ok {
		return
	}
	cols, rows, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return
	}
	size, err := json.Marshal(struct {
		Width  uint16
		Height uint16
	}{Width: uint16(cols), Height: uint16(rows)})
	if err != nil {
		return
	}
	c.send(conn, channelResize, size)
}

// makeRaw puts a real terminal into raw mode so keystrokes reach the host as
// they are typed, and the host's line discipline — not ours — decides what they
// mean. Input that is not a terminal is left alone.
func (c *Client) makeRaw() (restore func(), raw bool, err error) {
	f, ok := c.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return func() {}, false, nil
	}
	state, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return nil, false, err
	}
	return func() { term.Restore(int(f.Fd()), state) }, true, nil
}

// exitStatus reads the status the host reported when the session ended. A
// success marker means a clean end; otherwise the details may name an exit
// code, and failing that the host writes the shell's status into the message,
// since attach has no field of its own for one.
func exitStatus(payload []byte) int {
	var status struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Details struct {
			Causes []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"causes"`
		} `json:"details"`
	}
	if err := json.Unmarshal(payload, &status); err != nil {
		return 0
	}
	if status.Status == "Success" {
		return 0
	}
	for _, cause := range status.Details.Causes {
		if cause.Reason != "ExitCode" {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(cause.Message, "%d", &code); err == nil {
			return code
		}
	}
	// The protocol wraps the host's message in prose of its own, so look for
	// the report rather than expecting it to stand alone.
	if at := strings.LastIndex(status.Message, shellExited); at >= 0 {
		var code int
		if _, err := fmt.Sscanf(status.Message[at:], shellExitedFormat, &code); err == nil {
			return code
		}
	}
	return 1
}
