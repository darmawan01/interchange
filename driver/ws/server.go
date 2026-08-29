package ws

import (
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/engine"
)

// NewServer returns an http.Handler that serves reg over every socket it
// accepts: one driver and one engine server per connection, started before
// the first frame is read and stopped when the socket goes away.
//
//	mux.Handle("/ws", ws.NewServer(reg))
//
// This is the shape to reach for. Handler is the same thing with the engine
// server left to the caller, for a connection that needs to do something else
// as well.
func NewServer(reg *interchange.Registry, opts ...Option) http.Handler {
	cfg := newConfig(opts)
	return handler(cfg, func(c *Conn) error {
		srv := engine.NewServer(c.Driver(), reg, cfg.serverOpts...)
		if err := srv.Start(c.Context()); err != nil {
			return err
		}
		go func() {
			<-c.Done()
			_ = srv.Stop()
		}()
		return nil
	})
}

// Handler upgrades and hands each accepted socket to setup, which owns the
// connection:
//
//	h := ws.Handler(func(c *ws.Conn) error {
//		srv := engine.NewServer(c.Driver(), reg)
//		return srv.Start(c.Context())
//	})
//
// setup runs before the read loop starts, so a subscription it makes cannot
// miss a frame, and it must not block: the connection is served on the
// calling goroutine once it returns. An error from setup closes the socket.
func Handler(setup func(*Conn) error, opts ...Option) http.Handler {
	return handler(newConfig(opts), setup)
}

func handler(cfg config, setup func(*Conn) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sock, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: cfg.origins,
			Subprotocols:   cfg.subprotos,
		})
		if err != nil {
			// Accept has already written the failure to w.
			cfg.logger.Warn("ws: upgrade failed", slog.String("err", err.Error()))
			return
		}
		c := newConn(sock, cfg, r)
		if setup != nil {
			if err := setup(c); err != nil {
				cfg.logger.Warn("ws: connection setup failed", slog.String("err", err.Error()))
				c.fail(err, websocket.StatusInternalError, "setup failed")
				return
			}
		}
		c.run()
		_ = c.Close()
	})
}
