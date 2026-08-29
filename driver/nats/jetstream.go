package nats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// maxAge bounds how long an unconsumed request sits in the stream. A request
// nobody answered within it has a caller that gave up long ago; keeping it
// only replays work whose reply address is dead.
const maxAge = 5 * time.Minute

// nakDelay spaces out the redelivery of a message the engine could not
// handle, and maxDeliver stops one it can never handle from being retried
// forever: a malformed frame naks every time, and a poison message that
// redelivers without limit is a busy loop, not durability.
const (
	nakDelay   = time.Second
	maxDeliver = 5
)

type stream struct {
	js   jetstream.JetStream
	name string
}

// WithStream names the JetStream stream backing the RPC subject space.
// Default: the prefix, upper-cased.
func WithStream(name string) Option {
	return func(d *Driver) { d.streamName = name }
}

// NewJetStream builds the durable tier: requests are published into a stream
// and served from durable consumers, so a server that was down when the call
// was made still gets it.
//
// It declares NativeReply false, and that is not an oversight. JetStream does
// not store the publisher's reply subject -- a delivered message carries the
// consumer's ack subject in that field instead -- so the broker cannot route
// the response. The engine's MetaReplyTo fallback carries the return address
// in the envelope, and the reply itself goes back over core NATS: a reply is
// worth nothing after the caller's deadline, so persisting it would be cost
// without a benefit.
func NewJetStream(ctx context.Context, conn *nats.Conn, opts ...Option) (*Driver, error) {
	d, err := New(conn, opts...)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}
	name := d.streamName
	if name == "" {
		name = strings.ToUpper(sanitize(d.prefix))
	}
	d.stream = &stream{js: js, name: name}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        d.stream.name,
		Subjects:    []string{d.prefix + ".>"},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      maxAge,
		Description: "interchange RPC requests",
	}); err != nil {
		return nil, fmt.Errorf("nats: create stream %s: %w", d.stream.name, err)
	}
	return d, nil
}

// publish waits for the PubAck: "at-least-once" is a promise the caller can
// only make once the stream has the message on disk.
func (s *stream) publish(ctx context.Context, msg *nats.Msg) error {
	if _, err := s.js.PublishMsg(ctx, msg); err != nil {
		return fmt.Errorf("nats: jetstream publish %s: %w", msg.Subject, err)
	}
	return nil
}

func (s *stream) subscribe(ctx context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	cfg := jetstream.ConsumerConfig{
		FilterSubject: pattern,
		AckPolicy:     jetstream.AckExplicitPolicy,
		// A fresh consumer starts at now; an existing durable resumes at its
		// ack floor. That pair is exactly the durability being bought here --
		// a restart replays what this group never acked, and nothing older.
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    maxDeliver,
	}
	if group != "" {
		// A queue group is a shared durable consumer: same name, so members
		// compete for the same messages and the group's position outlives any
		// one member.
		cfg.Durable = durableName(pattern, group)
		cfg.Name = cfg.Durable
	}
	cons, err := s.js.CreateOrUpdateConsumer(ctx, s.name, cfg)
	if err != nil {
		return nil, fmt.Errorf("nats: consumer for %s: %w", pattern, err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		fn(interchange.Inbound{
			Address: m.Subject(),
			Header:  fromHeader(m.Headers()),
			Body:    m.Data(),
			// The ack is the engine's to give, not the delivery's: it fires
			// once the call has been handled and its reply sent, so a handler
			// that dies half way through leaves the message for redelivery
			// instead of losing it.
			Done: func(err error) {
				if err != nil {
					_ = m.NakWithDelay(nakDelay)
					return
				}
				_ = m.Ack()
			},
		})
	})
	if err != nil {
		return nil, fmt.Errorf("nats: consume %s: %w", pattern, err)
	}
	return func() error { cc.Stop(); return nil }, nil
}

// durableName is stable across processes and legal as a consumer name, which
// rules out the dots and wildcards a subject pattern is made of.
func durableName(pattern, group string) string {
	sum := sha256.Sum256([]byte(pattern + "\x00" + group))
	return "ix_" + sanitize(group) + "_" + hex.EncodeToString(sum[:4])
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}
