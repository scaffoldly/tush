package tunnel

import (
	"context"
	"net"

	"github.com/cnuss/libtunnel"
)

type Tunnel struct {
	tun libtunnel.TunnelV1
}

func New(ctx context.Context, provider string) *Tunnel {
	tun := libtunnel.New(
		libtunnel.Cloudflare().
			WithProvider(provider)).
		WithContext(ctx)
	return &Tunnel{tun: tun}
}

func (t *Tunnel) Listener() net.Listener {
	return t.tun.Listener()
}

func (t *Tunnel) URL() string {
	return t.tun.URL().String()
}
