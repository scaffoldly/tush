package tunnel

import (
	"context"
	"net"

	"github.com/cnuss/libtunnel"
)

// Anyone holding a tush URL can reach the shell behind it, so the URL should
// not be guessable. This asks the provider to mint an opaque hostname rather
// than the usual memorable adjective-animal pair.
const opaqueHeader, opaqueValue = "X-Opaque", "true"

type Tunnel struct {
	tun libtunnel.TunnelV1
}

func New(ctx context.Context, provider string) *Tunnel {
	tun := libtunnel.New(
		libtunnel.Cloudflare().
			WithProvider(provider).
			WithHeader(opaqueHeader, opaqueValue)).
		WithContext(ctx)
	return &Tunnel{tun: tun}
}

func (t *Tunnel) Listener() net.Listener {
	return t.tun.Listener()
}

func (t *Tunnel) URL() string {
	return t.tun.URL().String()
}
