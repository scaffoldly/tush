package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"mvdan.cc/sh/v3/interp"
)

// A session is carried by two WebSocket connections: pathStdio is full-duplex
// (stdin one way, stdout the other) and pathStderr carries stderr alone, so the
// two output streams stay distinguishable without framing. Both carry the same
// paramSession value so the server can tell that they belong to one client.
const (
	pathStdio    = "stdio"
	pathStderr   = "stderr"
	paramSession = "session"
)

// defaultHalfOpenTimeout bounds how long a client may hold a session after
// connecting only one of its two paths. Without it, a client that connects and
// then dies would wedge the server against every later client.
const defaultHalfOpenTimeout = 30 * time.Second

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Stdio is the trio of streams the consumer runs against. They are the stable
// side of the interconnect: they outlive any single client, so a disconnect
// does not tear down the shell behind them.
type Stdio struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Terminal is the client's end of the interconnect, the mirror image of Stdio:
// the client writes the keystrokes it collects and reads back what the host
// produced.
type Terminal struct {
	Stdin  io.Writer
	Stdout io.Reader
	Stderr io.Reader
}

// pipe is one stream of the stable interconnect. The consumer holds one end for
// its whole life while sessions attach and detach at the other.
type pipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newPipe() *pipe {
	r, w := io.Pipe()
	return &pipe{r: r, w: w}
}

// close unblocks both ends, whichever the local role happens to hold.
func (p *pipe) close() {
	p.r.Close()
	p.w.Close()
}

// channel holds one side of the session until a connection arrives for it. A
// channel can be reset and refilled, so that a departed client does not leave
// its half permanently occupied.
type channel struct {
	mu      sync.Mutex
	done    chan struct{}
	release chan struct{}
	conn    net.Conn
	err     error
	filled  bool
}

func newChannel() *channel {
	return &channel{done: make(chan struct{})}
}

// provide fills the channel and reports whether this caller won the race.
// Losers are duplicate connections and should hang up. The winner receives a
// channel that is closed if its connection is later released, so it can stop
// holding the connection open.
func (c *channel) provide(conn net.Conn, err error) (release <-chan struct{}, won bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.filled {
		return nil, false
	}
	c.filled = true
	c.conn, c.err = conn, err
	c.release = make(chan struct{})
	close(c.done)
	return c.release, true
}

// reset empties the channel so a later client can fill it, closing the
// abandoned connection and waking whoever was holding it.
func (c *channel) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.filled {
		return
	}
	c.filled = false
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn, c.err = nil, nil
	c.done = make(chan struct{})
	close(c.release)
	c.release = nil
}

func (c *channel) isFilled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.filled
}

// holds reports whether conn is the channel's current connection, which
// distinguishes a live connection from one that has already been superseded.
func (c *channel) holds(conn net.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.filled && c.conn == conn
}

// await blocks until the channel holds a connection. If the channel is reset
// while waiting, it keeps waiting for the next client rather than handing back
// a connection that has already been torn down.
func (c *channel) await(ctx context.Context) (net.Conn, error) {
	for {
		c.mu.Lock()
		done := c.done
		c.mu.Unlock()

		select {
		case <-done:
			c.mu.Lock()
			conn, err, filled := c.conn, c.err, c.filled
			c.mu.Unlock()
			if !filled {
				continue
			}
			return conn, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type Pipeline struct {
	ctx context.Context

	stdioCh  *channel
	stderrCh *channel

	// The stable interconnect, named for direction rather than for either
	// role: in and out are the two halves of the stdio connection, errs is the
	// stderr connection. Sessions come and go against these pipes, so a client
	// that drops and reconnects rejoins the same shell.
	in   *pipe
	out  *pipe
	errs *pipe

	halfOpenTimeout time.Duration

	sessionMu sync.Mutex
	sessionID string
}

func New(ctx context.Context) *Pipeline {
	p := &Pipeline{
		ctx:             ctx,
		stdioCh:         newChannel(),
		stderrCh:        newChannel(),
		in:              newPipe(),
		out:             newPipe(),
		errs:            newPipe(),
		halfOpenTimeout: defaultHalfOpenTimeout,
	}

	go func() {
		<-ctx.Done()
		// Tear the interconnect down so the consumer sees the shutdown rather
		// than blocking on streams that will never move again.
		p.in.close()
		p.out.close()
		p.errs.close()
	}()

	return p
}

// claimSession binds the pipeline to the first session ID it sees and reports
// whether id belongs to that session. It keeps a second client from taking over
// one half of a session already claimed by another.
func (p *Pipeline) claimSession(id string) bool {
	if id == "" {
		return false
	}
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()
	if p.sessionID == "" {
		p.sessionID = id
		p.armHalfOpenTimer(id)
		return true
	}
	return subtle.ConstantTimeCompare([]byte(p.sessionID), []byte(id)) == 1
}

// armHalfOpenTimer releases the session if the client has not connected both of
// its paths before the timeout. Callers must hold sessionMu.
func (p *Pipeline) armHalfOpenTimer(id string) {
	time.AfterFunc(p.halfOpenTimeout, func() {
		p.sessionMu.Lock()
		stale := p.sessionID == id && !(p.stdioCh.isFilled() && p.stderrCh.isFilled())
		if stale {
			p.sessionID = ""
		}
		p.sessionMu.Unlock()
		if !stale {
			return
		}
		p.stdioCh.reset()
		p.stderrCh.reset()
	})
}

// endSession releases the session that conn belonged to, freeing the pipeline
// for the next client. It is a no-op if conn has already been superseded, so a
// pump that notices a stale connection late cannot evict a newer client.
func (p *Pipeline) endSession(conn net.Conn) {
	p.sessionMu.Lock()
	current := p.stdioCh.holds(conn) || p.stderrCh.holds(conn)
	if current {
		p.sessionID = ""
	}
	p.sessionMu.Unlock()
	if !current {
		return
	}
	p.stdioCh.reset()
	p.stderrCh.reset()
}

// WithListener serves the server side of the session, accepting one WebSocket
// client on each of the two session paths.
// The two roles pump stderr in opposite directions: the server produces it and
// the client consumes it. Everything else is symmetric, since the stdio
// connection is full-duplex.
func (p *Pipeline) WithListener(l net.Listener) *Pipeline {
	go p.serve(l)
	go p.pumpIn(p.stdioCh, p.in.w, false)
	go p.pumpOut(p.out.r, p.stdioCh)
	go p.pumpOut(p.errs.r, p.stderrCh)
	return p
}

func (p *Pipeline) serve(l net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/"+pathStdio, p.accept(p.stdioCh))
	mux.HandleFunc("/"+pathStderr, p.accept(p.stderrCh))

	srv := &http.Server{Handler: mux}
	go func() {
		<-p.ctx.Done()
		srv.Close()
	}()
	// The tunnel listener yields plain HTTP; TLS is terminated at the edge.
	srv.Serve(l)
}

func (p *Pipeline) accept(ch *channel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.claimSession(r.URL.Query().Get(paramSession)) {
			http.Error(w, "session already active", http.StatusForbidden)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(p.ctx, conn, websocket.MessageBinary)
		release, won := ch.provide(nc, nil)
		if !won {
			conn.Close(websocket.StatusPolicyViolation, "session already active")
			return
		}
		// Hold the handler open so the conn survives for the session.
		select {
		case <-p.ctx.Done():
		case <-release:
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}
}

// WithURL serves the client side, dialing both session paths on the tunnel's
// public URL.
func (p *Pipeline) WithURL(u *url.URL) *Pipeline {
	id, err := newSessionID()
	if err != nil {
		p.stdioCh.provide(nil, err)
		p.stderrCh.provide(nil, err)
		return p
	}
	go p.dial(u, pathStdio, p.stdioCh, id)
	go p.dial(u, pathStderr, p.stderrCh, id)
	go p.pumpIn(p.stdioCh, p.in.w, true)
	go p.pumpOut(p.out.r, p.stdioCh)
	go p.pumpIn(p.stderrCh, p.errs.w, true)
	return p
}

func (p *Pipeline) dial(u *url.URL, path string, ch *channel, session string) {
	target := u.JoinPath(path)
	switch target.Scheme {
	case "https", "wss":
		target.Scheme = "wss"
	default:
		target.Scheme = "ws"
	}
	q := target.Query()
	q.Set(paramSession, session)
	target.RawQuery = q.Encode()

	conn, _, err := websocket.Dial(p.ctx, target.String(), nil)
	if err != nil {
		ch.provide(nil, err)
		return
	}
	release, won := ch.provide(websocket.NetConn(p.ctx, conn, websocket.MessageBinary), nil)
	if !won {
		conn.Close(websocket.StatusPolicyViolation, "session already active")
		return
	}
	select {
	case <-p.ctx.Done():
	case <-release:
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// pumpIn feeds whatever arrives on a session connection into a stable pipe.
//
// The two roles want opposite things when a connection drops. The host must not
// close its pipe — its shell would see EOF and exit, losing the session that the
// interconnect exists to preserve — so it waits for the next client. The client
// does close, so its terminal exits when the host is gone instead of waiting for
// a reconnect it has no way to make.
func (p *Pipeline) pumpIn(ch *channel, w *io.PipeWriter, endOnDisconnect bool) {
	for {
		conn, err := ch.await(p.ctx)
		if err != nil {
			if endOnDisconnect {
				w.CloseWithError(err)
			}
			return
		}
		io.Copy(w, conn)
		p.endSession(conn)
		if endOnDisconnect {
			w.Close()
			return
		}
	}
}

// pumpOut drains one of the consumer's output pipes to whichever client is
// currently attached. Bytes that cannot be delivered are held and handed to the
// next client, so output written across a reconnect is not lost.
func (p *Pipeline) pumpOut(r io.Reader, ch *channel) {
	buf := make([]byte, 32*1024)
	var pending []byte

	for {
		conn, err := ch.await(p.ctx)
		if err != nil {
			return
		}
		for {
			if pending == nil {
				n, rerr := r.Read(buf)
				if n > 0 {
					pending = buf[:n]
				}
				if rerr != nil {
					if pending != nil {
						conn.Write(pending)
					}
					return
				}
			}
			if _, werr := conn.Write(pending); werr != nil {
				p.endSession(conn)
				break
			}
			pending = nil
		}
	}
}

// Stdio returns the host's end of the interconnect. It does not block: the
// streams exist before any client connects, and reads simply wait for one.
func (p *Pipeline) Stdio() *Stdio {
	return &Stdio{
		Stdin:  p.in.r,
		Stdout: p.out.w,
		Stderr: p.errs.w,
	}
}

// Terminal returns the client's end of the interconnect.
func (p *Pipeline) Terminal() *Terminal {
	return &Terminal{
		Stdin:  p.out.w,
		Stdout: p.in.r,
		Stderr: p.errs.r,
	}
}

func (p *Pipeline) RunnerOption() interp.RunnerOption {
	s := p.Stdio()
	return interp.StdIO(s.Stdin, s.Stdout, s.Stderr)
}
