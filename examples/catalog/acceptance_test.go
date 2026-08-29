// Package catalog_test is the acceptance suite: every test is named after the
// BUILD-PLAN exit criterion it closes, and the criterion's exact wording is
// quoted above it. A checkbox nobody can run is a plan; a checkbox with a test
// under it is a claim.
//
// Four roads share one registry throughout: the Connect binding over httptest
// driven by the GENERATED Connect client, the REST binding transcoded off the
// same registry, the in-process memory bus, and a real NATS broker started
// inside the test binary. No docker, no fixture, no hand-written service code
// -- the ServiceDesc, the clients and the permission table are all generated
// from api/catalog/v1/catalog.proto.
package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/driver/memory"
	natsdriver "github.com/darmawan01/interchange/driver/nats"
	"github.com/darmawan01/interchange/engine"
	"github.com/darmawan01/interchange/errors"
	"github.com/darmawan01/interchange/examples/catalog"
	"github.com/darmawan01/interchange/examples/catalog/gen/go/authz"
	catalogv1 "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1"
	catalogv1bus "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1/catalogv1bus"
	catalogv1cli "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1/catalogv1cli"
	catalogv1connect "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1/catalogv1connect"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const tenant = "acme"

// ---------------------------------------------------------------------------
// Instrumentation. None of it is a mock: the tracer stages are ordinary named
// stages inserted by anchor, the observer is core's Observer seam, and the
// authorizer recorder wraps the real RBAC decider rather than replacing it.
// ---------------------------------------------------------------------------

// tracer records the stages a call traversed, keyed by the x-trace metadata
// the caller set, so two roads can be compared without either knowing it is
// being watched.
type tracer struct {
	mu  sync.Mutex
	log map[string][]string
}

func newTracer() *tracer { return &tracer{log: map[string][]string{}} }

func (t *tracer) probe(name string) interchange.Stage {
	return interchange.Named("probe:"+name, func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			t.mu.Lock()
			key := req.Metadata.Get("x-trace")
			t.log[key] = append(t.log[key], "probe:"+name)
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

// instrument interleaves a probe in front of every real stage, plus one
// innermost. The real stages stay in place and keep working -- replacing them,
// the way core's own symmetry test does, would mean the authorization this
// suite is about never fires.
func instrument(base *interchange.ChainSpec, t *tracer) (*interchange.ChainSpec, []string) {
	names := base.Names()
	chain := base
	for _, name := range names {
		chain = chain.Before(name, t.probe(name))
	}
	chain = chain.Append(t.probe("handler"))

	want := make([]string, 0, len(names)+1)
	for _, name := range names {
		want = append(want, "probe:"+name)
	}
	return chain, append(want, "probe:handler")
}

// observer captures the procedure at the two telemetry surfaces the build
// plan names separately: the span opened at the start of the call, and the
// metric label attached when it ends.
type observer struct {
	mu     sync.Mutex
	spans  []string
	labels []string
}

func (o *observer) ObserveCall(ctx context.Context, procedure string) (context.Context, func(error)) {
	o.mu.Lock()
	o.spans = append(o.spans, procedure)
	o.mu.Unlock()
	return ctx, func(error) {
		o.mu.Lock()
		o.labels = append(o.labels, procedure)
		o.mu.Unlock()
	}
}

func (o *observer) snapshot() (spans, labels []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.spans), slices.Clone(o.labels)
}

// recordingAuthorizer is the real RBAC decider with a tap on the procedure
// string it was asked about. Swapping the Authorizer is the extension point
// the /auth module exists to prove; this test does it in three lines and
// touches core, the contract and the bindings not at all.
type recordingAuthorizer struct {
	inner auth.Authorizer
	mu    sync.Mutex
	seen  []string
}

func (a *recordingAuthorizer) Authorize(ctx context.Context, procedure string, ann auth.Annotation, md map[string]string, msg proto.Message) error {
	a.mu.Lock()
	a.seen = append(a.seen, procedure)
	a.mu.Unlock()
	return a.inner.Authorize(ctx, procedure, ann, md, msg)
}

func (a *recordingAuthorizer) snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

// ---------------------------------------------------------------------------
// The stack. One chain, one registry, four roads.
// ---------------------------------------------------------------------------

type roads struct {
	svc   *catalog.Service
	impl  *catalog.Server
	seed  []*catalogv1.Provider
	chain *interchange.ChainSpec

	tracer    *tracer
	obs       *observer
	authz     *recordingAuthorizer
	baseNames []string
	wantPath  []string

	http    *httptest.Server
	mem     *memory.Bus
	natsSrv *server.Server
}

func newRoads(t *testing.T) *roads {
	t.Helper()

	tr := newTracer()
	obs := &observer{}
	roles, err := catalog.Roles()
	if err != nil {
		t.Fatalf("roles: %v", err)
	}
	az := &recordingAuthorizer{inner: roles}

	base, err := catalog.Chain(interchange.Config{Observer: obs}, catalog.WithAuthorizer(az))
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	chain, want := instrument(base, tr)
	if err := chain.Err(); err != nil {
		t.Fatalf("instrumented chain: %v", err)
	}

	impl := catalog.NewServer(catalog.WithClock(func() time.Time {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}))
	seed := impl.Seed(tenant, "stripe", "adyen")

	svc, err := catalog.Wire(impl, chain)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	httpSrv := httptest.NewServer(svc.Handler())
	t.Cleanup(httpSrv.Close)

	mem := memory.New()
	if _, err := svc.ServeBus(context.Background(), mem.Driver("server")); err != nil {
		t.Fatalf("memory bus: %v", err)
	}

	ns := natsServer(t)
	if _, err := svc.ServeBus(context.Background(), natsDriver(t, ns)); err != nil {
		t.Fatalf("nats bus: %v", err)
	}

	return &roads{
		svc: svc, impl: impl, seed: seed, chain: chain,
		tracer: tr, obs: obs, authz: az, baseNames: base.Names(), wantPath: want,
		http: httpSrv, mem: mem, natsSrv: ns,
	}
}

// natsServer starts a real NATS broker inside the test binary. The bus half of
// every claim below is checked against a broker rather than against an
// in-process stand-in, because a fake broker only proves the code agrees with
// itself.
func natsServer(t *testing.T) *server.Server {
	t.Helper()
	s := natsserver.RunServer(&server.Options{Port: -1, NoLog: true, NoSigs: true})
	t.Cleanup(s.Shutdown)
	return s
}

// natsDriver returns a driver on its own connection. Each engine client needs
// its own reply inbox, and an inbox belongs to a driver.
func natsDriver(t *testing.T, s *server.Server) *natsdriver.Driver {
	t.Helper()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	d, err := natsdriver.New(nc)
	if err != nil {
		t.Fatalf("nats driver: %v", err)
	}
	return d
}

// The four roads, behind one name each. Every one of them ends up in
// Registry.Dispatch; none of them holds a chain.
const (
	roadHTTP   = "http"
	roadREST   = "rest"
	roadMemory = "memory"
	roadNATS   = "nats"
)

// allRoads is every road the contract declares for ListProviders. The chain
// symmetry claim is quantified over exactly this list.
func allRoads() []string { return []string{roadHTTP, roadREST, roadMemory, roadNATS} }

func busRoads() []string { return []string{roadMemory, roadNATS} }

func (r *roads) driver(t *testing.T, road string) interchange.Driver {
	t.Helper()
	switch road {
	case roadMemory:
		return r.mem.Driver("client-" + t.Name() + "-" + randomish())
	case roadNATS:
		return natsDriver(t, r.natsSrv)
	}
	t.Fatalf("no such road %q", road)
	return nil
}

var counter struct {
	sync.Mutex
	n int
}

func randomish() string {
	counter.Lock()
	defer counter.Unlock()
	counter.n++
	return time.Now().Format("150405.000000000") + "-" + string(rune('a'+counter.n%26))
}

// busClient returns the GENERATED bus client, carrying md on every call.
func (r *roads) busClient(t *testing.T, road string, md interchange.Metadata) *catalogv1bus.CatalogServiceBusClient {
	t.Helper()
	cli, err := engine.NewClient(context.Background(), r.driver(t, road),
		engine.WithStaticMetadata(md), engine.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatalf("%s client: %v", road, err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return catalogv1bus.NewCatalogServiceBusClient(cli)
}

// connectClient returns the GENERATED Connect client. The front end in web/
// builds the same client from the same contract in TypeScript.
func (r *roads) connectClient() catalogv1connect.CatalogServiceClient {
	return catalogv1connect.NewCatalogServiceClient(http.DefaultClient, r.http.URL)
}

func header[T any](msg *T, md interchange.Metadata) *connect.Request[T] {
	req := connect.NewRequest(msg)
	for k, v := range md {
		req.Header().Set(k, v)
	}
	return req
}

// outcome is a call's result reduced to what a client actually branches on.
// It is deliberately the same shape on every road: that is the claim.
type outcome struct {
	code   interchange.Code
	reason string
}

func result(err error) outcome {
	if err == nil {
		return outcome{code: interchange.CodeOK}
	}
	var ce *connect.Error
	if stderrors.As(err, &ce) {
		return outcome{code: interchange.Code(ce.Code()), reason: ce.Meta().Get(rpc.ErrorReasonHeader)}
	}
	return outcome{code: interchange.CodeOf(err), reason: interchange.ReasonOf(err)}
}

func bearer(token string) interchange.Metadata {
	return interchange.NewMetadata(map[string]string{"authorization": "Bearer " + token})
}

func apiKey(key string) interchange.Metadata {
	return interchange.NewMetadata(map[string]string{"x-api-key": key})
}

func with(md interchange.Metadata, k, v string) interchange.Metadata {
	out := md.Clone()
	out.Set(k, v)
	return out
}

// listOnRoad makes the same ListProviders call on any of the four roads.
func (r *roads) listOnRoad(t *testing.T, road string, md interchange.Metadata) (*catalogv1.ListProvidersResponse, error) {
	t.Helper()
	req := &catalogv1.ListProvidersRequest{TenantId: tenant}
	switch road {
	case roadHTTP:
		resp, err := r.connectClient().ListProviders(context.Background(), header(req, md))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	case roadREST:
		out := &catalogv1.ListProvidersResponse{}
		if _, err := r.rest(t, http.MethodGet, "/v1/catalog/providers?tenant_id="+tenant, md, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	return r.busClient(t, road, md).ListProviders(context.Background(), req)
}

// rest calls the partner surface with a real HTTP request and decodes the
// body. It returns the raw body too, because on this road the spelling of the
// field names is part of the contract.
func (r *roads) rest(t *testing.T, method, path string, md interchange.Metadata, out proto.Message, body ...string) ([]byte, error) {
	t.Helper()
	var in io.Reader
	if len(body) > 0 {
		in = strings.NewReader(body[0])
	}
	req, err := http.NewRequestWithContext(context.Background(), method, r.http.URL+path, in)
	if err != nil {
		t.Fatal(err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range md {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		// application/problem+json, with the machine-readable reason in the
		// same header the Connect binding sets.
		return raw, &restError{
			err: &interchange.Error{
				Code:    statusToCode(resp.StatusCode),
				Message: string(raw),
				Reason:  resp.Header.Get(rpc.ErrorReasonHeader),
			},
			status:      resp.StatusCode,
			contentType: resp.Header.Get("Content-Type"),
		}
	}
	if out != nil {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, raw)
		}
	}
	return raw, nil
}

// restError carries the two things only this surface has: the HTTP status and
// the media type the failure was rendered as.
type restError struct {
	err         *interchange.Error
	status      int
	contentType string
}

func (e *restError) Error() string { return e.err.Error() }

// Unwrap is what lets interchange.CodeOf and ReasonOf read this the same way
// they read a bus error, which is what makes `result` road-agnostic.
func (e *restError) Unwrap() error { return e.err }

// statusToCode inverts errors.HTTPStatus over the codes this suite raises. It
// is written as a search rather than a second table so the two can never
// disagree: if the REST surface projects a status the taxonomy does not, this
// says so instead of quietly returning "unknown".
func statusToCode(status int) interchange.Code {
	for c := interchange.CodeCanceled; c <= interchange.CodeUnauthenticated; c++ {
		if errors.HTTPStatus(c) == status {
			return c
		}
	}
	return interchange.CodeUnknown
}

// ---------------------------------------------------------------------------
// Phase 2 · "A chain configured once demonstrably runs in the same order on
// every registered binding."
// ---------------------------------------------------------------------------

// TestChainConfiguredOnceRunsInTheSameOrderOnEveryRegisteredBinding is the
// invariant the whole project rests on. The chain is built once in wire.go and
// handed to Register once; the Connect binding, the in-process bus and a real
// NATS broker share nothing at the network layer; and the traversal recorded
// on each is the same list in the same order.
func TestChainConfiguredOnceRunsInTheSameOrderOnEveryRegisteredBinding(t *testing.T) {
	r := newRoads(t)

	// The chain wire.go composes, spelled out. Asserting it here is what stops
	// this test from passing vacuously the day someone empties the chain: an
	// empty chain is trivially identical on every road.
	wantChain := []string{"telemetry", "errors", "recover", "deadline", "authn", "validate", "authz"}
	if got := r.baseNames; !slices.Equal(got, wantChain) {
		t.Fatalf("wire.go composes %v, this suite was written against %v", got, wantChain)
	}

	for _, road := range allRoads() {
		if _, err := r.listOnRoad(t, road, with(bearer(catalog.TokenReader), "x-trace", road)); err != nil {
			t.Fatalf("%s: %v", road, err)
		}
	}

	for _, road := range allRoads() {
		if got := r.tracer.trace(road); !slices.Equal(got, r.wantPath) {
			t.Fatalf("%s road ran\n  %v\nchain is\n  %v", road, got, r.wantPath)
		}
	}

	// And the registry agrees: one chain, whatever road asks it.
	got, ok := r.svc.Registry.ChainNames(catalogv1bus.CatalogServiceListProvidersProcedure)
	if !ok {
		t.Fatal("ListProviders is not registered")
	}
	if !slices.Equal(got, r.chain.Names()) {
		t.Fatalf("registry reports %v, chain is %v", got, r.chain.Names())
	}
}

// ---------------------------------------------------------------------------
// Phase 4 · "The same procedure string appears in the authz check, the metrics
// labels and the trace span on both roads."
// ---------------------------------------------------------------------------

// TestSameProcedureStringInAuthzCheckMetricsLabelsAndTraceSpanOnBothRoads
// captures the string at each of the three points, on HTTP and on NATS, and
// asserts all of them are the one the contract minted. The procedure string is
// the join key between an authorization decision, a dashboard and a trace; if
// it differed by road, none of those three could be correlated across roads.
func TestSameProcedureStringInAuthzCheckMetricsLabelsAndTraceSpanOnBothRoads(t *testing.T) {
	r := newRoads(t)
	const want = "/catalog.v1.CatalogService/ListProviders"

	// The generated Connect client and the generated bus client agree on the
	// string before a single call is made -- they were generated from one
	// contract.
	if catalogv1connect.CatalogServiceListProvidersProcedure != want ||
		catalogv1bus.CatalogServiceListProvidersProcedure != want {
		t.Fatalf("generated procedure constants disagree: connect=%q bus=%q",
			catalogv1connect.CatalogServiceListProvidersProcedure,
			catalogv1bus.CatalogServiceListProvidersProcedure)
	}

	for _, road := range []string{roadHTTP, roadNATS} {
		if _, err := r.listOnRoad(t, road, bearer(catalog.TokenReader)); err != nil {
			t.Fatalf("%s: %v", road, err)
		}
	}

	spans, labels := r.obs.snapshot()
	checked := r.authz.snapshot()
	twice := []string{want, want}

	if !slices.Equal(spans, twice) {
		t.Fatalf("trace spans are %v, want %v", spans, twice)
	}
	if !slices.Equal(labels, twice) {
		t.Fatalf("metrics labels are %v, want %v", labels, twice)
	}
	if !slices.Equal(checked, twice) {
		t.Fatalf("authz checks are %v, want %v", checked, twice)
	}
}

// ---------------------------------------------------------------------------
// Phase 4 · "Authorization demonstrably fires on the bus call."
// ---------------------------------------------------------------------------

// TestAuthorizationFiresOnTheBusCall is the failure class this project exists
// to close, stated as a test: a check enforced in the HTTP handler and
// silently absent on the bus. Every case below is asserted with the same code
// AND the same reason on both roads -- "it failed on both" is a much weaker
// claim than "it failed the same way on both".
func TestAuthorizationFiresOnTheBusCall(t *testing.T) {
	r := newRoads(t)
	ctx := context.Background()

	list := func(t *testing.T, road string, md interchange.Metadata) outcome {
		t.Helper()
		_, err := r.listOnRoad(t, road, md)
		return result(err)
	}

	cases := []struct {
		name string
		md   interchange.Metadata
		want outcome
	}{
		{
			name: "a caller holding providers.read succeeds",
			md:   bearer(catalog.TokenReader),
			want: outcome{code: interchange.CodeOK},
		},
		{
			// A verified session for another tenant. The tenant comes off the
			// message by reflection, so this fires identically wherever the
			// message came from.
			name: "a caller in another tenant is denied",
			md:   bearer(catalog.TokenOtherTenant),
			want: outcome{code: interchange.CodePermissionDenied, reason: auth.ReasonTenantDenied},
		},
		{
			name: "an anonymous caller is denied",
			md:   nil,
			want: outcome{code: interchange.CodeUnauthenticated, reason: auth.ReasonUnauthenticated},
		},
		{
			name: "an unknown credential is denied",
			md:   bearer("not-a-token"),
			want: outcome{code: interchange.CodeUnauthenticated, reason: auth.ReasonUnauthenticated},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, road := range allRoads() {
				if got := list(t, road, tc.md); got != tc.want {
					t.Fatalf("%s: got %+v, want %+v", road, got, tc.want)
				}
			}
		})
	}

	// The permission itself, on the road where only the bus can reach the
	// method at all: a workload key that may read but not edit is denied
	// SyncProvider over NATS, and the key that may edit succeeds.
	t.Run("the permission decides on a bus-only method", func(t *testing.T) {
		req := &catalogv1.SyncProviderRequest{TenantId: tenant, ProviderId: r.seed[0].GetProviderId()}

		_, err := r.busClient(t, roadNATS, apiKey(catalog.KeyReadOnlyWorload)).SyncProvider(ctx, req)
		if got := (result(err)); got.code != interchange.CodePermissionDenied || got.reason != auth.ReasonPermissionDenied {
			t.Fatalf("a workload without providers.edit must be denied, got %+v", got)
		}

		resp, err := r.busClient(t, roadNATS, apiKey(catalog.KeySyncWorkload)).SyncProvider(ctx, req)
		if err != nil {
			t.Fatalf("a workload holding providers.edit must succeed: %v", err)
		}
		if want := "job_" + r.seed[0].GetProviderId(); resp.GetJobId() != want {
			t.Fatalf("job id is %q, want %q", resp.GetJobId(), want)
		}
	})

	// An error raised by the handler, not by an interceptor, still carries one
	// reason on every road.
	t.Run("a handler error carries one reason on every road", func(t *testing.T) {
		want := outcome{code: interchange.CodeNotFound, reason: catalog.ReasonProviderNotFound}

		_, httpErr := r.connectClient().GetProvider(ctx,
			header(&catalogv1.GetProviderRequest{TenantId: tenant, ProviderId: "prov_nope"}, bearer(catalog.TokenReader)))
		if got := result(httpErr); got != want {
			t.Fatalf("http: got %+v, want %+v", got, want)
		}
		for _, road := range busRoads() {
			_, err := r.busClient(t, road, bearer(catalog.TokenReader)).
				GetProvider(ctx, &catalogv1.GetProviderRequest{TenantId: tenant, ProviderId: "prov_nope"})
			if got := result(err); got != want {
				t.Fatalf("%s: got %+v, want %+v", road, got, want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Phase 2 · "Core builds and passes its tests with the /auth module absent. A
// service with an empty chain works."
// ---------------------------------------------------------------------------

// TestAServiceWithAnEmptyChainWorks serves the same generated ServiceDesc and
// the same handler with interchange.Chain() -- no auth, no errors, no
// validate, not even core's three stock stages -- on HTTP and on NATS.
//
// It needs its own Registry rather than the one above: Registry.Register
// rejects a second registration of the same service, deliberately, because a
// shadowed handler is a production-only bug. "The same registry with an empty
// chain" is therefore not expressible, and should not be: a chain is bound at
// registration, which is what makes it impossible for a binding to vary it.
func TestAServiceWithAnEmptyChainWorks(t *testing.T) {
	impl := catalog.NewServer()
	impl.Seed(tenant, "stripe")

	svc, err := catalog.Wire(impl, interchange.Chain())
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if n := svc.Chain.Len(); n != 0 {
		t.Fatalf("chain has %d stages, want an empty chain", n)
	}

	httpSrv := httptest.NewServer(svc.Handler())
	t.Cleanup(httpSrv.Close)

	ns := natsServer(t)
	if _, err := svc.ServeBus(context.Background(), natsDriver(t, ns)); err != nil {
		t.Fatalf("nats bus: %v", err)
	}

	req := &catalogv1.ListProvidersRequest{TenantId: tenant}
	// No credential anywhere: with the module absent there is nothing to
	// present one to.
	viaHTTP, err := catalogv1connect.NewCatalogServiceClient(http.DefaultClient, httpSrv.URL).
		ListProviders(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("http: %v", err)
	}

	cli, err := engine.NewClient(context.Background(), natsDriver(t, ns), engine.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatalf("nats client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	viaBus, err := catalogv1bus.NewCatalogServiceBusClient(cli).ListProviders(context.Background(), req)
	if err != nil {
		t.Fatalf("nats: %v", err)
	}

	if !proto.Equal(viaHTTP.Msg, viaBus) {
		t.Fatalf("an empty chain gave two answers:\n  http=%v\n  bus =%v", viaHTTP.Msg, viaBus)
	}
	if len(viaBus.GetProviders()) != 1 {
		t.Fatalf("got %d providers, want 1", len(viaBus.GetProviders()))
	}
}

// ---------------------------------------------------------------------------
// Phase 4 · "One low-risk service-to-service call, already on HTTP, is moved
// to the bus." + "The interceptor chain came along unchanged."
// ---------------------------------------------------------------------------

// TestTheInterceptorChainCameAlongUnchanged is the migration, compressed into
// one test: ListProviders is called over HTTP and then over NATS, and both the
// response and the chain traversal are compared. Moving a call to the bus is a
// change of constructor at the call site and nothing else.
func TestTheInterceptorChainCameAlongUnchanged(t *testing.T) {
	r := newRoads(t)

	viaHTTP, err := r.listOnRoad(t, roadHTTP, with(bearer(catalog.TokenReader), "x-trace", "before"))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	viaBus, err := r.listOnRoad(t, roadNATS, with(bearer(catalog.TokenReader), "x-trace", "after"))
	if err != nil {
		t.Fatalf("nats: %v", err)
	}

	if !proto.Equal(viaHTTP, viaBus) {
		t.Fatalf("the call answered differently after the move:\n  http=%v\n  bus =%v", viaHTTP, viaBus)
	}
	before, after := r.tracer.trace("before"), r.tracer.trace("after")
	if !slices.Equal(before, after) {
		t.Fatalf("the chain changed in the move:\n  http=%v\n  bus =%v", before, after)
	}
	if !slices.Equal(after, r.wantPath) {
		t.Fatalf("the bus ran %v, chain is %v", after, r.wantPath)
	}
}

// ---------------------------------------------------------------------------
// Cross-phase · the (transports) and (internal) annotations are load-bearing.
// ---------------------------------------------------------------------------

// TestTheTransportsAnnotationIsLoadBearing: an annotation nothing enforces is
// documentation. SyncProvider names only TRANSPORT_BUS, so the RPC binding
// must refuse it; Reconcile is additionally (internal), so it is off every
// public binding while staying reachable by a platform workload on the bus --
// which is what "internal" means and not one word more.
func TestTheTransportsAnnotationIsLoadBearing(t *testing.T) {
	r := newRoads(t)
	ctx := context.Background()

	t.Run("bus-only is unreachable over HTTP", func(t *testing.T) {
		_, err := r.connectClient().SyncProvider(ctx,
			header(&catalogv1.SyncProviderRequest{TenantId: tenant, ProviderId: r.seed[0].GetProviderId()},
				apiKey(catalog.KeySyncWorkload)))
		if err == nil {
			t.Fatal("a method not declared on TRANSPORT_RPC must not be mounted on the RPC binding")
		}
	})

	t.Run("internal is unreachable over HTTP", func(t *testing.T) {
		_, err := r.connectClient().Reconcile(ctx,
			header(&catalogv1.ReconcileRequest{}, apiKey(catalog.KeyPlatform)))
		if err == nil {
			t.Fatal("an (internal) method must not be mounted on a public binding")
		}
	})

	t.Run("internal still serves a platform workload on the bus", func(t *testing.T) {
		resp, err := r.busClient(t, roadNATS, apiKey(catalog.KeyPlatform)).
			Reconcile(ctx, &catalogv1.ReconcileRequest{})
		if err != nil {
			t.Fatalf("Reconcile on the bus: %v", err)
		}
		if resp.GetReconciled() != int32(len(r.seed)) {
			t.Fatalf("reconciled %d, want %d", resp.GetReconciled(), len(r.seed))
		}
	})

	t.Run("neither is reachable on the REST surface", func(t *testing.T) {
		// The REST binding on its own listener, so nothing else can answer.
		// The transcoder also speaks Connect on the procedure path, and its
		// method set is the REST-filtered one -- which is what stops a
		// partner poking the partner listener from reaching a bus-only
		// method by the back door.
		restOnly := httptest.NewServer(r.svc.REST.Handler())
		t.Cleanup(restOnly.Close)

		post := func(procedure, body string, md interchange.Metadata) int {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, restOnly.URL+procedure, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Connect-Protocol-Version", "1")
			for k, v := range md {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			return resp.StatusCode
		}

		for _, procedure := range []string{
			catalogv1bus.CatalogServiceSyncProviderProcedure,
			catalogv1bus.CatalogServiceReconcileProcedure,
		} {
			if got := post(procedure, "{}", apiKey(catalog.KeyPlatform)); got != http.StatusNotFound {
				t.Fatalf("%s answered %d on the REST listener; there must be no route", procedure, got)
			}
		}
		// A method that did declare the road is served, so the 404s above are
		// the filter and not a broken listener.
		if got := post(catalogv1bus.CatalogServiceListProvidersProcedure, `{"tenant_id":"`+tenant+`"}`, bearer(catalog.TokenReader)); got != http.StatusOK {
			t.Fatalf("ListProviders answered %d on the REST listener", got)
		}
	})

	t.Run("the fan-out the registry reports is the fan-out the contract declares", func(t *testing.T) {
		onRPC := procedures(r.svc.Registry.MethodsOn(transportv1.Transport_TRANSPORT_RPC))
		wantRPC := []string{
			catalogv1bus.CatalogServiceCreateProviderProcedure,
			catalogv1bus.CatalogServiceGetProviderProcedure,
			catalogv1bus.CatalogServiceListProvidersProcedure,
		}
		if !slices.Equal(onRPC, wantRPC) {
			t.Fatalf("on RPC: %v, want %v", onRPC, wantRPC)
		}
		onREST := procedures(r.svc.Registry.MethodsOn(transportv1.Transport_TRANSPORT_REST))
		if !slices.Equal(onREST, wantRPC) {
			t.Fatalf("on REST: %v, want %v", onREST, wantRPC)
		}
		if got := len(r.svc.Registry.MethodsOn(transportv1.Transport_TRANSPORT_BUS)); got != 5 {
			t.Fatalf("%d methods on the bus, want all 5", got)
		}
		if got := len(r.svc.Registry.MethodsOn(transportv1.Transport_TRANSPORT_MQTT)); got != 0 {
			t.Fatalf("%d methods on MQTT, want none: the contract names no MQTT road", got)
		}
	})
}

func procedures(mds []*interchange.MethodDesc) []string {
	out := make([]string, 0, len(mds))
	for _, md := range mds {
		out = append(out, md.Procedure)
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------------------
// Phase 2 (module) · "CI can be configured to fail on an RPC with no (auth)
// annotation." -- the build-time gate and the runtime check agree.
// ---------------------------------------------------------------------------

// TestGeneratedPermissionTableMatchesTheRuntimeInterceptor walks the table
// protoc-gen-authz emitted at build time against the annotation the authz
// interceptor decodes at runtime, procedure for procedure and in both
// directions.
//
// "Enforce twice" is only worth anything if the two enforcement points agree:
// a build-time gate and a runtime check that disagree are worse than either
// alone, because a reviewer reads the table and believes it.
func TestGeneratedPermissionTableMatchesTheRuntimeInterceptor(t *testing.T) {
	r := newRoads(t)

	table := authz.Permissions()
	registered := r.svc.Registry.Procedures()

	if len(table) != len(registered) {
		t.Fatalf("the table has %d rows and %d procedures are registered: %v vs %v",
			len(table), len(registered), keys(table), registered)
	}

	for _, procedure := range registered {
		rule, ok := table[procedure]
		if !ok {
			t.Fatalf("%s is served but has no row in the generated table; a gateway reading the table would have to deny it", procedure)
		}
		md, ok := r.svc.Registry.Method(procedure)
		if !ok {
			t.Fatalf("%s vanished from the registry", procedure)
		}
		// The same decode the interceptor does, off the same descriptor.
		ann := auth.AnnotationOf(md.Desc)
		if !ann.Present {
			t.Fatalf("%s: the table has a row but the runtime sees no annotation", procedure)
		}
		if got := ann.Permission.Atom(); got != rule.Permission {
			t.Fatalf("%s: runtime atom %q, table %q", procedure, got, rule.Permission)
		}
		if ann.Public != rule.Public || ann.Platform != rule.Platform {
			t.Fatalf("%s: runtime public=%v platform=%v, table public=%v platform=%v",
				procedure, ann.Public, ann.Platform, rule.Public, rule.Platform)
		}
		var kinds []string
		for _, k := range ann.AuthTypes {
			kinds = append(kinds, k.String())
		}
		if !slices.Equal(kinds, rule.AuthTypes) {
			t.Fatalf("%s: runtime auth types %v, table %v", procedure, kinds, rule.AuthTypes)
		}
	}

	// Every atom the table declares is one the role table can actually grant.
	// A permission nobody can hold denies everyone on the day it ships.
	roles, err := catalog.Roles()
	if err != nil {
		t.Fatal(err)
	}
	for _, atom := range authz.Atoms() {
		if _, err := auth.ParseAtom(atom); err != nil {
			t.Fatalf("the table declares %q, which is not a permission atom: %v", atom, err)
		}
		if !slices.ContainsFunc(roles.Roles(), func(role string) bool {
			return slices.Contains(roles.Grants(role), atom) || slices.Contains(roles.Grants(role), "providers.*")
		}) {
			t.Fatalf("no role grants %q, so every RPC declaring it denies everyone", atom)
		}
	}
}

func keys(m map[string]authz.Rule) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestTheTableIsEnforcedAtRuntime spot-checks one verb per row: a principal
// holding the declared atom is allowed and one without it is denied, with the
// denial carrying the reason the table's reader would expect.
func TestTheTableIsEnforcedAtRuntime(t *testing.T) {
	r := newRoads(t)
	ctx := context.Background()

	t.Run("providers.read", func(t *testing.T) {
		if _, err := r.listOnRoad(t, roadMemory, apiKey(catalog.KeyBrowserAPI)); err != nil {
			t.Fatalf("a reader must be allowed: %v", err)
		}
	})

	t.Run("providers.create", func(t *testing.T) {
		create := func(md interchange.Metadata, name string) outcome {
			_, err := r.connectClient().CreateProvider(ctx,
				header(&catalogv1.CreateProviderRequest{TenantId: tenant, DisplayName: name}, md))
			return result(err)
		}
		if got := create(bearer(catalog.TokenReader), "checkout"); got.reason != auth.ReasonPermissionDenied {
			t.Fatalf("a reader must not create, got %+v", got)
		}
		if got := create(bearer(catalog.TokenWriter), "checkout"); got.code != interchange.CodeOK {
			t.Fatalf("a writer must create, got %+v", got)
		}
		if got := create(bearer(catalog.TokenWriter), "checkout"); got.reason != catalog.ReasonProviderAlreadyExists {
			t.Fatalf("a duplicate must collide, got %+v", got)
		}
	})

	t.Run("providers.edit", func(t *testing.T) {
		req := &catalogv1.SyncProviderRequest{TenantId: tenant, ProviderId: r.seed[1].GetProviderId()}
		if _, err := r.busClient(t, roadMemory, apiKey(catalog.KeySyncWorkload)).SyncProvider(ctx, req); err != nil {
			t.Fatalf("a syncer must be allowed: %v", err)
		}
		_, err := r.busClient(t, roadMemory, apiKey(catalog.KeyReadOnlyWorload)).SyncProvider(ctx, req)
		if got := result(err); got.reason != auth.ReasonPermissionDenied {
			t.Fatalf("a read-only workload must be denied, got %+v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Phase 1 · "`ix init` produces a working project with a generated typed
// client." -- the CLI half, and the front-end half.
// ---------------------------------------------------------------------------

// TestTheGeneratedCLICoversTheContract: protoc-gen-cli ran with
// require_annotation=true, so a new RPC that nobody annotated fails the build
// rather than leaving an invisible hole in the tool. Coverage is the report
// that makes the remaining hole -- Reconcile, explicitly skipped -- visible.
func TestTheGeneratedCLICoversTheContract(t *testing.T) {
	cov := catalogv1cli.CatalogServiceCoverage()
	if len(cov.Missing) != 0 {
		t.Fatalf("the CLI is missing %v", cov.Missing)
	}
	if len(cov.Covered) != 4 {
		t.Fatalf("covered %v, want the four RPCs a human drives", cov.Covered)
	}
	if !slices.Equal(cov.Skipped, []string{catalogv1bus.CatalogServiceReconcileProcedure}) {
		t.Fatalf("skipped %v, want only Reconcile", cov.Skipped)
	}

	// And it is a real caller, not a report: the same tree catalogctl mounts,
	// driven against the running service over the Connect binding. *rpc.Client
	// satisfies clisupport.Invoker as it is, which is why the same commands
	// run over a bus with no adapter in between.
	t.Run("the generated tree drives the running service", func(t *testing.T) {
		r := newRoads(t)
		root := &cobra.Command{Use: "catalogctl", SilenceUsage: true, SilenceErrors: true}
		catalogv1cli.RegisterCatalogServiceCommands(root,
			rpc.NewClient(http.DefaultClient, r.http.URL, rpc.WithStaticMetadata(bearer(catalog.TokenReader))))

		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"catalog", "providers", "--tenant-id", tenant})
		if err := root.Execute(); err != nil {
			t.Fatalf("catalogctl catalog providers: %v\n%s", err, out.String())
		}
		for _, want := range []string{"stripe", "adyen", r.seed[0].GetProviderId()} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("output does not mention %q:\n%s", want, out.String())
			}
		}
	})
}

// TestAFrontEndImportsItsTypesFromGeneratedOutput closes the phase-1 criterion
// "A front end imports its types from generated output, not a hand-written
// file". The assertion is `tsc --noEmit` over web/: the app imports
// CatalogService from gen/ts and nothing else, so a renamed field is a failed
// build rather than a broken page.
//
// It skips when the workspace has not been installed, because a Go test suite
// that needs `npm install` to pass is a Go test suite that fails in CI for
// reasons nobody wants to debug. `npm --prefix examples/catalog ci` first.
func TestAFrontEndImportsItsTypesFromGeneratedOutput(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	// The workspace hoists node_modules here rather than into web/, because
	// module resolution walks up from the importing file and the generated
	// TypeScript lives in gen/ts, not in web/.
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "typescript")); err != nil {
		t.Skip("node_modules is not installed; run `npm --prefix examples/catalog ci`")
	}
	cmd := exec.Command("npm", "run", "--silent", "typecheck")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc --noEmit failed:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Cross-phase · the taxonomy the /errors module enforces is the one the
// service declares.
// ---------------------------------------------------------------------------

// TestEveryReasonThisServiceRaisesIsDeclared: the errors stage panics under
// `go test` on a reason outside the configured set, so the suite above already
// proves membership for every path it exercises. This states the set itself,
// so a reason added to a handler without being added to the contract fails
// here with a message that says what to do.
func TestEveryReasonThisServiceRaisesIsDeclared(t *testing.T) {
	set := errors.Union(errors.Stock(), catalog.Reasons())
	for _, reason := range []string{
		catalog.ReasonProviderNotFound,
		catalog.ReasonProviderAlreadyExists,
		catalog.ReasonDisplayNameRequired,
		auth.ReasonAnnotationMissing,
		auth.ReasonNotWired,
		auth.ReasonPermissionDenied,
		auth.ReasonTenantMissing,
		auth.ReasonTenantDenied,
		auth.ReasonUnauthenticated,
		auth.ReasonAuthTypeRejected,
	} {
		if !set.Has(reason) {
			t.Errorf("%s is raised on some road but is not in the declared set; add it to catalog.Reasons()", reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 3 · "An existing REST consumer is served by the transcoder." +
// "Old hand-written handlers are deleted as each path is covered."
// ---------------------------------------------------------------------------

// TestAnExistingRESTConsumerIsServedByTheTranscoder calls the URIs a partner
// already has -- the ones written in the google.api.http annotations -- with
// plain net/http and no generated client at all, which is what a partner has.
//
// Nothing in this repository implements those paths. There is no REST handler
// to delete because there was never one to write: the URI exists because the
// contract carries an annotation, and the call lands in Registry.Dispatch like
// every other road.
func TestAnExistingRESTConsumerIsServedByTheTranscoder(t *testing.T) {
	r := newRoads(t)
	md := bearer(catalog.TokenWriter)

	t.Run("GET /v1/catalog/providers", func(t *testing.T) {
		viaREST := &catalogv1.ListProvidersResponse{}
		if _, err := r.rest(t, http.MethodGet, "/v1/catalog/providers?tenant_id="+tenant, md, viaREST); err != nil {
			t.Fatalf("REST: %v", err)
		}
		viaRPC, err := r.listOnRoad(t, roadHTTP, md)
		if err != nil {
			t.Fatalf("RPC: %v", err)
		}
		// The same message, not merely the same data: one contract, one
		// handler, two projections of the one response.
		if !proto.Equal(viaREST, viaRPC) {
			t.Fatalf("the two HTTP roads answered differently:\n  rest=%v\n  rpc =%v", viaREST, viaRPC)
		}
		if len(viaREST.GetProviders()) != len(r.seed) {
			t.Fatalf("got %d providers, want %d", len(viaREST.GetProviders()), len(r.seed))
		}
	})

	t.Run("GET /v1/catalog/providers/{provider_id}", func(t *testing.T) {
		out := &catalogv1.GetProviderResponse{}
		path := "/v1/catalog/providers/" + r.seed[0].GetProviderId() + "?tenant_id=" + tenant
		if _, err := r.rest(t, http.MethodGet, path, md, out); err != nil {
			t.Fatalf("REST: %v", err)
		}
		// The path parameter bound onto the request message's provider_id
		// field; nothing here parsed a URI.
		if !proto.Equal(out.GetProvider(), r.seed[0]) {
			t.Fatalf("got %v, want %v", out.GetProvider(), r.seed[0])
		}
	})

	t.Run("POST /v1/catalog/providers", func(t *testing.T) {
		out := &catalogv1.CreateProviderResponse{}
		body := `{"tenant_id":"` + tenant + `","display_name":"worldpay"}`
		if _, err := r.rest(t, http.MethodPost, "/v1/catalog/providers", md, out, body); err != nil {
			t.Fatalf("REST: %v", err)
		}
		if out.GetProvider().GetDisplayName() != "worldpay" {
			t.Fatalf("created %v", out.GetProvider())
		}
		// And it is really in the store, reachable from another road.
		viaBus, err := r.busClient(t, roadNATS, md).
			GetProvider(context.Background(), &catalogv1.GetProviderRequest{
				TenantId: tenant, ProviderId: out.GetProvider().GetProviderId(),
			})
		if err != nil {
			t.Fatalf("the bus cannot see what REST created: %v", err)
		}
		if !proto.Equal(viaBus.GetProvider(), out.GetProvider()) {
			t.Fatalf("bus=%v rest=%v", viaBus.GetProvider(), out.GetProvider())
		}
	})

	t.Run("a failure is problem+json with the same reason as every other road", func(t *testing.T) {
		path := "/v1/catalog/providers/prov_nope?tenant_id=" + tenant
		_, err := r.rest(t, http.MethodGet, path, md, nil)
		var re *restError
		if !stderrors.As(err, &re) {
			t.Fatalf("expected a REST failure, got %v", err)
		}
		if re.status != http.StatusNotFound {
			t.Fatalf("status %d, want 404", re.status)
		}
		if got := re.contentType; !strings.HasPrefix(got, "application/problem+json") {
			t.Fatalf("content type %q, want application/problem+json", got)
		}
		// The reason -- the thing a client branches on -- is byte-identical
		// to the one the bus and the Connect road return for the same call.
		if got := result(err); got.reason != catalog.ReasonProviderNotFound {
			t.Fatalf("reason %q, want %q", got.reason, catalog.ReasonProviderNotFound)
		}
		if !strings.Contains(string(re.err.Message), `"reason":"`+catalog.ReasonProviderNotFound+`"`) {
			t.Fatalf("the problem document does not carry the reason:\n%s", re.err.Message)
		}
	})
}

// ---------------------------------------------------------------------------
// Phase 3 · "Per-surface JSON casing, written down: camelCase on RPC,
// snake_case on REST."
// ---------------------------------------------------------------------------

// TestPerSurfaceJSONCasingCamelCaseOnRPCSnakeCaseOnREST reads the raw bytes
// off both HTTP roads. The two surfaces have different audiences -- an SDK
// generated from the contract, and a partner reading a URI -- and the decision
// is written down in binding/rest/README.md. A surface whose casing nobody
// chose is the failure; a surface whose casing nobody checked is how you get
// there.
func TestPerSurfaceJSONCasingCamelCaseOnRPCSnakeCaseOnREST(t *testing.T) {
	r := newRoads(t)
	md := bearer(catalog.TokenReader)

	restBody, err := r.rest(t, http.MethodGet, "/v1/catalog/providers?tenant_id="+tenant, md, nil)
	if err != nil {
		t.Fatalf("REST: %v", err)
	}
	if !strings.Contains(string(restBody), `"display_name"`) {
		t.Fatalf("REST is not snake_case:\n%s", restBody)
	}
	if strings.Contains(string(restBody), `"displayName"`) {
		t.Fatalf("REST leaked the RPC surface's casing:\n%s", restBody)
	}

	// The Connect road, spoken as a raw JSON POST the way a browser client
	// does -- no generated client in the way to hide the spelling.
	rpcBody := r.connectJSON(t, catalogv1bus.CatalogServiceListProvidersProcedure,
		`{"tenantId":"`+tenant+`"}`, md)
	if !strings.Contains(rpcBody, `"displayName"`) {
		t.Fatalf("RPC is not camelCase:\n%s", rpcBody)
	}
	if strings.Contains(rpcBody, `"display_name"`) {
		t.Fatalf("RPC leaked the REST surface's casing:\n%s", rpcBody)
	}

	// A partner already sending the other spelling keeps working: protojson
	// accepts a field under both names on the way in. This is the one thing
	// that makes the decision safe to have made.
	out := &catalogv1.CreateProviderResponse{}
	if _, err := r.rest(t, http.MethodPost, "/v1/catalog/providers", bearer(catalog.TokenWriter), out,
		`{"tenantId":"`+tenant+`","displayName":"legacy-client"}`); err != nil {
		t.Fatalf("a partner sending camelCase must keep working: %v", err)
	}
	if out.GetProvider().GetDisplayName() != "legacy-client" {
		t.Fatalf("got %v", out.GetProvider())
	}
}

// connectJSON speaks the Connect unary protocol by hand, so the test sees the
// bytes rather than a decoded message.
func (r *roads) connectJSON(t *testing.T, procedure, body string, md interchange.Metadata) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		r.http.URL+procedure, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	for k, v := range md {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect POST %s: %d\n%s", procedure, resp.StatusCode, raw)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// Phase 3 · "The emitted OpenAPI matches what partners already call, or the
// migration is explicitly versioned."
// ---------------------------------------------------------------------------

var updateGolden = flag.Bool("update", false, "rewrite openapi.json")

// TestTheEmittedOpenAPIMatchesWhatPartnersCall pins the document against a
// committed golden file. It is the partner-facing artifact, so it is under a
// drift gate of its own: a path that changes is a change a partner will
// notice, and it should be visible in a diff before it is visible in their
// logs.
//
// It also asserts the three properties that make the document safe to publish:
// the paths are the ones the annotations declare, the property names are the
// REST surface's, and nothing the contract kept off this road appears at all.
func TestTheEmittedOpenAPIMatchesWhatPartnersCall(t *testing.T) {
	doc, err := catalog.OpenAPI("https://api.example.com")
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	if *updateGolden {
		if err := os.WriteFile("openapi.json", doc, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatalf("%v (run `go test . -update`)", err)
	}
	if string(doc) != string(golden) {
		t.Fatalf("openapi.json is stale; run `go test . -update` and review the diff")
	}

	// Deterministic: same input, same bytes, or the gate flaps and nobody
	// trusts it.
	again, err := catalog.OpenAPI("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(doc) {
		t.Fatal("two emissions of the same contract differ")
	}

	var parsed struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("the emitted document is not JSON: %v", err)
	}

	wantPaths := []string{"/v1/catalog/providers", "/v1/catalog/providers/{provider_id}"}
	got := make([]string, 0, len(parsed.Paths))
	for p := range parsed.Paths {
		got = append(got, p)
	}
	slices.Sort(got)
	if !slices.Equal(got, wantPaths) {
		t.Fatalf("paths are %v, want %v", got, wantPaths)
	}

	// The two roads a partner is not on must not be documented on the road
	// they are on. An internal RPC in a partner-facing spec is a leak.
	for _, absent := range []string{"SyncProvider", "Reconcile", "sync", "reconcile"} {
		if strings.Contains(string(doc), absent) {
			t.Fatalf("%q appears in the partner-facing document; it declares no REST road", absent)
		}
	}

	// Property names are the REST surface's, matching what the transcoder
	// actually writes. A spec that says displayName over a wire that says
	// display_name is worse than no spec.
	if !strings.Contains(string(doc), `"display_name"`) || strings.Contains(string(doc), `"displayName"`) {
		t.Fatal("the document's property names do not match the REST surface's casing")
	}
}
