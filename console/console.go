package console

import (
	"bytes"
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

// ErrEvicted is what an attachment ends with when another client took the
// console from it.
//
// A console has one client at a time, and the newcomer wins. Refusing the
// newcomer instead would be the other choice, and was what this did: it made
// something as ordinary as refreshing a browser tab fail, because the tab
// raced the server noticing that its own previous connection had died. Whoever
// arrives last is also the one with their hands on the keyboard.
//
// Nothing is given away by letting them in. The URL is the only credential
// there is, so anyone able to take the console this way could instead attach
// normally and type exit, which ends the session outright rather than
// interrupting it.
var ErrEvicted = errors.New("another client attached")

// scrollback is how much recent output a console remembers, to replay to a
// client when it attaches. Enough to carry a screen or two of a full-screen
// program's redraw, which is what makes a reconnect land somewhere recognisable
// rather than on a blank screen.
const scrollback = 256 << 10

// sgrReset turns off any colour or style still in force.
var sgrReset = []byte("\x1b[0m")

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
	current *viewer
	history []byte

	// Resizes arrive from whichever client is attached and can land at any
	// moment, including while the console is being torn down.
	masterMu  sync.Mutex
	closed    bool
	closeOnce sync.Once
}

// viewer is one attached client: where its output goes, and a way to tell it
// that somebody else has taken the console.
type viewer struct {
	out     io.Writer
	evicted chan struct{}
	once    sync.Once
}

func (v *viewer) evict() {
	v.once.Do(func() { close(v.evicted) })
}

func (v *viewer) gone() bool {
	select {
	case <-v.evicted:
		return true
	default:
		return false
	}
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
			if c.current != nil {
				c.current.out.Write(buf[:n])
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
		c.history = fromLineStart(c.history)
		return
	}
	c.history = append(c.history, out...)
	if len(c.history) > scrollback {
		c.history = append(c.history[:0], c.history[len(c.history)-scrollback:]...)
		c.history = fromLineStart(c.history)
	}
}

// fromLineStart drops a partial first line, because dropping the oldest output
// by byte count cuts wherever it lands — including the middle of an escape
// sequence. Replaying from there prints the tail of the sequence as text, or
// worse, leaves the terminal in a mode the missing bytes were meant to end.
// Sequences do not span lines, so a line boundary is a safe place to resume.
func fromLineStart(history []byte) []byte {
	i := bytes.IndexByte(history, '\n')
	if i < 0 || i+1 >= len(history) {
		return history
	}
	return history[i+1:]
}

// Attach makes out the console's display and feeds it whatever arrives on in,
// until the client goes away, another client takes the console, or the context
// ends. The shell is left running in every case.
func (c *Console) Attach(ctx context.Context, in io.Reader, out io.Writer) error {
	v := c.claim(out)
	defer c.release(v)

	typed := make(chan error, 1)
	go func() {
		_, err := io.Copy(keystrokes{c, v}, in)
		typed <- err
	}()

	select {
	case err := <-typed:
		if v.gone() {
			return ErrEvicted
		}
		return err
	case <-v.evicted:
		return ErrEvicted
	case <-ctx.Done():
		return ctx.Err()
	}
}

// keystrokes carries one client's input to the terminal, and stops the moment
// that client stops being the attached one.
//
// Without this, a client being evicted would go on typing into the session it
// had just lost: its copying goroutine outlives the eviction by however long
// the connection takes to tear down, and anything already in flight would land
// in the new client's shell.
type keystrokes struct {
	c *Console
	v *viewer
}

func (k keystrokes) Write(p []byte) (int, error) {
	if k.v.gone() {
		return 0, ErrEvicted
	}
	return k.c.master.Write(p)
}

// claim makes out the display, taking the console from whoever held it. It
// first replays what the shell has said recently, so that the client arrives
// at a screen with something on it rather than waiting for the shell to speak
// again.
func (c *Console) claim(out io.Writer) *viewer {
	c.mu.Lock()
	defer c.mu.Unlock()

	// The newcomer wins. Whoever was here is told so, and stops being sent
	// output before the replay below goes to their replacement.
	if c.current != nil {
		c.current.evict()
	}

	if len(c.history) > 0 {
		// The scrollback resumes partway through a session, so whatever colour
		// or style was in force before it is not in the replay. Clear it rather
		// than let the client inherit the previous client's formatting.
		out.Write(sgrReset)
		out.Write(c.history)
	}

	c.current = &viewer{out: out, evicted: make(chan struct{})}
	return c.current
}

// release gives up the console, unless somebody else has already taken it —
// in which case it is theirs, not ours to clear.
func (c *Console) release(v *viewer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == v {
		c.current = nil
	}
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
