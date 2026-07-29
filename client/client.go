package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"

	"github.com/cnuss/tush/pipeline"
)

// exitInterrupted is the conventional status for a client killed by a signal.
const exitInterrupted = 130

// detachKey is Ctrl+], the same escape telnet uses. A raw terminal forwards
// Ctrl+C to the host instead of stopping the client, so there has to be some
// other way out.
const detachKey = 0x1d

// Client is a dumb terminal. It runs no interpreter of its own: it forwards
// what the user types to the host and renders what the host sends back.
type Client struct {
	ctx  context.Context
	term *pipeline.Terminal

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func New(ctx context.Context) *Client {
	return &Client{ctx: ctx}
}

// WithTerminal sets the tunnel end the client talks to.
func (c *Client) WithTerminal(t *pipeline.Terminal) *Client {
	c.term = t
	return c
}

// WithStdio sets the local streams the user sees and types into.
func (c *Client) WithStdio(stdin io.Reader, stdout, stderr io.Writer) *Client {
	c.stdin, c.stdout, c.stderr = stdin, stdout, stderr
	return c
}

// Run forwards streams until the host hangs up, the user detaches, or the
// context is cancelled.
func (c *Client) Run() int {
	if c.term == nil || c.stdin == nil || c.stdout == nil || c.stderr == nil {
		return 1
	}

	restore, raw, err := c.makeRaw()
	if err != nil {
		fmt.Fprintf(c.stderr, "tush: %v\n", err)
		return 1
	}
	defer restore()
	if raw {
		fmt.Fprintf(c.stderr, "Press Ctrl+] to detach.\r\n")
	}

	detached := make(chan struct{})
	var once sync.Once
	detach := func() { once.Do(func() { close(detached) }) }

	// Local input travels to the host for as long as the client lives. It is
	// not waited on: it blocks reading the user's terminal, which nothing else
	// can interrupt.
	go c.forward(detach)
	go io.Copy(c.stderr, c.term.Stderr)

	hostGone := make(chan struct{})
	go func() {
		defer close(hostGone)
		io.Copy(c.stdout, c.term.Stdout)
	}()

	select {
	case <-hostGone:
		return 0
	case <-detached:
		return 0
	case <-c.ctx.Done():
		return exitInterrupted
	}
}

// forward sends what the user types to the host, watching for the detach key.
func (c *Client) forward(detach func()) {
	buf := make([]byte, 4096)
	for {
		n, err := c.stdin.Read(buf)
		if n > 0 {
			if i := bytes.IndexByte(buf[:n], detachKey); i >= 0 {
				c.term.Stdin.Write(buf[:i])
				detach()
				return
			}
			if _, werr := c.term.Stdin.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
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
