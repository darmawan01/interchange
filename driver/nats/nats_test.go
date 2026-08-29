package nats_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	natsdriver "github.com/darmawan01/interchange/driver/nats"
	"github.com/darmawan01/interchange/drivertest"
	"github.com/darmawan01/interchange/engine"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

// runServer starts a NATS server inside the test binary. The suite needs a
// real broker -- a fake one would only prove the driver agrees with itself --
// but it must not need one installed.
func runServer(t *testing.T, js bool, maxPayload int32) *server.Server {
	t.Helper()
	opts := &server.Options{Port: -1, NoLog: true, NoSigs: true, JetStream: js}
	if js {
		opts.StoreDir = t.TempDir()
	}
	if maxPayload > 0 {
		opts.MaxPayload = maxPayload
	}
	s := natsserver.RunServer(opts)
	t.Cleanup(s.Shutdown)
	return s
}

func connect(t *testing.T, s *server.Server) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func newDriver(t *testing.T, s *server.Server, js bool) *natsdriver.Driver {
	t.Helper()
	var (
		d   *natsdriver.Driver
		err error
	)
	if js {
		d, err = natsdriver.NewJetStream(context.Background(), connect(t, s))
	} else {
		d, err = natsdriver.New(connect(t, s))
	}
	if err != nil {
		t.Fatalf("driver: %v", err)
	}
	return d
}

func pairs(js bool, maxPayload int32) drivertest.Factory {
	return func(t *testing.T) drivertest.Pair {
		s := runServer(t, js, maxPayload)
		return drivertest.Pair{Server: newDriver(t, s, js), Client: newDriver(t, s, js)}
	}
}

func TestConformance(t *testing.T) {
	drivertest.Run(t, pairs(false, 0))
}

// The durable tier runs the same suite. It differs only in its Capabilities:
// at-least-once on, native reply off, because JetStream does not store the
// publisher's reply subject and the engine falls back to the envelope.
func TestConformanceJetStream(t *testing.T) {
	s := runServer(t, true, 0)
	if _, err := natsdriver.NewJetStream(context.Background(), connect(t, s)); err != nil {
		t.Skipf("in-process JetStream unavailable: %v", err)
	}
	drivertest.Run(t, pairs(true, 0))
}

// A broker with an 8 KiB ceiling has to carry a 32 KiB call, which it can
// only do if the engine chunked it. Counting the frames on the wire says so
// directly; a passing round trip alone would not distinguish chunking from a
// server that quietly raised its limit.
func TestMaxPayloadChunking(t *testing.T) {
	const ceiling = 8 * 1024
	s := runServer(t, false, ceiling)
	drv := newDriver(t, s, false)

	caps := drv.Caps()
	if caps.MaxPayload <= 0 || caps.MaxPayload >= ceiling {
		t.Fatalf("MaxPayload is %d; want the server's negotiated %d less headroom", caps.MaxPayload, ceiling)
	}
	body := bytes.Repeat([]byte("x"), 32*1024)
	if err := drv.Publish(context.Background(), drv.Address("/chunk.Svc/Big"), body, nil); err == nil {
		t.Fatal("a 32 KiB publish over an 8 KiB broker succeeded: the ceiling this test rests on is not real")
	}

	sniffConn := connect(t, s)
	sniff, err := sniffConn.SubscribeSync("rpc.>")
	if err != nil {
		t.Fatal(err)
	}
	if err := sniffConn.Flush(); err != nil {
		t.Fatal(err)
	}

	cli, err := engine.NewClient(context.Background(), drv, engine.WithTimeout(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	// Nothing is serving the procedure; the call is expected to time out. The
	// frames it put on the wire before that are what is under test.
	go func() {
		_, _ = cli.Do(context.Background(), &transportv1.Request{
			Procedure: interchange.Procedure("chunk.Svc", "Big"),
			Payload:   body,
			Codec:     interchange.CodecProto,
		})
	}()

	var frames int
	for {
		msg, err := sniff.NextMsg(2 * time.Second)
		if err != nil {
			break
		}
		frames++
		if len(msg.Data) > ceiling {
			t.Fatalf("frame %d is %d bytes, over the %d ceiling", frames, len(msg.Data), ceiling)
		}
	}
	if frames < 2 {
		t.Fatalf("the 32 KiB call arrived as %d frame(s): it was not chunked", frames)
	}
}

// One message, two servers in a queue group, one handler call. This is the
// only capability the conformance suite cannot exercise on its own: the test
// service declares no groups, so without this the durable-consumer path in
// JetStream mode is never touched.
func TestQueueGroupCompetes(t *testing.T) {
	for _, tc := range []struct {
		name string
		js   bool
	}{{"core", false}, {"jetstream", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := runServer(t, tc.js, 0)
			var calls atomic.Int32
			got := make(chan struct{}, 4)
			for range 2 {
				d := newDriver(t, s, tc.js)
				unsub, err := d.Subscribe(context.Background(), d.ServiceWildcard("queue.Svc"), "workers",
					func(in interchange.Inbound) {
						// Core NATS has no acknowledgement to give, so Done
						// stays nil there and is wired only on the durable tier.
						if hasDone := in.Done != nil; hasDone != tc.js {
							t.Errorf("Inbound.Done non-nil = %v, want %v", hasDone, tc.js)
						}
						if in.Done != nil {
							in.Done(nil)
						}
						calls.Add(1)
						got <- struct{}{}
					})
				if err != nil {
					t.Fatalf("subscribe: %v", err)
				}
				t.Cleanup(func() { _ = unsub() })
			}

			pub := newDriver(t, s, tc.js)
			if err := pub.Publish(context.Background(), pub.Address("/queue.Svc/Do"), []byte("one"), nil); err != nil {
				t.Fatalf("publish: %v", err)
			}
			select {
			case <-got:
			case <-time.After(5 * time.Second):
				t.Fatal("no member of the queue group was delivered to")
			}
			// A second delivery would arrive within this window; its absence
			// is the assertion.
			time.Sleep(300 * time.Millisecond)
			if n := calls.Load(); n != 1 {
				t.Fatalf("%d handlers ran for one message; a queue group means exactly one", n)
			}
		})
	}
}

// Ack on completion, not on delivery: a message the engine reports as
// unhandled must come back. That is the difference between at-least-once
// delivery and at-least-once processing, and it is the whole reason the
// durable tier claims AtLeastOnce.
func TestJetStreamRedeliversUnhandled(t *testing.T) {
	s := runServer(t, true, 0)
	sub := newDriver(t, s, true)

	var attempts atomic.Int32
	got := make(chan int32, 4)
	unsub, err := sub.Subscribe(context.Background(), sub.ServiceWildcard("redeliver.Svc"), "workers",
		func(in interchange.Inbound) {
			if in.Done == nil {
				t.Error("a JetStream Inbound must carry Done: without it the ack is a lie")
				return
			}
			n := attempts.Add(1)
			if n == 1 {
				in.Done(errors.New("handler failed"))
			} else {
				in.Done(nil)
			}
			got <- n
		})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = unsub() })

	pub := newDriver(t, s, true)
	if err := pub.Publish(context.Background(), pub.Address("/redeliver.Svc/Do"), []byte("one"), nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	for want := int32(1); want <= 2; want++ {
		select {
		case n := <-got:
			if n != want {
				t.Fatalf("delivery %d arrived as attempt %d", want, n)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("only %d delivery(s): a naked message must be redelivered", want-1)
		}
	}
}

func TestRegisteredFactory(t *testing.T) {
	s := runServer(t, false, 0)
	// A stream name without jetstream:true must leave a plain core driver:
	// half a durable tier is the one state this config surface can reach.
	d, err := interchange.NewDriver("nats", map[string]string{
		"url": s.ClientURL(), "prefix": "acme", "stream": "ACME",
	})
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	t.Cleanup(func() { _ = d.(interchange.Closer).Close() })
	if got := d.Address("/pkg.Svc/Method"); got != "acme.pkg.Svc.Method" {
		t.Fatalf("Address is %q, want acme.pkg.Svc.Method -- the prefix config key did not take", got)
	}
	if got := d.ServiceWildcard("pkg.Svc"); got != "acme.pkg.Svc.*" {
		t.Fatalf("ServiceWildcard is %q, want acme.pkg.Svc.*", got)
	}
	if caps := d.Caps(); !caps.NativeReply || caps.AtLeastOnce {
		t.Fatalf("caps are %+v; a driver built without jetstream:true is core NATS", caps)
	}
	if err := d.Publish(context.Background(), d.Address("/pkg.Svc/Method"), []byte("x"), nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
}
