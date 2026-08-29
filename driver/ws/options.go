package ws

import (
	"log/slog"
	"net/http"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/engine"
)

type config struct {
	maxPayload  int
	readLimit   int64
	logger      *slog.Logger
	origins     []string
	subprotos   []string
	handshake   interchange.Metadata
	fromRequest func(*http.Request) interchange.Metadata
	serverOpts  []engine.ServerOption
	dialHeader  http.Header
	httpClient  *http.Client
}

// Option configures a connection. The same options apply to both ends; the
// ones that only make sense on one say so.
type Option func(*config)

func newConfig(opts []Option) config {
	cfg := config{
		maxPayload: DefaultMaxPayload,
		logger:     slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.readLimit <= 0 {
		// A chunk is at most MaxPayload; the slack covers the address prefix
		// and the Frame wrapper the engine puts around it.
		cfg.readLimit = int64(cfg.maxPayload) + 64<<10
		if cfg.maxPayload <= 0 {
			cfg.readLimit = 8 << 20
		}
	}
	return cfg
}

// WithMaxPayload sets the frame ceiling reported to the engine, above which
// it chunks. Zero means no ceiling.
func WithMaxPayload(n int) Option {
	return func(c *config) { c.maxPayload = n }
}

// WithReadLimit caps one inbound WebSocket message. The default leaves room
// for one chunk plus framing; lower it on a socket reachable by something you
// do not trust, and pair it with engine.WithMaxMessage, which caps what
// reassembly will accumulate out of many small chunks.
func WithReadLimit(n int64) Option {
	return func(c *config) { c.readLimit = n }
}

// WithLogger sets the logger. Default: slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithOrigins is the browser origin allowlist for the upgrade. Empty means
// same-origin only, which is coder/websocket's default and the right one.
func WithOrigins(patterns ...string) Option {
	return func(c *config) { c.origins = patterns }
}

// WithSubprotocols negotiates WebSocket subprotocols: offered on dial,
// accepted on upgrade.
func WithSubprotocols(names ...string) Option {
	return func(c *config) { c.subprotos = names }
}

// WithHandshakeMetadata is client-side: the metadata sent as the first frame
// on the socket, which the server merges into every call on that connection.
// It is how a browser supplies a credential it cannot put in a header.
func WithHandshakeMetadata(md interchange.Metadata) Option {
	return func(c *config) { c.handshake = md }
}

// WithRequestMetadata is server-side: connection metadata read off the
// upgrade request -- a query parameter, a cookie, a subprotocol entry. It
// merges into the same connection map the handshake frame writes, and the
// frame wins because it arrives later.
func WithRequestMetadata(f func(*http.Request) interchange.Metadata) Option {
	return func(c *config) { c.fromRequest = f }
}

// WithServerOptions passes engine.ServerOptions to the per-connection engine
// server NewServer builds.
func WithServerOptions(opts ...engine.ServerOption) Option {
	return func(c *config) { c.serverOpts = append(c.serverOpts, opts...) }
}

// WithDialHeader sets HTTP headers on the upgrade request. A non-browser
// client can put a credential here instead of in a handshake frame; a browser
// cannot, which is why the frame exists.
func WithDialHeader(h http.Header) Option {
	return func(c *config) { c.dialHeader = h }
}

// WithHTTPClient sets the client used for the upgrade request.
func WithHTTPClient(h *http.Client) Option {
	return func(c *config) { c.httpClient = h }
}
