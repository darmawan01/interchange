// Package drivertest is the conformance suite every transport driver must
// pass. It is public API: a third party adding a broker runs the same suite
// the drivers in the box run, and a driver that passes it needs no
// broker-specific tests to be trusted by the engine.
//
// The suite asserts the contract between a driver and the engine, which is
// smaller than it looks: six methods and an honest Capabilities value. Every
// test below is really one question -- does the engine still work when this
// capability is absent?
package drivertest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/engine"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	"github.com/darmawan01/interchange/internal/testsvc"
)

// Pair is a server-side and a client-side driver attached to the same
// transport. They may be the same value if the driver is symmetric.
type Pair struct {
	Server interchange.Driver
	Client interchange.Driver
}

// Factory builds a fresh Pair for one test. Register any cleanup with t.
type Factory func(t *testing.T) Pair

// Run executes the conformance suite.
func Run(t *testing.T, newPair Factory) {
	t.Helper()
	t.Run("Capabilities", func(t *testing.T) { testCapabilities(t, newPair) })
	t.Run("Unary", func(t *testing.T) { testUnary(t, newPair) })
	t.Run("Error", func(t *testing.T) { testError(t, newPair) })
	t.Run("Metadata", func(t *testing.T) { testMetadata(t, newPair) })
	t.Run("Deadline", func(t *testing.T) { testDeadline(t, newPair) })
	t.Run("UnknownProcedure", func(t *testing.T) { testUnknown(t, newPair) })
	t.Run("Concurrent", func(t *testing.T) { testConcurrent(t, newPair) })
	t.Run("LargePayload", func(t *testing.T) { testLarge(t, newPair) })
	t.Run("Addressing", func(t *testing.T) { testAddressing(t, newPair) })
}

type harness struct {
	client *engine.Client
	caps   interchange.Capabilities
}

func start(t *testing.T, newPair Factory, impl *testsvc.Impl, opts ...engine.ClientOption) *harness {
	t.Helper()
	pair := newPair(t)

	reg := interchange.NewRegistry()
	if err := reg.Register(testsvc.Desc(), impl, interchange.DefaultChain(interchange.Config{})); err != nil {
		t.Fatal(err)
	}
	srv := engine.NewServer(pair.Server, reg)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	opts = append([]engine.ClientOption{engine.WithTimeout(10 * time.Second)}, opts...)
	cli, err := engine.NewClient(context.Background(), pair.Client, opts...)
	if err != nil {
		t.Fatalf("start client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	// A broker subscription is not always live the instant Subscribe returns.
	// One warm-up call, retried briefly, is cheaper than a sleep in every test.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var out commonv1.Problem
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err := cli.Invoke(ctx, testsvc.EchoProcedure, &commonv1.Problem{Title: "warmup"}, &out)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("driver never became ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return &harness{client: cli, caps: pair.Client.Caps()}
}

func testCapabilities(t *testing.T, newPair Factory) {
	pair := newPair(t)
	caps := pair.Server.Caps()
	if caps.Name == "" {
		t.Error("Capabilities.Name is empty: `ix describe` and every diagnostic identifies a driver by it")
	}
	if caps.Transport == 0 {
		t.Error("Capabilities.Transport is unset: the engine cannot tell which procedures to subscribe")
	}
	if caps.MaxPayload < 0 {
		t.Errorf("Capabilities.MaxPayload is %d; use 0 for no ceiling", caps.MaxPayload)
	}
	if !caps.NativeReply && pair.Client.ReplyAddress() == "" {
		t.Error("a driver with no native reply must supply a ReplyAddress for the engine to fall back to")
	}
	proc := testsvc.EchoProcedure
	if pair.Server.Address(proc) == "" {
		t.Error("Address returned an empty channel name")
	}
	if pair.Server.ServiceWildcard(testsvc.Service) == "" {
		t.Error("ServiceWildcard returned an empty pattern")
	}
}

func testUnary(t *testing.T, newPair Factory) {
	h := start(t, newPair, testsvc.EchoImpl())
	var out commonv1.Problem
	if err := h.client.Invoke(context.Background(), testsvc.EchoProcedure,
		&commonv1.Problem{Title: "hello"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.GetTitle() != "echo:hello" {
		t.Fatalf("response is %q, want %q", out.GetTitle(), "echo:hello")
	}
}

func testError(t *testing.T, newPair Factory) {
	h := start(t, newPair, testsvc.EchoImpl())
	var out commonv1.Problem
	err := h.client.Invoke(context.Background(), testsvc.FailProcedure, &commonv1.Problem{}, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := interchange.CodeOf(err); got != interchange.CodeNotFound {
		t.Errorf("code is %v, want not_found -- the status must survive the transport", got)
	}
	if got := interchange.ReasonOf(err); got != "PROVIDER_NOT_FOUND" {
		t.Errorf("reason is %q, want PROVIDER_NOT_FOUND -- a client branches on this", got)
	}
}

func testMetadata(t *testing.T, newPair Factory) {
	seen := make(chan string, 1)
	impl := testsvc.EchoImpl()
	inner := impl.Echo
	impl.Echo = func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		return inner(ctx, in)
	}

	pair := newPair(t)
	reg := interchange.NewRegistry()
	chain := interchange.Chain(interchange.Named("capture", func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			select {
			case seen <- req.Metadata.Get("authorization"):
			default:
			}
			return next(ctx, req)
		}
	}))
	if err := reg.Register(testsvc.Desc(), impl, chain); err != nil {
		t.Fatal(err)
	}
	srv := engine.NewServer(pair.Server, reg)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	cli, err := engine.NewClient(context.Background(), pair.Client,
		engine.WithStaticMetadata(interchange.Metadata{"authorization": "Bearer tok"}),
		engine.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	var out commonv1.Problem
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = cli.Invoke(ctx, testsvc.EchoProcedure, &commonv1.Problem{Title: "md"}, &out)
		cancel()
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	select {
	case got := <-seen:
		if got != "Bearer tok" {
			t.Fatalf("metadata arrived as %q; a credential must survive whether the transport has native headers or not", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the interceptor never saw the request")
	}
}

func testDeadline(t *testing.T, newPair Factory) {
	got := make(chan bool, 1)
	impl := testsvc.EchoImpl()
	impl.Echo = func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		_, ok := ctx.Deadline()
		select {
		case got <- ok:
		default:
		}
		return &commonv1.Problem{Title: "echo:" + in.GetTitle()}, nil
	}
	h := start(t, newPair, impl)
	// Drain the warm-up call's observation.
	select {
	case <-got:
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out commonv1.Problem
	if err := h.client.Invoke(ctx, testsvc.EchoProcedure, &commonv1.Problem{Title: "dl"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("the handler context carried no deadline: deadline_unix_ms did not cross the wire")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}
}

func testUnknown(t *testing.T, newPair Factory) {
	h := start(t, newPair, testsvc.EchoImpl())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out commonv1.Problem
	err := h.client.Invoke(ctx, interchange.Procedure(testsvc.Service, "NoSuchMethod"), &commonv1.Problem{}, &out)
	if code := interchange.CodeOf(err); code != interchange.CodeUnimplemented {
		t.Fatalf("code is %v, want unimplemented -- a caller must not hang on a method that does not exist", code)
	}
}

func testConcurrent(t *testing.T, newPair Factory) {
	h := start(t, newPair, testsvc.EchoImpl())
	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out commonv1.Problem
			title := strings.Repeat("a", i+1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := h.client.Invoke(ctx, testsvc.EchoProcedure, &commonv1.Problem{Title: title}, &out); err != nil {
				errs <- err
				return
			}
			if out.GetTitle() != "echo:"+title {
				// Correlation is the whole point: a mismatched reply here
				// means responses were matched by arrival order.
				errs <- interchange.Errorf(interchange.CodeInternal,
					"reply mismatch: got %q, want %q", out.GetTitle(), "echo:"+title)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func testLarge(t *testing.T, newPair Factory) {
	pair := newPair(t)
	size := 256 * 1024
	if max := pair.Client.Caps().MaxPayload; max > 0 && size < max*3 {
		size = max * 3
	}
	body := strings.Repeat("x", size)

	impl := testsvc.EchoImpl()
	impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		if in.GetDetail() != "" && len(in.GetDetail()) != size {
			return nil, interchange.Errorf(interchange.CodeInternal,
				"request lost bytes: %d of %d", len(in.GetDetail()), size)
		}
		return &commonv1.Problem{Title: "echo:" + in.GetTitle(), Detail: in.GetDetail()}, nil
	}
	h := start(t, newPair, impl)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out commonv1.Problem
	if err := h.client.Invoke(ctx, testsvc.EchoProcedure,
		&commonv1.Problem{Title: "big", Detail: body}, &out); err != nil {
		t.Fatalf("large call: %v -- a payload over the ceiling must be chunked, not rejected", err)
	}
	if len(out.GetDetail()) != size {
		t.Fatalf("response lost bytes: %d of %d", len(out.GetDetail()), size)
	}
}

func testAddressing(t *testing.T, newPair Factory) {
	pair := newPair(t)
	d := pair.Server
	addr := d.Address(testsvc.EchoProcedure)
	wildcard := d.ServiceWildcard(testsvc.Service)
	if addr == wildcard {
		t.Error("Address and ServiceWildcard returned the same string: a wildcard that matches one method is not a wildcard")
	}
	// Two different procedures must not share a channel unless the transport
	// genuinely has one channel, in which case the envelope carries the
	// procedure and Address is allowed to be constant.
	other := d.Address(testsvc.FailProcedure)
	if addr == other && !isSingleChannel(d) {
		t.Errorf("Address(%s) and Address(%s) are both %q on a transport with more than one channel",
			testsvc.EchoProcedure, testsvc.FailProcedure, addr)
	}
}

// isSingleChannel reports the degenerate case: one socket, so the procedure
// lives entirely in the envelope. WebSocket is this; brokers are not.
func isSingleChannel(d interchange.Driver) bool {
	return d.Address("/a.B/C") == d.Address("/x.Y/Z")
}
