package client

import (
	"context"
	"io"

	"github.com/cnuss/tush/pipeline"
)

// exitInterrupted is the conventional status for a client killed by a signal.
const exitInterrupted = 130

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

// Run forwards streams until the host hangs up or the context is cancelled.
func (c *Client) Run() int {
	if c.term == nil || c.stdin == nil || c.stdout == nil || c.stderr == nil {
		return 1
	}

	// Local input travels to the host for as long as the client lives. These
	// copies are not waited on: the first blocks reading the user's terminal,
	// which nothing else can interrupt.
	go io.Copy(c.term.Stdin, c.stdin)
	go io.Copy(c.stderr, c.term.Stderr)

	hostGone := make(chan struct{})
	go func() {
		defer close(hostGone)
		io.Copy(c.stdout, c.term.Stdout)
	}()

	select {
	case <-hostGone:
		return 0
	case <-c.ctx.Done():
		return exitInterrupted
	}
}
