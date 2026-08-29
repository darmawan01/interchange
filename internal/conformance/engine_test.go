package conformance

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	"github.com/darmawan01/interchange/internal/testsvc"
)

// busPair wires a server and a client onto one bus with the given
// capabilities. Every test below is the same test with a different
// Capabilities value -- which is the property that lets the engine be written
// once and adapt, rather than switch on transport type.
func busPair(t *testing.T, caps interchange.Capabilities, impl *testsvc.Impl, opts ...memory.Option) *engine.Client {
	t.Helper()
	reg := interchange.NewRegistry()
	if err := reg.Register(testsvc.Desc(), impl, interchange.DefaultChain(interchange.Config{})); err != nil {
		t.Fatal(err)
	}
	bus := memory.New(append([]memory.Option{memory.WithCapabilities(caps)}, opts...)...)
	srv := engine.NewServer(bus.Driver("server"), reg)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	cli, err := engine.NewClient(context.Background(), bus.Driver("client"), engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// TestMetadataFallback: a transport with neither native headers nor a native
// reply carries both in the envelope, and the handler cannot tell.
func TestMetadataFallback(t *testing.T) {
	var seen string
	impl := testsvc.EchoImpl()
	impl.Echo = func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		md, _ := interchange.MethodFromContext(ctx)
		if md == nil {
			t.Error("dispatch did not put the method in the context")
		}
		return &commonv1.Problem{Title: "echo:" + in.GetTitle()}, nil
	}

	caps := memory.DefaultCapabilities()
	caps.NativeHeaders = false
	caps.NativeReply = false

	reg := interchange.NewRegistry()
	chain := interchange.Chain(interchange.Named("capture", func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			seen = req.Metadata.Get("authorization")
			return next(ctx, req)
		}
	}))
	if err := reg.Register(testsvc.Desc(), impl, chain); err != nil {
		t.Fatal(err)
	}
	bus := memory.New(memory.WithCapabilities(caps))
	srv := engine.NewServer(bus.Driver("server"), reg)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	cli, err := engine.NewClient(context.Background(), bus.Driver("client"),
		engine.WithStaticMetadata(interchange.Metadata{"authorization": "Bearer tok"}),
		engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{Title: "hi"}, &out); err != nil {
		t.Fatalf("call over a transport with no headers and no native reply: %v", err)
	}
	if out.GetTitle() != "echo:hi" {
		t.Fatalf("response is %q", out.GetTitle())
	}
	if seen != "Bearer tok" {
		t.Fatalf("credential did not survive the fallback: %q", seen)
	}
}

// TestChunking: a payload over the transport's ceiling is split into frames
// and reassembled, in both directions, with no per-service code.
func TestChunking(t *testing.T) {
	big := strings.Repeat("x", 40_000)
	impl := testsvc.EchoImpl()
	impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		if len(in.GetDetail()) != len(big) {
			return nil, interchange.Errorf(interchange.CodeInternal, "request lost bytes: %d of %d", len(in.GetDetail()), len(big))
		}
		return &commonv1.Problem{Title: "ok", Detail: in.GetDetail()}, nil
	}

	caps := memory.DefaultCapabilities()
	caps.MaxPayload = 1024
	cli := busPair(t, caps, impl)

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure,
		&commonv1.Problem{Title: "big", Detail: big}, &out); err != nil {
		t.Fatalf("chunked call: %v", err)
	}
	if out.GetDetail() != big {
		t.Fatalf("response lost bytes: %d of %d", len(out.GetDetail()), len(big))
	}
}

// TestReplaySuppression: an at-least-once transport redelivers, and the
// handler runs once. The second delivery replays the cached response rather
// than dropping the call, because the redelivery usually happened *because*
// the first response was lost.
func TestReplaySuppression(t *testing.T) {
	var calls atomic.Int32
	impl := testsvc.EchoImpl()
	impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		calls.Add(1)
		return &commonv1.Problem{Title: "echo:" + in.GetTitle()}, nil
	}

	caps := memory.DefaultCapabilities()
	caps.AtLeastOnce = true
	cli := busPair(t, caps, impl, memory.WithDuplicateDelivery())

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{Title: "once"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.GetTitle() != "echo:once" {
		t.Fatalf("response is %q", out.GetTitle())
	}
	// Give the duplicate delivery time to arrive and be suppressed.
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times under at-least-once delivery, want 1", n)
	}
}

// TestDeadlineCrossesTheWire: the client's context deadline becomes the
// server's, which is the thing HTTP gives free and a bus does not.
func TestDeadlineCrossesTheWire(t *testing.T) {
	got := make(chan time.Duration, 1)
	impl := testsvc.EchoImpl()
	impl.Echo = func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			got <- 0
			return nil, interchange.Errorf(interchange.CodeInternal, "handler context carries no deadline")
		}
		got <- time.Until(dl)
		return &commonv1.Problem{Title: "ok"}, nil
	}
	cli := busPair(t, memory.DefaultCapabilities(), impl)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out commonv1.Problem
	if err := cli.Invoke(ctx, testsvc.EchoProcedure, &commonv1.Problem{}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	remaining := <-got
	if remaining <= 0 || remaining > 2*time.Second {
		t.Fatalf("server deadline is %v, want just under 2s", remaining)
	}
}

// TestExpiredDeadlineIsNotDispatched: a message that sat in a queue past its
// deadline must not start work nobody is waiting for.
func TestExpiredDeadlineIsNotDispatched(t *testing.T) {
	var calls atomic.Int32
	impl := testsvc.EchoImpl()
	impl.Echo = func(context.Context, *commonv1.Problem) (*commonv1.Problem, error) {
		calls.Add(1)
		return &commonv1.Problem{}, nil
	}
	reg := interchange.NewRegistry()
	if err := reg.Register(testsvc.Desc(), impl, interchange.DefaultChain(interchange.Config{})); err != nil {
		t.Fatal(err)
	}
	env := &interchange.Envelope{
		Procedure: testsvc.EchoProcedure,
		Payload:   nil,
		Deadline:  time.Now().Add(-time.Second),
	}
	_, err := reg.Dispatch(context.Background(), env)
	if interchange.CodeOf(err) != interchange.CodeDeadlineExceeded {
		t.Fatalf("expected deadline_exceeded, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("the handler ran for a call whose deadline had already passed")
	}
}

// TestClientTimeoutEvictsThePendingCall: no response, no leak.
func TestClientTimeoutEvictsThePendingCall(t *testing.T) {
	impl := testsvc.EchoImpl()
	impl.Echo = func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	cli := busPair(t, memory.DefaultCapabilities(), impl)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var out commonv1.Problem
	err := cli.Invoke(ctx, testsvc.EchoProcedure, &commonv1.Problem{}, &out)
	if code := interchange.CodeOf(err); code != interchange.CodeDeadlineExceeded {
		t.Fatalf("expected deadline_exceeded, got %v (%v)", code, err)
	}
}

// TestUnknownProcedure: the engine answers rather than hanging, so a caller
// learns immediately that a method it asked for does not exist here.
func TestUnknownProcedure(t *testing.T) {
	cli := busPair(t, memory.DefaultCapabilities(), testsvc.EchoImpl())
	var out commonv1.Problem
	err := cli.Invoke(context.Background(), interchange.Procedure(testsvc.Service, "Nope"), &commonv1.Problem{}, &out)
	if code := interchange.CodeOf(err); code != interchange.CodeUnimplemented {
		t.Fatalf("expected unimplemented, got %v (%v)", code, err)
	}
}

// TestPanicBecomesAnError: on a bus a dropped connection takes the subscriber
// with it, so recover matters more here than behind an HTTP server.
func TestPanicBecomesAnError(t *testing.T) {
	impl := testsvc.EchoImpl()
	impl.Echo = func(context.Context, *commonv1.Problem) (*commonv1.Problem, error) {
		panic("boom")
	}
	cli := busPair(t, memory.DefaultCapabilities(), impl)
	var out commonv1.Problem
	err := cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{}, &out)
	if code := interchange.CodeOf(err); code != interchange.CodeInternal {
		t.Fatalf("expected internal, got %v (%v)", code, err)
	}
}
