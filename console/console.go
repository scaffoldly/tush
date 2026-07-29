package console

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/creack/pty"
)

// Programs behave badly against a zero-sized terminal, so a console starts at
// the conventional default until a client says what size it really is.
const (
	defaultRows = 24
	defaultCols = 80
)

// ErrBusy is returned when a client tries to attach to a console that already
// has one.
var ErrBusy = errors.New("console already has a client attached")

// scrollback is how much recent output a console remembers, to replay to a
// client when it attaches. Enough to carry a screen or two of a full-screen
// program's redraw, which is what makes a reconnect land somewhere recognisable
// rather than on a blank screen.
const scrollback = 256 << 10

// Console is a pseudo-terminal for the shell to run against. Handing the shell
// a real terminal rather than a plain pipe is what makes the kernel line
// discipline apply: input is echoed back, line editing works, full-screen
// programs can drive the display, and Ctrl+C reaches the running command
// through the terminal's own foreground process group.
//
// The console outlives any one client. Clients attach and detach against a
// shell that keeps running in between, so a dropped connection costs the user
// nothing but the time to reconnect.
type Console struct {
	master *os.File
	tty    *os.File

	mu      sync.Mutex
	display io.Writer
	history []byte

	// Resizes arrive from whichever client is attached and can land at any
	// moment, including while the console is being torn down.
	masterMu  sync.Mutex
	closed    bool
	closeOnce sync.Once
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
	go c.paint()
	return c, nil
}

// TTY is the side the shell and the commands it runs use as their terminal.
func (c *Console) TTY() *os.File {
	return c.tty
}

// paint carries everything the shell writes to whichever client is attached,
// and remembers the tail of it. One reader serves every client in turn, because
// a terminal cannot be read from two places at once without the two stealing
// each other's bytes.
//
// Output is never held waiting for a client: a shell left running with nobody
// attached would otherwise fill the terminal's buffer and block on its own
// output. What a departed client missed is recovered from the scrollback when
// the next one arrives, not by making the shell wait.
func (c *Console) paint() {
	buf := make([]byte, 32*1024)
	for {
		n, err := c.master.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.remember(buf[:n])
			if c.display != nil {
				c.display.Write(buf[:n])
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// remember appends to the scrollback, dropping the oldest output once it is
// full. Callers must hold mu.
func (c *Console) remember(out []byte) {
	if len(out) >= scrollback {
		c.history = append(c.history[:0], out[len(out)-scrollback:]...)
		return
	}
	c.history = append(c.history, out...)
	if len(c.history) > scrollback {
		c.history = append(c.history[:0], c.history[len(c.history)-scrollback:]...)
	}
}

// Attach makes out the console's display and feeds it whatever arrives on in,
// until the client goes away or the context ends. The shell is left running
// either way.
func (c *Console) Attach(ctx context.Context, in io.Reader, out io.Writer) error {
	if err := c.claim(out); err != nil {
		return err
	}
	defer c.release()

	typed := make(chan error, 1)
	go func() {
		_, err := io.Copy(c.master, in)
		typed <- err
	}()

	select {
	case err := <-typed:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// claim makes out the display, first replaying what the shell has said
// recently so that the client arrives at a screen with something on it rather
// than waiting for the shell to speak again.
func (c *Console) claim(out io.Writer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.display != nil {
		return ErrBusy
	}
	if len(c.history) > 0 {
		out.Write(c.history)
	}
	c.display = out
	return nil
}

func (c *Console) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.display = nil
}

// Resize sets the window size that programs see.
func (c *Console) Resize(rows, cols uint16) error {
	c.masterMu.Lock()
	defer c.masterMu.Unlock()
	if c.closed {
		return os.ErrClosed
	}
	return pty.Setsize(c.master, &pty.Winsize{Rows: rows, Cols: cols})
}

// Close releases both ends. It must not be called while the shell is still
// running, since the shell holds the terminal as its own descriptor; wait for
// the shell to return first.
func (c *Console) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Shut the door on resizes before closing, and wait for any already
		// under way: a resize reads the descriptor this is about to close.
		c.masterMu.Lock()
		c.closed = true
		c.masterMu.Unlock()

		c.tty.Close()
		err = c.master.Close()
	})
	return err
}
