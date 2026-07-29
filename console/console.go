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

// paint carries everything the shell writes to whichever client is attached.
// One reader serves every client in turn, because a terminal cannot be read
// from two places at once without the two stealing each other's bytes.
//
// Output produced while nobody is attached is dropped rather than held, so that
// a shell left running with no client cannot fill the terminal's buffer and
// then block on its own output.
func (c *Console) paint() {
	buf := make([]byte, 32*1024)
	for {
		n, err := c.master.Read(buf)
		if n > 0 {
			c.mu.Lock()
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

func (c *Console) claim(out io.Writer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.display != nil {
		return ErrBusy
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
	return pty.Setsize(c.master, &pty.Winsize{Rows: rows, Cols: cols})
}

// Close releases both ends. It must not be called while the shell is still
// running, since the shell holds the terminal as its own descriptor; wait for
// the shell to return first.
func (c *Console) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.tty.Close()
		err = c.master.Close()
	})
	return err
}
