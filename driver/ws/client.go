package ws

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

// Dial opens a socket and returns the driver for it. If the options carry
// handshake metadata it is sent as the first frame, before Dial returns, so
// the first call on the connection is already credentialed.
//
// The returned driver owns the connection: Close it, or let the engine do so
// through interchange.Closer. It is also an interchange.Watcher, so an
// engine.Client over it fails its pending calls the moment the socket dies.
func Dial(ctx context.Context, url string, opts ...Option) (*Driver, error) {
	cfg := newConfig(opts)
	sock, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:   cfg.httpClient,
		HTTPHeader:   cfg.dialHeader,
		Subprotocols: cfg.subprotos,
	})
	if err != nil {
		return nil, fmt.Errorf("ws: dial %s: %w", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	c := newConn(sock, cfg, nil)
	if len(cfg.handshake) > 0 {
		c.mergeMetadata(cfg.handshake)
		if err := c.sendHandshake(ctx, cfg.handshake); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("ws: handshake: %w", err)
		}
	}
	// Nothing can arrive before the caller has made a call, and a call needs
	// the driver this returns, so starting the loop here races with nothing.
	go c.run()
	return c.drv, nil
}
