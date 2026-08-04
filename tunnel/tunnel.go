package tunnel

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"

	"github.com/cnuss/libtunnel"

	"github.com/scaffoldly/tush/debug"
)

// Anyone holding a tush URL can reach the shell behind it, so the URL should
// not be guessable. This asks the provider to mint an opaque hostname rather
// than the usual memorable adjective-animal pair.
const opaqueHeader, opaqueValue = "X-Opaque", "true"

// edge is where the connector dials to reach the tunnel edge, instead of
// discovering it and landing on port 7844.
//
// 7844 is blocked on a great many networks — hotel and guest wifi, corporate
// egress filters, CI runners — and the whole point of tush is publishing a
// shell from wherever you happen to be. This is a relay on 443, which goes
// through anything that lets HTTPS out.
//
// It costs nothing in confidentiality: the connector's TLS to the edge is
// end-to-end against a fixed server name, so a relay that does not terminate
// TLS cannot read what passes through it, and neither the tunnel nor its
// credentials change.
const edge = "relay.tunnel.pizza:443"

type Tunnel struct {
	tun libtunnel.TunnelV1
}

func New(ctx context.Context, provider string) *Tunnel {
	tun := libtunnel.New(
		libtunnel.Cloudflare().
			WithProvider(provider).
			WithHeader(opaqueHeader, opaqueValue).
			WithEdge(edge)).
		WithContext(ctx)

	// Publishing a tunnel is the one part of a session that depends on somebody
	// else's network, and when it does not happen tush has nothing of its own
	// to say — the library is the only thing that knows how far it got.
	if debug.Logging() {
		tun = tun.WithLogger(logger())
	}

	return &Tunnel{tun: tun}
}

// envLevel names how much the tunnel library should say. It is the library's
// own variable, honoured here so that turning tush's logging on does not also
// mean choosing its verbosity.
const envLevel = "LIBTUNNEL_LOG"

// logger sends the library's account of itself to stderr.
//
// Info is enough to place a failure — it names the hostname it was given and
// says when it is connected and waiting on DNS, which is where a tunnel that
// never arrives usually stops. Debug adds every nameserver probe, which is what
// distinguishes a slow answer from a network that is refusing to give one, and
// is far too much to have on by default.
func logger() *slog.Logger {
	level := slog.LevelInfo
	if name := os.Getenv(envLevel); name != "" {
		if err := level.UnmarshalText([]byte(name)); err != nil {
			level = slog.LevelInfo
		}
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func (t *Tunnel) Listener() net.Listener {
	return t.tun.Listener()
}

// URL is the public address of the tunnel, once it is reachable.
//
// It waits for readiness rather than asking for the address straight away, and
// checks what comes back. A tunnel that ends without ever becoming reachable
// has no address, and libtunnel's URL returns a pointer — so this was written
// for a while as URL().String(), which is a segmentation fault the first time a
// tunnel fails to come up. Selecting on Done alongside readiness is the shape
// the library's own example shows, and turns that into something a user can
// read.
func (t *Tunnel) URL() (string, error) {
	select {
	case <-t.tun.TunnelReady():
	case <-t.tun.Done():
		return "", t.failure()
	}

	u := t.tun.URL()
	if u == nil {
		return "", t.failure()
	}
	return u.String(), nil
}

// failure explains why the tunnel ended. It reports something even when the
// library has nothing to add, since an empty error reads as success.
func (t *Tunnel) failure() error {
	if err := t.tun.Err(); err != nil {
		return err
	}
	return errors.New("the tunnel closed before it was reachable")
}
