// Package memory is an in-process transport driver. It is a real driver, not
// a mock: it implements the same six methods, declares Capabilities the same
// way, and imports no concrete message type.
//
// It exists for three reasons. It is how the engine is tested without a
// broker; it is what `ix dev` runs; and it is the fourth driver that
// falsifies the seam -- if the engine ever needs to know which transport it
// is on, this driver is where that shows up first.
package memory

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
)

// Bus is an in-process broker. Drivers attached to the same Bus can reach
// each other; drivers on different buses cannot.
type Bus struct {
	mu     sync.RWMutex
	subs   map[int64]*subscription
	nextID atomic.Int64
	caps   interchange.Capabilities

	// deliverTwice makes every publish arrive twice, which is what an
	// at-least-once transport does on a bad day. It is how replay suppression
	// is tested without waiting for a broker to misbehave.
	deliverTwice bool

	wg sync.WaitGroup
}

type subscription struct {
	id      int64
	pattern string
	group   string
	fn      func(interchange.Inbound)
	ctx     context.Context
}

// Option configures a Bus.
type Option func(*Bus)

// WithCapabilities overrides the declared capabilities. Tests use it to run
// the same suite against a transport with no headers, no native reply, a
// payload ceiling, or at-least-once delivery -- which is the whole point of
// Capabilities being data rather than a type switch.
func WithCapabilities(c interchange.Capabilities) Option {
	return func(b *Bus) {
		c.Transport = transportv1.Transport_TRANSPORT_BUS
		if c.Name == "" {
			c.Name = "memory"
		}
		b.caps = c
	}
}

// WithDuplicateDelivery makes every message arrive twice.
func WithDuplicateDelivery() Option {
	return func(b *Bus) { b.deliverTwice = true }
}

// DefaultCapabilities is what an in-process bus can honestly claim: headers
// and replies are free, competing groups are implemented, nothing is
// redelivered, and there is no payload ceiling.
func DefaultCapabilities() interchange.Capabilities {
	return interchange.Capabilities{
		Name:           "memory",
		Transport:      transportv1.Transport_TRANSPORT_BUS,
		NativeHeaders:  true,
		NativeReply:    true,
		CompetingGroup: true,
		OrderedPerKey:  true,
	}
}

// New returns an empty bus.
func New(opts ...Option) *Bus {
	b := &Bus{subs: map[int64]*subscription{}, caps: DefaultCapabilities()}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Driver attaches a driver to the bus. name becomes its reply address, so two
// drivers on one bus must not share a name.
func (b *Bus) Driver(name string) *Driver {
	return &Driver{bus: b, inbox: "_INBOX." + name}
}

// Wait blocks until every in-flight delivery has been handed to a subscriber.
// Tests use it to avoid sleeping.
func (b *Bus) Wait() { b.wg.Wait() }

func (b *Bus) subscribe(ctx context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	if pattern == "" {
		return nil, fmt.Errorf("memory: empty subscription pattern")
	}
	id := b.nextID.Add(1)
	s := &subscription{id: id, pattern: pattern, group: group, fn: fn, ctx: ctx}
	b.mu.Lock()
	b.subs[id] = s
	b.mu.Unlock()
	return func() error {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		return nil
	}, nil
}

func (b *Bus) publish(ctx context.Context, from *Driver, addr string, body []byte, hdr map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.RLock()
	var direct []*subscription
	groups := map[string][]*subscription{}
	for _, s := range b.subs {
		if !match(s.pattern, addr) {
			continue
		}
		if s.group == "" {
			direct = append(direct, s)
			continue
		}
		groups[s.group] = append(groups[s.group], s)
	}
	b.mu.RUnlock()

	targets := direct
	for _, members := range groups {
		// Competing consumers: exactly one member of each group. Picking the
		// lowest id keeps it deterministic, which matters more in a test
		// driver than fairness does.
		pick := members[0]
		for _, m := range members {
			if m.id < pick.id {
				pick = m
			}
		}
		targets = append(targets, pick)
	}
	if len(targets) == 0 {
		return fmt.Errorf("memory: no subscriber for %q", addr)
	}

	times := 1
	if b.deliverTwice {
		times = 2
	}
	for _, s := range targets {
		for range times {
			b.deliver(s, from, addr, body, hdr)
		}
	}
	return nil
}

func (b *Bus) deliver(s *subscription, from *Driver, addr string, body []byte, hdr map[string]string) {
	in := interchange.Inbound{Address: addr, Body: append([]byte(nil), body...)}
	if b.caps.NativeHeaders {
		in.Header = maps.Clone(hdr)
	}
	if b.caps.NativeReply && from != nil {
		reply := from.inbox
		in.Reply = func(ctx context.Context, rbody []byte, rhdr map[string]string) error {
			return b.publish(ctx, nil, reply, rbody, rhdr)
		}
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if s.ctx != nil && s.ctx.Err() != nil {
			return
		}
		s.fn(in)
	}()
}

// match implements NATS-style subject matching: "*" is one token, ">" is the
// rest. Every driver in the box uses this grammar, which is why one address
// scheme survives three brokers.
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

// Driver is one participant on a Bus.
type Driver struct {
	bus   *Bus
	inbox string
}

var _ interchange.Driver = (*Driver)(nil)

// Publish sends one frame to a named channel.
func (d *Driver) Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error {
	return d.bus.publish(ctx, d, addr, body, hdr)
}

// Subscribe receives frames matching a pattern.
func (d *Driver) Subscribe(ctx context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	return d.bus.subscribe(ctx, pattern, group, fn)
}

// ReplyAddress is this driver's inbox.
func (d *Driver) ReplyAddress() string { return d.inbox }

// Address maps a procedure to a subject: "/pkg.Svc/Method" becomes
// "rpc.pkg.Svc.Method".
func (d *Driver) Address(procedure string) string {
	return "rpc." + interchange.ServiceOf(procedure) + "." + interchange.MethodOf(procedure)
}

// ServiceWildcard subscribes to every method of a service.
func (d *Driver) ServiceWildcard(service string) string { return "rpc." + service + ".*" }

// Caps reports what this transport can do.
func (d *Driver) Caps() interchange.Capabilities { return d.bus.caps }
