package mqtt

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/drivertest"
	"github.com/darmawan01/interchange/engine"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	"github.com/darmawan01/interchange/internal/testsvc"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// broker starts an MQTT 5 broker in this process on a free localhost port.
// The conformance suite needs a real broker, not a fake: $share, response
// topics and QoS 1 redelivery are the things being tested.
func broker(t *testing.T, hooks ...mochi.Hook) (string, *mochi.Server) {
	t.Helper()
	s := mochi.New(&mochi.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := s.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range hooks {
		if err := s.AddHook(h, nil); err != nil {
			t.Fatal(err)
		}
	}

	l := listeners.NewTCP(listeners.Config{ID: "ix", Address: "127.0.0.1:0"})
	if err := s.AddListener(l); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() {
		// mochi 2.7.9 can deadlock in Close when a client is connecting or
		// disconnecting at that moment: Clients.GetByListener holds the read
		// lock and calls Len, which takes it again, behind a writer waiting in
		// Clients.Delete. Shutting the broker down off the test goroutine
		// keeps that from hanging the run; the listener goes away with the
		// process either way.
		closed := make(chan struct{})
		go func() { defer close(closed); _ = s.Close() }()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Log("broker shutdown did not return: mochi 2.7.9 Clients lock, not the driver")
		}
	})
	return "tcp://" + l.Address(), s
}

// inflightZero asserts every QoS 1 message the broker delivered was
// acknowledged. Nothing acknowledges on delivery here: the PUBACK is
// Inbound.Done, so a message still in flight at the end of a test is work the
// engine reported as unhandled -- or an ack the driver forgot.
func inflightZero(t *testing.T, s *mochi.Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		stuck := 0
		for _, cl := range s.Clients.GetAll() {
			stuck += cl.State.Inflight.Len()
		}
		if stuck == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%d message(s) still unacknowledged: a QoS 1 packet nobody PUBACKs is redelivered forever", stuck)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dial(t *testing.T, url string, opts ...func(*Config)) *Driver {
	t.Helper()
	cfg := Config{URL: url}
	for _, o := range opts {
		o(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

var prefixSeq atomic.Int64

func TestConformance(t *testing.T) {
	url, _ := broker(t)
	drivertest.Run(t, func(t *testing.T) drivertest.Pair {
		// One prefix per pair: the suite builds several pairs against the one
		// broker, and a stale subscription from a finished case must not see
		// the next case's traffic.
		prefix := "ix" + string(rune('a'+prefixSeq.Add(1)%26))
		with := func(c *Config) { c.Prefix = prefix }
		return drivertest.Pair{Server: dial(t, url, with), Client: dial(t, url, with)}
	})
}

// counting wraps a driver to observe what crossed the wire: how many packets
// were published, how many were delivered, and the bytes of the last publish
// so a test can redeliver them exactly as a broker would.
type counting struct {
	interchange.Driver
	published atomic.Int32
	delivered atomic.Int32

	last atomic.Value // publish
}

type publish struct {
	addr string
	body []byte
	hdr  map[string]string
}

func (c *counting) Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error {
	c.published.Add(1)
	c.last.Store(publish{addr: addr, body: body, hdr: hdr})
	return c.Driver.Publish(ctx, addr, body, hdr)
}

func (c *counting) Subscribe(ctx context.Context, pattern, group string, fn func(interchange.Inbound)) (interchange.Unsubscribe, error) {
	return c.Driver.Subscribe(ctx, pattern, group, func(in interchange.Inbound) {
		c.delivered.Add(1)
		fn(in)
	})
}

// serve wires a server and a client over one broker and returns both plus the
// handler's call count.
func serve(t *testing.T, url string, impl *testsvc.Impl, opts ...func(*Config)) (*counting, *counting, *engine.Client) {
	t.Helper()
	prefix := "ixt" + string(rune('a'+prefixSeq.Add(1)%26))
	opts = append(opts, func(c *Config) { c.Prefix = prefix })
	srvDrv := &counting{Driver: dial(t, url, opts...)}
	cliDrv := &counting{Driver: dial(t, url, opts...)}

	reg := interchange.NewRegistry()
	if err := reg.Register(testsvc.Desc(), impl, interchange.DefaultChain(interchange.Config{})); err != nil {
		t.Fatal(err)
	}
	srv := engine.NewServer(srvDrv, reg)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	cli, err := engine.NewClient(context.Background(), cliDrv, engine.WithTimeout(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return srvDrv, cliDrv, cli
}

// TestRedeliverySuppressed replays the exact bytes of a request the way a QoS
// 1 broker does when its PUBACK is lost. AtLeastOnce is why the engine keeps
// a dedupe table; this asserts the handler ran once and the redelivery still
// got answered, because the redelivery usually happened because the first
// answer was lost.
func TestRedeliverySuppressed(t *testing.T) {
	url, brk := broker(t)
	var calls atomic.Int32
	impl := testsvc.EchoImpl()
	impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		calls.Add(1)
		return &commonv1.Problem{Title: "echo:" + in.GetTitle()}, nil
	}
	srvDrv, cliDrv, cli := serve(t, url, impl)

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure,
		&commonv1.Problem{Title: "dup"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	before, replies := srvDrv.delivered.Load(), cliDrv.delivered.Load()

	// The server answers through Inbound.Reply, not through Publish, so the
	// replay is observed where it lands: on the client's reply topic.
	p := cliDrv.last.Load().(publish)
	if err := cliDrv.Driver.Publish(context.Background(), p.addr, p.body, p.hdr); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for cliDrv.delivered.Load() == replies && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if srvDrv.delivered.Load() == before {
		t.Fatal("the redelivered frame never reached the server")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times; replay suppression exists so an at-least-once transport cannot double-execute", n)
	}
	if cliDrv.delivered.Load() == replies {
		t.Error("the redelivery was dropped instead of replayed: the peer that retried is still waiting")
	}
	// The replayed answer counts as handled: the redelivery must be acked too,
	// or the broker keeps retrying a request that has already been answered.
	inflightZero(t, brk)
}

// TestChunking drives MaxPayload below the message size and asserts the
// engine framed the call into several packets and reassembled it whole.
func TestChunking(t *testing.T) {
	url, brk := broker(t)
	const small = 8 << 10
	body := strings.Repeat("x", 5*small)

	impl := testsvc.EchoImpl()
	impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		return &commonv1.Problem{Title: "echo:" + in.GetTitle(), Detail: in.GetDetail()}, nil
	}
	_, cliDrv, cli := serve(t, url, impl, func(c *Config) { c.MaxPayload = small })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out commonv1.Problem
	if err := cli.Invoke(ctx, testsvc.EchoProcedure,
		&commonv1.Problem{Title: "big", Detail: body}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.GetDetail() != body {
		t.Fatalf("payload lost bytes: %d of %d", len(out.GetDetail()), len(body))
	}
	if n := cliDrv.published.Load(); n < 5 {
		t.Fatalf("the request went out in %d packets; %d bytes over a %d ceiling must be chunked",
			n, len(body), small)
	}
	// Every frame of a chunked message is held unacknowledged until the whole
	// message has been handled, so this is where a missing ack shows up.
	inflightZero(t, brk)
}

func TestTopicGrammar(t *testing.T) {
	d := &Driver{prefix: "ix"}
	if got := d.Address("/pkg.Svc/Method"); got != "ix/rpc/pkg.Svc/Method" {
		t.Errorf("Address is %q", got)
	}
	if got := d.ServiceWildcard("pkg.Svc"); got != "ix/rpc/pkg.Svc/+" {
		t.Errorf("ServiceWildcard is %q", got)
	}
	for _, tc := range []struct {
		filter, topic string
		want          bool
	}{
		{"ix/rpc/pkg.Svc/+", "ix/rpc/pkg.Svc/Method", true},
		{"ix/rpc/pkg.Svc/+", "ix/rpc/pkg.Svc/Method/Extra", false},
		{"ix/rpc/pkg.Svc/+", "ix/rpc/other.Svc/Method", false},
		{"ix/rpc/#", "ix/rpc/pkg.Svc/Method", true},
		{"ix/reply/abc", "ix/reply/abc", true},
	} {
		if got := match(tc.filter, tc.topic); got != tc.want {
			t.Errorf("match(%q, %q) = %v", tc.filter, tc.topic, got)
		}
	}
}

// TestSharedSubscription is the CompetingGroup claim under test: two servers
// in one group must share the work, not each answer. Nothing else in the
// suite exercises $share, because the test fixture declares no group.
func TestSharedSubscription(t *testing.T) {
	url, brk := broker(t)
	prefix := "ixs" + string(rune('a'+prefixSeq.Add(1)%26))

	var handled [2]atomic.Int32
	for i := range handled {
		counter := &handled[i]
		impl := testsvc.EchoImpl()
		impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
			counter.Add(1)
			return &commonv1.Problem{Title: "echo:" + in.GetTitle()}, nil
		}
		desc := testsvc.Desc()
		for j := range desc.Methods {
			desc.Methods[j].Group = "workers"
		}
		reg := interchange.NewRegistry()
		if err := reg.Register(desc, impl, interchange.DefaultChain(interchange.Config{})); err != nil {
			t.Fatal(err)
		}
		drv := &counting{Driver: dial(t, url, func(c *Config) { c.Prefix = prefix })}
		srv := engine.NewServer(drv, reg)
		if err := srv.Start(context.Background()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = srv.Stop() })
	}

	cliDrv := &counting{Driver: dial(t, url, func(c *Config) { c.Prefix = prefix })}
	cli, err := engine.NewClient(context.Background(), cliDrv, engine.WithTimeout(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	const n = 20
	for i := range n {
		var out commonv1.Problem
		if err := cli.Invoke(context.Background(), testsvc.EchoProcedure,
			&commonv1.Problem{Title: strconv.Itoa(i)}, &out); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	total := handled[0].Load() + handled[1].Load()
	if total != n {
		t.Fatalf("%d calls ran %d handlers: a shared subscription competes, it does not fan out", n, total)
	}
	inflightZero(t, brk)
	if handled[0].Load() == 0 || handled[1].Load() == 0 {
		t.Errorf("one member took every message (%d/%d): the group is not being shared",
			handled[0].Load(), handled[1].Load())
	}
}

// TestFactory asserts the "mqtt" registration `ix` builds a driver through.
func TestFactory(t *testing.T) {
	url, _ := broker(t)
	d, err := interchange.NewDriver("mqtt", map[string]string{"url": url, "prefix": "org", "qos": "1"})
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	t.Cleanup(func() { _ = d.(*Driver).Close() })
	if got := d.Caps().Name; got != "mqtt" {
		t.Errorf("Caps().Name is %q", got)
	}
	if got := d.Address(testsvc.EchoProcedure); got != "org/rpc/"+testsvc.Service+"/Echo" {
		t.Errorf("Address is %q", got)
	}
	if _, err := interchange.NewDriver("mqtt", map[string]string{"url": url, "qos": "0"}); err == nil {
		t.Error("qos 0 was accepted: a dropped chunk cannot be detected, let alone recovered")
	}
}

// capture records the MQTT 5 properties the broker saw, which is the only
// place to assert them: they are transport-native and never reach the engine.
type capture struct {
	mochi.HookBase
	mu   sync.Mutex
	seen map[string]packets.Packet
}

func (c *capture) Provides(b byte) bool { return b == mochi.OnPublish }

func (c *capture) OnPublish(_ *mochi.Client, pk packets.Packet) (packets.Packet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]packets.Packet{}
	}
	switch {
	case strings.Contains(pk.TopicName, "/rpc/"):
		c.seen["request"] = pk
	case strings.Contains(pk.TopicName, "/reply/"):
		c.seen["reply"] = pk
	}
	return pk, nil
}

// TestCorrelationData asserts the engine's correlation id reaches the wire as
// MQTT 5 Correlation Data and comes back on the reply, which is what lets a
// plain MQTT 5 client -- one that has never heard of an envelope -- match a
// response to its request.
func TestCorrelationData(t *testing.T) {
	cap := &capture{}
	url, brk := broker(t, cap)
	_, _, cli := serve(t, url, testsvc.EchoImpl())

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure,
		&commonv1.Problem{Title: "corr"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}

	cap.mu.Lock()
	req, reply := cap.seen["request"], cap.seen["reply"]
	cap.mu.Unlock()

	if len(req.Properties.CorrelationData) == 0 {
		t.Fatal("the request carried no Correlation Data: an MQTT-native peer has nothing to match on")
	}
	if string(reply.Properties.CorrelationData) != string(req.Properties.CorrelationData) {
		t.Errorf("the reply's Correlation Data is %q, want %q",
			reply.Properties.CorrelationData, req.Properties.CorrelationData)
	}
	inflightZero(t, brk)
	if req.Properties.ResponseTopic == "" {
		t.Error("the request carried no Response Topic")
	}
	for _, u := range req.Properties.User {
		if u.Key == interchange.MetaCorrelationID {
			t.Errorf("%s is still a user property: it is moved to Correlation Data, not copied", u.Key)
		}
	}
}
