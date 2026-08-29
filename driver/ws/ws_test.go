package ws_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/driver/ws"
	"github.com/darmawan01/interchange/drivertest"
	"github.com/darmawan01/interchange/engine"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/darmawan01/interchange/internal/testsvc"
	"google.golang.org/protobuf/proto"
)

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

// serve mounts h and returns a driver dialled to it. Cleanups are registered
// so the socket closes before the HTTP server does.
func serve(t *testing.T, h http.Handler, dial ...ws.Option) *ws.Driver {
	t.Helper()
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	d, err := ws.Dial(context.Background(), wsURL(hs), dial...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func registry(t *testing.T, impl *testsvc.Impl, chain *interchange.ChainSpec) *interchange.Registry {
	t.Helper()
	reg := interchange.NewRegistry()
	if err := reg.Register(testsvc.Desc(), impl, chain); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestConformance(t *testing.T) {
	drivertest.Run(t, func(t *testing.T) drivertest.Pair {
		conns := make(chan *ws.Conn, 1)
		hs := httptest.NewServer(ws.Handler(func(c *ws.Conn) error {
			conns <- c
			return nil
		}))
		t.Cleanup(hs.Close)

		client, err := ws.Dial(context.Background(), wsURL(hs))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })

		select {
		case c := <-conns:
			return drivertest.Pair{Server: c.Driver(), Client: client}
		case <-time.After(5 * time.Second):
			t.Fatal("the upgrade handler never ran")
			return drivertest.Pair{}
		}
	})
}

// TestHandshakeCredential closes the browser case: no Authorization header is
// possible on an upgrade, so the credential arrives in the first frame and
// every later call on that socket must reach the chain carrying it.
func TestHandshakeCredential(t *testing.T) {
	seen := make(chan string, 8)
	chain := interchange.Chain(interchange.Named("capture", func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			seen <- req.Metadata.Get("authorization")
			return next(ctx, req)
		}
	}))
	reg := registry(t, testsvc.EchoImpl(), chain)

	// Mixed case on purpose: a browser writes whatever it likes, and the
	// connection map and the envelope map must not end up with two keys.
	d := serve(t, ws.NewServer(reg),
		ws.WithHandshakeMetadata(interchange.Metadata{"Authorization": "Bearer tok"}))

	// No client metadata anywhere: the handshake frame is the only source.
	cli, err := engine.NewClient(context.Background(), d, engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{Title: "hi"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := <-seen; got != "Bearer tok" {
		t.Fatalf("the chain saw authorization=%q, want %q: a handshake credential must arrive exactly as an HTTP header would", got, "Bearer tok")
	}

	// A per-call value beats the connection's, the same precedence the engine
	// gives a native header that the envelope also carries.
	payload, err := proto.Marshal(&commonv1.Problem{Title: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Do(context.Background(), &transportv1.Request{
		Procedure: testsvc.EchoProcedure,
		Payload:   payload,
		Codec:     interchange.CodecProto,
		Metadata:  map[string]string{"authorization": "Bearer call"},
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := <-seen; got != "Bearer call" {
		t.Fatalf("the chain saw authorization=%q, want the per-call value to win", got)
	}
}

// TestRequestMetadata is the other half of the browser case: a credential on
// the upgrade URL, merged into the same connection map.
func TestRequestMetadata(t *testing.T) {
	seen := make(chan string, 4)
	chain := interchange.Chain(interchange.Named("capture", func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			seen <- req.Metadata.Get("authorization")
			return next(ctx, req)
		}
	}))
	reg := registry(t, testsvc.EchoImpl(), chain)

	h := ws.NewServer(reg, ws.WithRequestMetadata(func(r *http.Request) interchange.Metadata {
		return interchange.Metadata{"authorization": "Bearer " + r.URL.Query().Get("token")}
	}))
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)

	d, err := ws.Dial(context.Background(), wsURL(hs)+"?token=urltok")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	cli, err := engine.NewClient(context.Background(), d, engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{Title: "hi"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := <-seen; got != "Bearer urltok" {
		t.Fatalf("the chain saw authorization=%q, want %q", got, "Bearer urltok")
	}
}

// TestConcurrentCalls: one pipe, many calls in flight. The engine's
// correlation map does the matching; this asserts the driver does not
// serialise the socket or hand a reply to the wrong caller.
func TestConcurrentCalls(t *testing.T) {
	d := serve(t, ws.NewServer(registry(t, testsvc.EchoImpl(), interchange.DefaultChain(interchange.Config{}))))
	cli, err := engine.NewClient(context.Background(), d, engine.WithTimeout(15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			title := strings.Repeat("q", i+1)
			var out commonv1.Problem
			if err := cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{Title: title}, &out); err != nil {
				errs <- err
				return
			}
			if out.GetTitle() != "echo:"+title {
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

// TestLargePayloadChunks proves the chunking is real rather than incidental:
// the read limit is set below the message, so a payload that crossed the
// socket whole would be rejected by the peer.
func TestLargePayloadChunks(t *testing.T) {
	const (
		maxPayload = 4 << 10
		readLimit  = 8 << 10
		size       = 256 << 10
	)
	opts := []ws.Option{ws.WithMaxPayload(maxPayload), ws.WithReadLimit(readLimit)}

	impl := testsvc.EchoImpl()
	impl.Echo = func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		if len(in.GetDetail()) != size {
			return nil, interchange.Errorf(interchange.CodeInternal,
				"request lost bytes: %d of %d", len(in.GetDetail()), size)
		}
		return &commonv1.Problem{Title: "echo:" + in.GetTitle(), Detail: in.GetDetail()}, nil
	}
	d := serve(t, ws.NewServer(registry(t, impl, interchange.DefaultChain(interchange.Config{})), opts...), opts...)
	cli, err := engine.NewClient(context.Background(), d, engine.WithTimeout(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure,
		&commonv1.Problem{Title: "big", Detail: strings.Repeat("x", size)}, &out); err != nil {
		t.Fatalf("large call: %v", err)
	}
	if len(out.GetDetail()) != size {
		t.Fatalf("response lost bytes: %d of %d", len(out.GetDetail()), size)
	}
}

// TestCloseFailsInFlight: a dead socket must fail a pending call, not leave
// it to time out. The driver is an interchange.Watcher and the engine does
// the rest.
func TestCloseFailsInFlight(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)

	impl := testsvc.EchoImpl()
	impl.Echo = func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return &commonv1.Problem{Title: "echo:" + in.GetTitle()}, nil
	}
	d := serve(t, ws.NewServer(registry(t, impl, interchange.DefaultChain(interchange.Config{}))))
	cli, err := engine.NewClient(context.Background(), d, engine.WithTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		var out commonv1.Problem
		done <- cli.Invoke(context.Background(), testsvc.EchoProcedure, &commonv1.Problem{Title: "stuck"}, &out)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if code := interchange.CodeOf(err); code != interchange.CodeUnavailable {
			t.Fatalf("in-flight call ended with %v (%v), want unavailable", code, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the in-flight call outlived the socket: a closed connection must fail it, not leave it to its deadline")
	}
}

// TestOneChannel is the degenerate addressing, asserted directly: two
// procedures share a channel and the procedure lives in the envelope.
func TestOneChannel(t *testing.T) {
	d := serve(t, ws.Handler(func(*ws.Conn) error { return nil }))
	if got, other := d.Address(testsvc.EchoProcedure), d.Address(testsvc.FailProcedure); got != other {
		t.Errorf("Address is %q for one procedure and %q for another; a socket has one channel", got, other)
	}
	if d.Address(testsvc.EchoProcedure) == d.ServiceWildcard(testsvc.Service) {
		t.Error("Address and ServiceWildcard are the same string: the engine's wildcard plan cannot be told from a per-procedure one")
	}
	if caps := d.Caps(); caps.NativeHeaders || !caps.NativeReply || caps.CompetingGroup ||
		caps.Transport != transportv1.Transport_TRANSPORT_WS {
		t.Errorf("capabilities are dishonest: %+v", caps)
	}
}

// TestHandshakeCredentialSurvivesChunking: the credential rides beside the
// envelope, not inside it, so a request too big for one frame still arrives
// carrying it. This is the case the driver could not cover while it was
// rewriting envelopes.
func TestHandshakeCredentialSurvivesChunking(t *testing.T) {
	const size = 64 << 10
	seen := make(chan string, 4)
	chain := interchange.Chain(interchange.Named("capture", func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			seen <- req.Metadata.Get("authorization")
			return next(ctx, req)
		}
	}))
	opts := []ws.Option{ws.WithMaxPayload(4 << 10), ws.WithReadLimit(8 << 10)}
	d := serve(t, ws.NewServer(registry(t, testsvc.EchoImpl(), chain), opts...),
		append(opts, ws.WithHandshakeMetadata(interchange.Metadata{"authorization": "Bearer tok"}))...)

	cli, err := engine.NewClient(context.Background(), d, engine.WithTimeout(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	var out commonv1.Problem
	if err := cli.Invoke(context.Background(), testsvc.EchoProcedure,
		&commonv1.Problem{Title: "big", Detail: strings.Repeat("x", size)}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := <-seen; got != "Bearer tok" {
		t.Fatalf("the chain saw authorization=%q on a chunked request, want %q", got, "Bearer tok")
	}
}
