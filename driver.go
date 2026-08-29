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

	// Header is empty when Caps().NativeHeaders is false -- the engine has
	// folded metadata into the envelope in that case.
	Header map[string]string

	Body []byte

	// Reply is nil when Caps().NativeReply is false. The engine then falls
	// back to publishing to the address in the envelope's metadata.
	Reply func(body []byte, hdr map[string]string) error
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
