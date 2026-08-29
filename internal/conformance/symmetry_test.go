// Package conformance holds the tests that assert the one behavioural
// invariant core imposes: whatever chain you configure runs identically on
// every transport.
//
// It lives in its own package so it can import a binding and a driver
// without core importing either.
package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/darmawan01/interchange/internal/testsvc"
)

// tracer records the stages a call traversed, per procedure, so two roads can
// be compared without either knowing it is being watched.
type tracer struct {
	mu  sync.Mutex
	log map[string][]string
}

func newTracer() *tracer { return &tracer{log: map[string][]string{}} }

func (t *tracer) stage(name string) interchange.Stage {
	return interchange.Named(name, func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			t.mu.Lock()
			key := req.Metadata.Get("x-trace")
			t.log[key] = append(t.log[key], name)
			t.mu.Unlock()
			return next(ctx, req)
		}
	})
}

func (t *tracer) trace(key string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.log[key])
}

type stack struct {
	reg     *interchange.Registry
	http    *httptest.Server
	client  *rpc.Client
	bus     *memory.Bus
	busSrv  *engine.Server
	busCli  *engine.Client
	chain   *interchange.ChainSpec
	tracer  *tracer
	binding *rpc.Binding
}

func newStack(t *testing.T, opts ...memory.Option) *stack {
	t.Helper()
	tr := newTracer()
	// The chain is configured once, by name, and handed to Register once.
	// Neither binding is given the chain itself.
	chain := interchange.DefaultChain(interchange.Config{}).
		After("deadline", tr.stage("authz")).
		Append(tr.stage("validate"))
	for _, name := range []string{"telemetry", "recover", "deadline"} {
		chain = chain.Replace(name, tr.stage(name))
	}
	if err := chain.Err(); err != nil {
		t.Fatal(err)
	}

	reg := interchange.NewRegistry()
	if err := reg.Register(testsvc.Desc(), testsvc.EchoImpl(), chain); err != nil {
		t.Fatal(err)
	}

	binding := rpc.New(reg)
	if err := binding.Mount(testsvc.Desc()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(binding.Handler())
	t.Cleanup(srv.Close)

	bus := memory.New(opts...)
	busSrv := engine.NewServer(bus.Driver("server"), reg)
	if err := busSrv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busSrv.Stop() })

	busCli, err := engine.NewClient(context.Background(), bus.Driver("client"),
		engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busCli.Close() })

	return &stack{
		reg: reg, http: srv, client: rpc.NewClient(http.DefaultClient, srv.URL),
		bus: bus, busSrv: busSrv, busCli: busCli, chain: chain, tracer: tr, binding: binding,
	}
}

func (s *stack) method(t *testing.T, procedure string) *interchange.MethodDesc {
	t.Helper()
	md, ok := s.reg.Method(procedure)
	if !ok {
		t.Fatalf("procedure %s is not registered", procedure)
	}
	return md
}

// TestChainSymmetry is the invariant. One chain, configured once; a browser
// road and a bus road that share nothing at the network layer; the same
// stages in the same order on both.
func TestChainSymmetry(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	in := &commonv1.Problem{Title: "hello"}
	var viaHTTP commonv1.Problem
	if err := s.client.Invoke(ctx, s.method(t, testsvc.EchoProcedure), in, &viaHTTP,
		interchange.Metadata{"x-trace": "http"}); err != nil {
		t.Fatalf("http call: %v", err)
	}

	var viaBus commonv1.Problem
	busReq := &commonv1.Problem{Title: "hello"}
	cli, err := engine.NewClient(ctx, s.bus.Driver("tracer-client"),
		engine.WithStaticMetadata(interchange.Metadata{"x-trace": "bus"}),
		engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.Invoke(ctx, testsvc.EchoProcedure, busReq, &viaBus); err != nil {
		t.Fatalf("bus call: %v", err)
	}

	if viaHTTP.GetTitle() != "echo:hello" || viaBus.GetTitle() != "echo:hello" {
		t.Fatalf("handlers disagree: http=%q bus=%q", viaHTTP.GetTitle(), viaBus.GetTitle())
	}

	want := s.chain.Names()
	if got := s.tracer.trace("http"); !slices.Equal(got, want) {
		t.Fatalf("HTTP road ran %v, chain is %v", got, want)
	}
	if got := s.tracer.trace("bus"); !slices.Equal(got, want) {
		t.Fatalf("bus road ran %v, chain is %v", got, want)
	}
}

// TestErrorIsTheSameOnBothRoads: one reason string, whichever road it came
// back on. That is what lets a client branch once.
func TestErrorIsTheSameOnBothRoads(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	var out commonv1.Problem
	httpErr := s.client.Invoke(ctx, s.method(t, testsvc.FailProcedure), &commonv1.Problem{}, &out, nil)
	busErr := s.busCli.Invoke(ctx, testsvc.FailProcedure, &commonv1.Problem{}, &out)

	for road, err := range map[string]error{"http": httpErr, "bus": busErr} {
		if err == nil {
			t.Fatalf("%s: expected an error", road)
		}
		if got := interchange.CodeOf(err); got != interchange.CodeNotFound {
			t.Fatalf("%s: code is %v, want not_found", road, got)
		}
		if got := interchange.ReasonOf(err); got != "PROVIDER_NOT_FOUND" {
			t.Fatalf("%s: reason is %q, want PROVIDER_NOT_FOUND", road, got)
		}
	}
}

// TestTransportAnnotationIsLoadBearing: a method declared bus-only is not
// reachable over HTTP, and one declared on both is.
func TestTransportAnnotationIsLoadBearing(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	var out commonv1.Problem
	if err := s.busCli.Invoke(ctx, testsvc.BusOnlyProcedure, &commonv1.Problem{Title: "x"}, &out); err != nil {
		t.Fatalf("bus-only method must serve on the bus: %v", err)
	}

	err := s.client.Invoke(ctx, s.method(t, testsvc.BusOnlyProcedure), &commonv1.Problem{}, &out, nil)
	if err == nil {
		t.Fatal("a method not declared on TRANSPORT_RPC must not be mounted on the RPC binding")
	}
	if code := interchange.CodeOf(err); code != interchange.CodeUnimplemented && code != interchange.CodeUnknown {
		t.Fatalf("unexpected code %v", code)
	}
}

// TestEngineSubscriptionPlan: the engine subscribes a service wildcard, not a
// subject per method, when the methods agree on their group.
func TestEngineSubscriptionPlan(t *testing.T) {
	s := newStack(t)
	plan := s.busSrv.Plan()
	if len(plan) != 1 {
		t.Fatalf("expected one wildcard subscription, got %v", plan)
	}
	if want := "rpc." + testsvc.Service + ".*"; plan[0].Pattern != want {
		t.Fatalf("pattern is %q, want %q", plan[0].Pattern, want)
	}
}

// TestMethodsOn reports the fan-out the way `ix describe` reads it.
func TestMethodsOn(t *testing.T) {
	s := newStack(t)
	if got := len(s.reg.MethodsOn(transportv1.Transport_TRANSPORT_RPC)); got != 2 {
		t.Fatalf("%d methods on RPC, want 2", got)
	}
	if got := len(s.reg.MethodsOn(transportv1.Transport_TRANSPORT_BUS)); got != 3 {
		t.Fatalf("%d methods on the bus, want 3", got)
	}
}
