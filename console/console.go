package console

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/creack/pty"
)

// Programs behave badly against a zero-sized terminal, and nothing tells us the
// client's real size yet, so a console starts at the conventional default.
const (
	defaultRows = 24
	defaultCols = 80
)

// interruptKey is Ctrl+C. The console handles it itself rather than leaving it
// to the terminal's line discipline: a signal from the line discipline goes to
// the terminal's foreground process group, and this terminal has none, since
// the interpreter runs in-process and never claims one. Catching the byte here
// and cancelling the running command reaches the same end by another road.
const interruptKey = 0x03

// Console is a pseudo-terminal for the shell to run against. Handing the shell
// a real terminal rather than a plain pipe is what makes the kernel line
// discipline apply: input is echoed back, line editing works, and full-screen
// programs can drive the display.
type Console struct {
	master *os.File
	tty    *os.File

	onInterrupt func()
}

func Open() (*Console, error) {
	master, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	c := &Console{master: master, tty: tty}
	if err := c.Resize(defaultRows, defaultCols); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// TTY is the side the shell and the commands it runs use as their terminal.
func (c *Console) TTY() *os.File {
	return c.tty
}

// WithInterrupt sets what to do when the user presses Ctrl+C. Without it the
// key is passed through to the terminal like any other byte.
func (c *Console) WithInterrupt(fn func()) *Console {
	c.onInterrupt = fn
	return c
}

// Attach carries the console between the two streams — what the user types in,
// what the terminal paints out — until the context is done.
func (c *Console) Attach(ctx context.Context, in io.Reader, out io.Writer) {
	go c.forward(in)
	go io.Copy(out, c.master)
	go func() {
		<-ctx.Done()
		c.Close()
	}()
}

// forward writes what the user types into the terminal, pulling out the
// interrupt key on the way past.
func (c *Console) forward(in io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := in.Read(buf)
		for chunk := buf[:n]; len(chunk) > 0; {
			i := -1
			if c.onInterrupt != nil {
				i = bytes.IndexByte(chunk, interruptKey)
			}
			if i < 0 {
				c.master.Write(chunk)
				break
			}
			c.master.Write(chunk[:i])
			c.onInterrupt()
			chunk = chunk[i+1:]
		}
		if err != nil {
			return
		}
	}
}

// Resize sets the window size that programs see.
func (c *Console) Resize(rows, cols uint16) error {
	return pty.Setsize(c.master, &pty.Winsize{Rows: rows, Cols: cols})
}

func (c *Console) Close() error {
	c.tty.Close()
	return c.master.Close()
}
