// Package mqtt is the MQTT 5 transport driver.
//
// Version 5 only, and that is a decision rather than an omission (docs/08):
// 3.1.1 has no user properties, no response topic and no correlation data, so
// a 3.1.1 driver would have to reinvent metadata and reply routing inside the
// payload -- badly, and differently from every other 3.1.1 client on the
// broker. On v5 those three facilities are native, which is why this driver
// declares NativeHeaders and NativeReply and stays the size of an adapter.
//
// Topic grammar, with the prefix configurable (default "ix"):
//
//	procedure "/pkg.Svc/Method"  ->  ix/rpc/pkg.Svc/Method
//	service   "pkg.Svc"          ->  ix/rpc/pkg.Svc/+
//	reply address                ->  ix/reply/<client-id>
//	competing group "g"          ->  $share/g/<pattern>
package mqtt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
)

// Defaults. The payload ceiling applies only when the broker's CONNACK
// advertises no Maximum Packet Size; 256 KiB is under every hosted broker's
// limit we know of, and anything larger the engine chunks.
const (
	DefaultPrefix     = "ix"
	DefaultQoS        = 1
	defaultMaxPayload = 256 << 10
	packetHeadroom    = 1 << 10 // topic + properties, subtracted from the broker's ceiling
	connectTimeout    = 10 * time.Second
)

// Config is the driver's configuration. The factory registered as "mqtt"
// builds it from the driver block in interchange.yaml; see README.md for the
// key names.
type Config struct {
	URL        string // tcp://host:port
	Prefix     string
	QoS        byte // 1 or 2; 0 would drop chunks of a message with no way to notice
	ClientID   string
	Username   string
	Password   string
	MaxPayload int // overrides what the broker advertises; 0 means negotiate
}

// Driver is one MQTT 5 connection.
type Driver struct {
	cli    *paho.Client
	prefix string
	qos    byte
	inbox  string
	caps   interchange.Capabilities

	mu     sync.RWMutex
	subs   map[int64]*sub
	nextID atomic.Int64
}

type sub struct {
	filter string // the bare pattern; a delivered topic never carries the $share prefix
	fn     func(interchange.Inbound)
}

var _ interchange.Driver = (*Driver)(nil)

func init() {
	interchange.RegisterDriver("mqtt", func(cfg map[string]string) (interchange.Driver, error) {
		c := Config{
			URL:      cfg["url"],
			Prefix:   cfg["prefix"],
			ClientID: cfg["client_id"],
			Username: cfg["username"],
			Password: cfg["password"],
		}
		if raw := cfg["qos"]; raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 2 {
				return nil, fmt.Errorf("mqtt: qos %q: only 1 and 2 are supported", raw)
			}
			c.QoS = byte(n)
		}
		if raw := cfg["max_payload"]; raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("mqtt: max_payload: %w", err)
			}
			c.MaxPayload = n
		}
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		return Connect(ctx, c)
	})
}

// Connect dials the broker and completes the MQTT 5 handshake.
func Connect(ctx context.Context, cfg Config) (*Driver, error) {
	if cfg.Prefix == "" {
		cfg.Prefix = DefaultPrefix
	}
	switch cfg.QoS {
	case 0:
		cfg.QoS = DefaultQoS
	case 1, 2:
	default:
		return nil, fmt.Errorf("mqtt: qos %d: only 1 and 2 are supported", cfg.QoS)
	}
	if cfg.ClientID == "" {
		// A shared client id is a session takeover: the broker evicts the
		// first holder when the second connects, and the reply topic below is
		// only unique because this is.
		var b [8]byte
		_, _ = rand.Read(b[:])
		cfg.ClientID = "ix-" + hex.EncodeToString(b[:])
	}
	addr := cfg.URL
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		addr = u.Host
	}
	if addr == "" {
		addr = "127.0.0.1:1883"
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mqtt: dial %s: %w", addr, err)
	}
	// paho writes a packet with several Write calls and locks the connection
	// only if it is a sync.Locker. The engine publishes concurrently on one
	// client, so an unwrapped conn interleaves two packets' bytes.
	conn = packets.NewThreadSafeConn(conn)

	d := &Driver{
		prefix: strings.Trim(cfg.Prefix, "/"),
		qos:    cfg.QoS,
		subs:   map[int64]*sub{},
	}
	d.inbox = d.prefix + "/reply/" + cfg.ClientID
	d.cli = paho.NewClient(paho.ClientConfig{
		ClientID:          cfg.ClientID,
		Conn:              conn,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){d.route},
		// The PUBACK is Inbound.Done, not delivery: acking on arrival would
		// tell the broker a request was handled before the handler started.
		EnableManualAcknowledgment: true,
	})
	ca, err := d.cli.Connect(ctx, &paho.Connect{
		ClientID:     cfg.ClientID,
		KeepAlive:    30,
		CleanStart:   true,
		Username:     cfg.Username,
		UsernameFlag: cfg.Username != "",
		Password:     []byte(cfg.Password),
		PasswordFlag: cfg.Password != "",
	})
	if err != nil {
		return nil, fmt.Errorf("mqtt: connect %s: %w", addr, err)
	}
	if ca.ReasonCode != 0 {
		return nil, fmt.Errorf("mqtt: broker refused connection: reason %d", ca.ReasonCode)
	}

	ceiling := cfg.MaxPayload
	if ceiling == 0 {
		ceiling = defaultMaxPayload
		if p := ca.Properties; p != nil && p.MaximumPacketSize != nil && *p.MaximumPacketSize > 0 {
			// Headroom for the topic and properties, but never past halving a
			// broker that advertises a very small packet.
			ceiling = max(int(*p.MaximumPacketSize)-packetHeadroom, int(*p.MaximumPacketSize)/2)
		}
	}
	d.caps = interchange.Capabilities{
		Name:           "mqtt",
		Transport:      transportv1.Transport_TRANSPORT_MQTT,
		NativeHeaders:  true, // user properties
		NativeReply:    true, // response topic
		CompetingGroup: true, // $share
		OrderedPerKey:  false,
		MaxPayload:     ceiling,
		AtLeastOnce:    true, // QoS 1 redelivers; the engine suppresses the replay
	}
	return d, nil
}

// Close disconnects. The engine calls it on shutdown via interchange.Closer.
func (d *Driver) Close() error { return d.cli.Disconnect(&paho.Disconnect{ReasonCode: 0}) }

// Publish sends one frame. hdr becomes user properties, the response topic
// points at this client's inbox so the peer's reply is routed by the broker
// rather than by anything in the payload, and the engine's correlation id
// becomes Correlation Data -- the property a plain MQTT 5 client matches a
// response on. It is moved there rather than copied: a peer that speaks MQTT
// reads the native property, and one that speaks interchange reads the id
// inside the envelope, so a third copy in the user properties would only leak
// an ix- key into handler metadata.
func (d *Driver) Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error {
	props := &paho.PublishProperties{ResponseTopic: d.inbox}
	for k, v := range hdr {
		if k == interchange.MetaCorrelationID {
			props.CorrelationData = []byte(v)
			continue
		}
		props.User.Add(k, v)
	}
	_, err := d.cli.Publish(ctx, &paho.Publish{Topic: addr, QoS: d.qos, Payload: body, Properties: props})
	return err
}

// Subscribe receives frames matching pattern. A non-empty group becomes a
// shared subscription, which is MQTT's competing-consumer delivery.
func (d *Driver) Subscribe(ctx context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	if pattern == "" {
		return nil, fmt.Errorf("mqtt: empty subscription pattern")
	}
	topic := pattern
	if group != "" {
		topic = "$share/" + group + "/" + pattern
	}
	// Registered before the SUBACK, because paho delivers on its own
	// goroutine: a message arriving in that window would find no subscription,
	// and this driver acknowledges what it cannot route.
	id := d.nextID.Add(1)
	d.mu.Lock()
	d.subs[id] = &sub{filter: pattern, fn: fn}
	d.mu.Unlock()
	if _, err := d.cli.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: d.qos}},
	}); err != nil {
		d.mu.Lock()
		delete(d.subs, id)
		d.mu.Unlock()
		return nil, fmt.Errorf("mqtt: subscribe %s: %w", topic, err)
	}
	return func() error {
		d.mu.Lock()
		delete(d.subs, id)
		d.mu.Unlock()
		// interchange.Unsubscribe carries no context, and none is invented
		// here: paho bounds every packet wait with its PacketTimeout, so a
		// silent broker fails this rather than hanging shutdown behind it.
		_, err := d.cli.Unsubscribe(context.Background(), &paho.Unsubscribe{Topics: []string{topic}})
		return err
	}, nil
}

// route hands one delivered publish to every matching subscription. Delivery
// is synchronous: the engine already dispatches handlers on its own
// goroutines, and acknowledging in order is cheaper than a goroutine per
// frame.
func (d *Driver) route(p paho.PublishReceived) (bool, error) {
	// One PUBACK per packet even if two subscriptions match the topic and
	// both report an outcome.
	var once sync.Once
	done := func(err error) {
		once.Do(func() {
			// MQTT has no negative acknowledgement -- a PUBACK reason code
			// does not make a broker retry -- so "not handled" is expressed
			// by leaving the packet unacknowledged and letting the broker
			// redeliver it.
			if err == nil {
				_ = d.cli.Ack(p.Packet)
			}
		})
	}

	in := interchange.Inbound{Address: p.Packet.Topic, Body: p.Packet.Payload, Done: done}
	if props := p.Packet.Properties; props != nil {
		in.Header = make(map[string]string, len(props.User))
		for _, u := range props.User {
			in.Header[u.Key] = u.Value
		}
		if props.ResponseTopic != "" {
			reply, corr := props.ResponseTopic, props.CorrelationData
			in.Reply = func(ctx context.Context, body []byte, hdr map[string]string) error {
				// Echoing Correlation Data is what makes the reply matchable
				// by a peer that never saw our envelope.
				rp := &paho.PublishProperties{CorrelationData: corr}
				for k, v := range hdr {
					rp.User.Add(k, v)
				}
				_, err := d.cli.Publish(ctx, &paho.Publish{
					Topic: reply, QoS: d.qos, Payload: body, Properties: rp,
				})
				return err
			}
		}
	}

	d.mu.RLock()
	var targets []*sub
	for _, s := range d.subs {
		if match(s.filter, p.Packet.Topic) {
			targets = append(targets, s)
		}
	}
	d.mu.RUnlock()
	for _, s := range targets {
		s.fn(in)
	}
	if len(targets) == 0 {
		// Nobody will call Done for a packet no subscription matched: there
		// is no handler to finish it, and redelivering it would loop forever.
		done(nil)
	}
	return len(targets) > 0, nil
}

// ReplyAddress is this client's response topic.
func (d *Driver) ReplyAddress() string { return d.inbox }

// Address maps "/pkg.Svc/Method" to "<prefix>/rpc/pkg.Svc/Method".
func (d *Driver) Address(procedure string) string {
	return d.prefix + "/rpc/" + interchange.ServiceOf(procedure) + "/" + interchange.MethodOf(procedure)
}

// ServiceWildcard subscribes to every method of a service.
func (d *Driver) ServiceWildcard(service string) string {
	return d.prefix + "/rpc/" + service + "/+"
}

// Caps reports what this transport can do.
func (d *Driver) Caps() interchange.Capabilities { return d.caps }

// match is MQTT topic filter matching: "+" is one level, "#" is the rest.
func match(filter, topic string) bool {
	f := strings.Split(filter, "/")
	t := strings.Split(topic, "/")
	for i, seg := range f {
		if seg == "#" {
			return i <= len(t)
		}
		if i >= len(t) {
			return false
		}
		if seg != "+" && seg != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}
