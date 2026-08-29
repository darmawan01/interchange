// Package nats is the NATS transport driver. It is six methods over
// github.com/nats-io/nats.go: subject naming, header translation, and the
// broker's own reply path. Correlation, deadlines, chunking and replay
// suppression are the engine's, not this file's.
//
// Two tiers ship here, per the durability decision in docs/08: core NATS is
// the default and is what request/reply should use, and NewJetStream gives
// the same driver a durable spine for traffic that has to survive a restart.
// The tiers differ only in the Capabilities they declare -- which is the
// claim "all variation comes from Caps()" being tested rather than asserted.
package nats

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/nats-io/nats.go"
)

// DefaultPrefix is the first token of every subject this driver names.
const DefaultPrefix = "rpc"

// headroom is taken off the server's negotiated max_payload. A NATS server
// counts header bytes against that limit and the engine repeats the request's
// headers on every chunk, so the ceiling handed to the engine has to leave
// room for metadata it knows nothing about.
const headroom = 1024

// Driver is one NATS connection.
type Driver struct {
	conn   *nats.Conn
	prefix string
	inbox  string

	// stream is nil for core NATS -- the one flag that says which tier this
	// driver is. Set by NewJetStream; see jetstream.go.
	stream *stream

	// streamName is the JetStream stream WithStream asked for. It is inert
	// until NewJetStream reads it, so naming a stream on a core driver is a
	// no-op rather than a half-built durable tier.
	streamName string

	// ownConn is true only when this package dialled the connection, which
	// happens exactly in the registry factory. An injected connection belongs
	// to the caller and Close must not take it down.
	ownConn bool
}

var (
	_ interchange.Driver = (*Driver)(nil)
	_ interchange.Closer = (*Driver)(nil)
)

// Option configures a Driver.
type Option func(*Driver)

// WithPrefix replaces the "rpc" root of every subject. Two deployments
// sharing one NATS cluster use it to stay out of each other's subject space.
func WithPrefix(p string) Option {
	return func(d *Driver) { d.prefix = strings.Trim(p, ".") }
}

// New builds a driver over an existing connection. The connection stays the
// caller's: Close leaves it open.
func New(conn *nats.Conn, opts ...Option) (*Driver, error) {
	if conn == nil {
		return nil, errors.New("nats: nil connection")
	}
	d := &Driver{conn: conn, prefix: DefaultPrefix, inbox: nats.NewInbox()}
	for _, o := range opts {
		o(d)
	}
	if d.prefix == "" {
		return nil, errors.New("nats: empty subject prefix")
	}
	return d, nil
}

// Publish sends one frame. On core NATS the reply subject is this driver's
// inbox, so a responder answers through the broker's own reply path and the
// engine's correlation needs nothing in the envelope.
func (d *Driver) Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := &nats.Msg{Subject: addr, Data: body, Header: toHeader(hdr)}
	if d.stream != nil && d.owns(addr) {
		return d.stream.publish(ctx, msg)
	}
	msg.Reply = d.inbox
	return d.conn.PublishMsg(msg)
}

// Subscribe receives frames. A non-empty group is a NATS queue group, which
// is competing-consumer delivery for free.
func (d *Driver) Subscribe(ctx context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	if pattern == "" {
		return nil, errors.New("nats: empty subscription pattern")
	}
	if d.stream != nil && d.owns(pattern) {
		return d.stream.subscribe(ctx, pattern, group, fn)
	}
	handler := func(m *nats.Msg) { fn(d.inbound(m)) }
	var (
		sub *nats.Subscription
		err error
	)
	if group != "" {
		sub, err = d.conn.QueueSubscribe(pattern, group, handler)
	} else {
		sub, err = d.conn.Subscribe(pattern, handler)
	}
	if err != nil {
		return nil, fmt.Errorf("nats: subscribe %s: %w", pattern, err)
	}
	// Subscribe is asynchronous on the wire. Callers publish the instant it
	// returns, so the interest has to be at the server before we say yes.
	if err := d.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("nats: flush subscription %s: %w", pattern, err)
	}
	return sub.Unsubscribe, nil
}

func (d *Driver) inbound(m *nats.Msg) interchange.Inbound {
	in := interchange.Inbound{Address: m.Subject, Header: fromHeader(m.Header), Body: m.Data}
	if m.Reply != "" && d.stream == nil {
		in.Reply = func(body []byte, hdr map[string]string) error {
			return m.RespondMsg(&nats.Msg{Data: body, Header: toHeader(hdr)})
		}
	}
	return in
}

// ReplyAddress is this driver's inbox, created once so every reply -- native
// or folded into the envelope -- lands on one subscription.
func (d *Driver) ReplyAddress() string { return d.inbox }

// Address maps "/pkg.Svc/Method" to "rpc.pkg.Svc.Method".
func (d *Driver) Address(procedure string) string {
	return d.prefix + "." + interchange.ServiceOf(procedure) + "." + interchange.MethodOf(procedure)
}

// ServiceWildcard subscribes to every method of a service.
func (d *Driver) ServiceWildcard(service string) string {
	return d.prefix + "." + service + ".*"
}

// Caps reports what this connection can do. MaxPayload is the server's
// negotiated limit, not a constant: a cluster configured down to 64 KiB has
// to be chunked for, and only the connection knows.
func (d *Driver) Caps() interchange.Capabilities {
	maxPayload := int(d.conn.MaxPayload()) - headroom
	if maxPayload < 1 {
		maxPayload = 1
	}
	return interchange.Capabilities{
		Name:           "nats",
		Transport:      transportv1.Transport_TRANSPORT_BUS,
		NativeHeaders:  true,
		NativeReply:    d.stream == nil,
		CompetingGroup: true,
		OrderedPerKey:  true,
		MaxPayload:     maxPayload,
		AtLeastOnce:    d.stream != nil,
	}
}

// Close drops the connection when this package dialled it.
func (d *Driver) Close() error {
	if d.ownConn {
		d.conn.Close()
	}
	return nil
}

// owns reports whether a subject is in this driver's RPC subject space. In
// JetStream mode everything else -- inboxes, above all -- stays on core NATS,
// because a reply is not something you want persisted.
func (d *Driver) owns(subject string) bool {
	return strings.HasPrefix(subject, d.prefix+".")
}

// Header maps are converted by iteration rather than through Header.Set:
// textproto canonicalises keys, and metadata keys are the engine's to
// normalise, not the transport's.
func toHeader(hdr map[string]string) nats.Header {
	if len(hdr) == 0 {
		return nil
	}
	h := make(nats.Header, len(hdr))
	for k, v := range hdr {
		h[k] = []string{v}
	}
	return h
}

func fromHeader(h nats.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func init() { Register() }

// Register makes the driver available to interchange.NewDriver, and through
// it to `ix` and to a server wiring drivers by name from interchange.yaml.
// Config keys: url, prefix, jetstream ("true"), stream.
func Register() {
	interchange.RegisterDriver("nats", func(cfg map[string]string) (interchange.Driver, error) {
		url := cfg["url"]
		if url == "" {
			url = nats.DefaultURL
		}
		conn, err := nats.Connect(url, nats.Name("interchange"))
		if err != nil {
			return nil, fmt.Errorf("nats: connect %s: %w", url, err)
		}
		opts := []Option{func(d *Driver) { d.ownConn = true }}
		if p := cfg["prefix"]; p != "" {
			opts = append(opts, WithPrefix(p))
		}
		if s := cfg["stream"]; s != "" {
			opts = append(opts, WithStream(s))
		}
		var d *Driver
		if cfg["jetstream"] == "true" {
			d, err = NewJetStream(context.Background(), conn, opts...)
		} else {
			d, err = New(conn, opts...)
		}
		if err != nil {
			conn.Close()
			return nil, err
		}
		return d, nil
	})
}
