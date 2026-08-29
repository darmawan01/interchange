package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/darmawan01/interchange"
)

// Conn is one WebSocket connection and everything the brokers do for the
// other drivers: the read loop, the connection-scoped metadata a handshake
// frame establishes, and a Done channel that says the pipe is gone.
type Conn struct {
	ws  *websocket.Conn
	cfg config
	req *http.Request

	// writeMu makes one Publish one WebSocket message. Frames must not
	// interleave on the wire; calls still overtake each other freely, which
	// is what demultiplexing over one pipe means.
	writeMu sync.Mutex

	mu     sync.RWMutex
	meta   interchange.Metadata
	subs   []*subscription
	nextID int64

	drv *Driver

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once

	errMu sync.Mutex
	err   error
}

type subscription struct {
	id      int64
	pattern string
	group   string
	fn      func(interchange.Inbound)
}

func newConn(sock *websocket.Conn, cfg config, req *http.Request) *Conn {
	sock.SetReadLimit(cfg.readLimit)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		ws:     sock,
		cfg:    cfg,
		req:    req,
		meta:   interchange.Metadata{},
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	c.drv = &Driver{c: c}
	if req != nil && cfg.fromRequest != nil {
		// The other half of the browser problem: a credential that rode the
		// upgrade URL or the subprotocol list rather than a frame. Same
		// destination, same merge.
		c.mergeMetadata(cfg.fromRequest(req))
	}
	return c
}

// Driver is the interchange.Driver for this connection.
func (c *Conn) Driver() *Driver { return c.drv }

// Request is the upgrade request, or nil on a dialled connection.
func (c *Conn) Request() *http.Request { return c.req }

// Context is cancelled when the connection ends.
func (c *Conn) Context() context.Context { return c.ctx }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err reports why the connection ended, or nil while it is live or after a
// clean close.
func (c *Conn) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Metadata is the connection-scoped metadata established by the handshake
// frame and by the upgrade request. It is a copy.
func (c *Conn) Metadata() interchange.Metadata {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.meta.Clone()
}

func (c *Conn) mergeMetadata(md interchange.Metadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range md {
		// Set lowercases: a browser sending "Authorization" and a call
		// sending "authorization" must be one key, or the engine's own
		// Set() collapses them in map order and the winner is a coin toss.
		c.meta.Set(k, v)
	}
}

// Close ends the connection. It is idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
		close(c.done)
	})
	return nil
}

func (c *Conn) fail(err error, code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		c.cancel()
		_ = c.ws.Close(code, reason)
		close(c.done)
	})
}

func (c *Conn) send(ctx context.Context, addr string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return errors.New("ws: connection is closed")
	default:
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, encode(addr, body))
}

// sendHandshake writes the connection-scoped metadata as the first frame. A
// dialled client uses it; a browser sends the same JSON as its first text
// frame.
func (c *Conn) sendHandshake(ctx context.Context, md interchange.Metadata) error {
	b, err := json.Marshal(md.AsMap())
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

func (c *Conn) subscribe(pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	select {
	case <-c.done:
		return nil, errors.New("ws: connection is closed")
	default:
	}
	c.mu.Lock()
	c.nextID++
	s := &subscription{id: c.nextID, pattern: pattern, group: group, fn: fn}
	c.subs = append(c.subs, s)
	c.mu.Unlock()
	return func() error {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, existing := range c.subs {
			if existing.id == s.id {
				c.subs = append(c.subs[:i], c.subs[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// run is the read loop. It delivers on its own goroutine and delivers in
// arrival order: the engine hands every unit of real work to a goroutine of
// its own, so serialising here costs nothing and reordering here would break
// chunk reassembly for no gain.
func (c *Conn) run() {
	handshakeOpen := true
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || errors.Is(err, context.Canceled) {
				_ = c.Close()
				return
			}
			c.fail(err, websocket.StatusInternalError, "read failed")
			return
		}
		if typ == websocket.MessageText {
			if !handshakeOpen {
				// Credentials that arrive mid-stream would apply to calls
				// already dispatched under the old ones. Refuse rather than
				// invent a race.
				c.fail(errors.New("ws: handshake frame after the first message"),
					websocket.StatusPolicyViolation, "late handshake")
				return
			}
			var md map[string]string
			if err := json.Unmarshal(data, &md); err != nil {
				c.fail(err, websocket.StatusPolicyViolation, "malformed handshake")
				return
			}
			c.mergeMetadata(interchange.NewMetadata(md))
			handshakeOpen = false
			continue
		}
		handshakeOpen = false

		addr, body, err := decode(data)
		if err != nil {
			c.fail(err, websocket.StatusPolicyViolation, "malformed frame")
			return
		}
		c.dispatch(addr, body)
	}
}

func (c *Conn) dispatch(addr string, body []byte) {
	in := interchange.Inbound{
		Address: addr,
		// The handshake merge, in full: the connection's metadata is what
		// this socket knows and the envelope does not. The engine merges it
		// under the envelope's own metadata, so a per-call value still wins.
		Header: c.Metadata().AsMap(),
		Body:   body,
		Reply: func(rbody []byte, _ map[string]string) error {
			return c.send(c.ctx, AddressReply, rbody)
		},
	}

	c.mu.RLock()
	subs := make([]*subscription, len(c.subs))
	copy(subs, c.subs)
	c.mu.RUnlock()

	for _, s := range subs {
		if match(s.pattern, addr) {
			s.fn(in)
		}
	}
}
