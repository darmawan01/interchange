// Package ws is the WebSocket transport driver and the connection-lifecycle
// shim around it.
//
// WebSocket is the degenerate case in addressing and the interesting one in
// lifecycle. There is exactly one channel -- the socket -- so Address()
// ignores the procedure and the procedure lives entirely in the envelope.
// What a socket adds is everything a broker did for you: accepting the
// upgrade, the handshake frame that carries credentials (a browser cannot set
// an Authorization header on an upgrade), demultiplexing concurrent calls
// over one pipe, and noticing that the pipe is gone. That is the shim in
// conn.go, server.go and client.go; this file is the six methods.
//
// Correlation, deadlines, chunking and metadata fallback are the engine's.
// The driver neither serialises nor reorders: frames go out in the order the
// engine hands them over and come back to whichever pending call the
// correlation map says they belong to.
package ws

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
)

// The address grammar, such as it is. A socket has one channel, so these are
// constants rather than functions of the procedure -- but they are three
// distinct constants, because the engine subscribes a wildcard and publishes
// requests and replies on one connection, and those three roles still have to
// be told apart.
//
// They are exported because a non-Go client (a browser) writes them into its
// frames verbatim.
const (
	// AddressCall is where a request goes. Constant: the procedure is in the
	// envelope, not in the address.
	AddressCall = "ws.rpc"

	// AddressReply is the socket itself. It is a package constant and not a
	// per-connection value on purpose: with NativeReply the responder is
	// never told where to answer, and on a socket there is exactly one place
	// an answer can go, so both ends have to name it the same thing.
	AddressReply = "ws"

	// AddressWildcard is what a server subscribes. It matches AddressCall and
	// not AddressReply, so a connection may carry a server and a client at
	// once without either seeing the other's traffic.
	AddressWildcard = "ws.*"
)

// DefaultMaxPayload is the frame ceiling handed to the engine. A socket has no
// protocol limit, but an unbounded one means a single call can pin an
// arbitrary buffer at both ends; 512 KiB keeps a frame comfortably inside
// every intermediary's default and makes the engine chunk anything larger.
const DefaultMaxPayload = 512 << 10

// Driver is one connection. Server-side it is handed to the caller by the
// upgrade handler; client-side it comes back from Dial.
type Driver struct{ c *Conn }

var (
	_ interchange.Driver  = (*Driver)(nil)
	_ interchange.Closer  = (*Driver)(nil)
	_ interchange.Watcher = (*Driver)(nil)
)

// Publish writes one frame. hdr is dropped: a WebSocket frame has no headers,
// which is why Caps().NativeHeaders is false and the engine has already
// folded metadata into the envelope by the time it gets here.
func (d *Driver) Publish(ctx context.Context, addr string, body []byte, _ map[string]string) error {
	if addr == "" {
		return errors.New("ws: empty address")
	}
	return d.c.send(ctx, addr, body)
}

// Subscribe registers interest in an address pattern on this connection. The
// grammar is the same subject match the other drivers use, so the engine's
// wildcard plan needs no special case here.
func (d *Driver) Subscribe(_ context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	if pattern == "" {
		return nil, errors.New("ws: empty subscription pattern")
	}
	return d.c.subscribe(pattern, group, fn)
}

// ReplyAddress is the socket.
func (d *Driver) ReplyAddress() string { return AddressReply }

// Address maps every procedure to the one channel there is.
func (d *Driver) Address(string) string { return AddressCall }

// ServiceWildcard is the same one channel, named as a pattern.
func (d *Driver) ServiceWildcard(string) string { return AddressWildcard }

// Caps reports what a socket can do: replies come back on it for free, it has
// no headers and no competing consumers, and it delivers each frame once.
func (d *Driver) Caps() interchange.Capabilities {
	return interchange.Capabilities{
		Name:           "ws",
		Transport:      transportv1.Transport_TRANSPORT_WS,
		NativeHeaders:  false,
		NativeReply:    true,
		CompetingGroup: false,
		OrderedPerKey:  true,
		MaxPayload:     d.c.cfg.maxPayload,
		AtLeastOnce:    false,
	}
}

// Close shuts the connection down. The engine calls it on shutdown via
// interchange.Closer.
func (d *Driver) Close() error { return d.c.Close() }

// Conn returns the connection this driver rides, for its metadata and its
// lifetime.
func (d *Driver) Conn() *Conn { return d.c }

// Done is closed when the socket is gone. It is interchange.Watcher: the
// engine watches it and fails every pending call, so an in-flight request on
// a dead socket ends at once instead of waiting out its deadline.
func (d *Driver) Done() <-chan struct{} { return d.c.Done() }

// wire framing: uvarint address length, address, body. Small, self-delimiting
// and trivial to write from JavaScript, which is the client that matters.
func encode(addr string, body []byte) []byte {
	out := make([]byte, 0, binary.MaxVarintLen64+len(addr)+len(body))
	out = binary.AppendUvarint(out, uint64(len(addr)))
	out = append(out, addr...)
	return append(out, body...)
}

func decode(b []byte) (string, []byte, error) {
	n, read := binary.Uvarint(b)
	if read <= 0 || uint64(len(b)-read) < n {
		return "", nil, fmt.Errorf("ws: malformed frame: %d bytes", len(b))
	}
	return string(b[read : read+int(n)]), b[read+int(n):], nil
}

// match is the subject grammar shared by every driver in the box: "*" is one
// token, ">" is the rest.
func match(pattern, subject string) bool {
	p := strings.Split(pattern, ".")
	s := strings.Split(subject, ".")
	for i, tok := range p {
		if tok == ">" {
			return i <= len(s)
		}
		if i >= len(s) {
			return false
		}
		if tok == "*" {
			continue
		}
		if tok != s[i] {
			return false
		}
	}
	return len(p) == len(s)
}
