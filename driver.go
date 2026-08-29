package interchange

import (
	"context"

	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
)

// Unsubscribe tears down a subscription.
type Unsubscribe func() error

// Inbound is one frame off the wire, as a driver hands it to the engine.
type Inbound struct {
	Address string

	// Header is metadata the transport supplied out of band. On a transport
	// with native headers it is those headers. On one without, it is
	// whatever the driver knows that the envelope does not -- a WebSocket's
	// connection-scoped credential, established by a handshake frame because
	// a browser cannot set an Authorization header on an upgrade.
	//
	// The engine merges it beneath the envelope's own metadata, so a
	// per-call value always wins over a per-connection one.
	Header map[string]string

	Body []byte

	// Reply is nil when Caps().NativeReply is false. The engine then falls
	// back to publishing to the address in the envelope's metadata.
	Reply func(body []byte, hdr map[string]string) error

	// Done, when non-nil, reports the outcome of this message back to the
	// transport, exactly once, after the call has been handled and its reply
	// sent. A driver over a broker with explicit acknowledgement wires it to
	// the ack.
	//
	// It exists because acking on delivery and acking on completion are
	// different guarantees, and only the second one is worth calling
	// at-least-once: a handler that crashes half way through a message the
	// broker already considers delivered is work that silently vanishes.
	// Replay suppression dedupes a redelivery; it cannot conjure one.
	//
	// Done(nil) means the message was handled. Done(err) means it was not,
	// and the driver may redeliver it if the transport can. A driver whose
	// transport has no acknowledgement leaves this nil.
	Done func(err error)
}

// Capabilities is what a driver declares about its transport. The engine
// reads it and degrades gracefully; it is the ONLY place per-transport
// behaviour is allowed to differ, which is what lets the engine be written
// without a switch on transport type.
type Capabilities struct {
	// Name identifies the driver in diagnostics and in `ix describe`.
	Name string

	// Transport is which road this driver is. It is routing metadata -- the
	// engine uses it to decide which procedures to subscribe, never to
	// choose behaviour.
	Transport transportv1.Transport

	NativeHeaders  bool // NATS yes · MQTT 5 yes · WebSocket no
	NativeReply    bool // NATS inbox · MQTT 5 response topic · WS same socket
	CompetingGroup bool // NATS queue group · MQTT $share · WS no
	OrderedPerKey  bool
	MaxPayload     int  // engine chunks into Frames above this
	AtLeastOnce    bool // engine enables replay suppression via sequence
}

// Driver is everything a broker-specific adapter has to supply. If a driver
// is much bigger than this, engine responsibilities have leaked into it.
//
// A driver sees procedure strings, bytes and metadata. It may not import a
// single concrete message type: the moment it does, it has stopped being an
// adapter and become a second implementation of the API.
type Driver interface {
	// Publish sends one frame to a named channel. hdr is dropped by drivers
	// whose transport has no native metadata -- the engine has already folded
	// it into the envelope in that case (see Caps).
	Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error

	// Subscribe receives frames matching a pattern. group requests
	// competing-consumer delivery where the transport supports it.
	Subscribe(ctx context.Context, pattern, group string, fn func(Inbound)) (Unsubscribe, error)

	// ReplyAddress is where replies to this client come back: a NATS inbox,
	// an MQTT response topic, or -- for WebSocket -- the socket itself.
	ReplyAddress() string

	// Address maps a procedure to a channel name.
	Address(procedure string) string

	// ServiceWildcard is the pattern that subscribes to a whole service.
	ServiceWildcard(service string) string

	Caps() Capabilities
}

// Closer is an optional driver capability: a driver holding a connection
// implements it and the engine closes it on shutdown.
type Closer interface {
	Close() error
}

// Watcher is an optional driver capability for a driver that holds a
// connection rather than talking to a broker that outlives it. The engine
// watches Done and fails every pending call when the transport is gone.
//
// Without it a client on a dead socket waits out its deadline for a reply
// that provably will not arrive -- correct, but a needlessly slow way to
// learn something the driver already knew.
type Watcher interface {
	Done() <-chan struct{}
}
