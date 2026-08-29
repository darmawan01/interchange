package rest_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rest"
	"github.com/darmawan01/interchange/binding/rest/internal/testfixture"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/errors"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
)

// tracer records the stages a call traversed, per road, so two bindings can be
// compared without either knowing it is being watched. Copied from core's
// conformance suite, because it is the same invariant being asserted.
type tracer struct {
	mu  sync.Mutex
	log map[string][]string
}

func newTracer() *tracer { return &tracer{log: map[string][]string{}} }

func (t *tracer) stage(name string) interchange.Stage {
	return interchange.Named(name, func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			t.mu.Lock()
			t.log[req.Metadata.Get("x-trace")] = append(t.log[req.Metadata.Get("x-trace")], name)
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
	reg    *interchange.Registry
	sd     *interchange.ServiceDesc
	rest   *httptest.Server
	rpc    *httptest.Server
	client *rpc.Client
	chain  *interchange.ChainSpec
	tracer *tracer
}

// newStack registers the fixture once and mounts it on both HTTP bindings.
// One registry, one chain, two roads -- which is the wiring the symmetry test
// below depends on.
func newStack(t *testing.T, opts ...rest.Option) *stack {
	t.Helper()
	tr := newTracer()
	chain := interchange.Chain(tr.stage("telemetry"), tr.stage("authz"), tr.stage("validate"))
	if err := chain.Err(); err != nil {
		t.Fatal(err)
	}

	sd, err := testfixture.Desc()
	if err != nil {
		t.Fatal(err)
	}
	reg := interchange.NewRegistry()
	if err := reg.Register(sd, testfixture.New(), chain); err != nil {
		t.Fatal(err)
	}

	restBinding := rest.New(reg, opts...)
	if err := restBinding.Mount(sd); err != nil {
		t.Fatal(err)
	}
	rpcBinding := rpc.New(reg)
	if err := rpcBinding.Mount(sd); err != nil {
		t.Fatal(err)
	}

	restSrv := httptest.NewServer(restBinding.Handler())
	t.Cleanup(restSrv.Close)
	rpcSrv := httptest.NewServer(rpcBinding.Handler())
	t.Cleanup(rpcSrv.Close)

	return &stack{
		reg: reg, sd: sd, rest: restSrv, rpc: rpcSrv,
		client: rpc.NewClient(http.DefaultClient, rpcSrv.URL),
		chain:  chain, tracer: tr,
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

// do issues a plain HTTP request -- no client library, because a partner does
// not have one.
func (s *stack) do(t *testing.T, method, path, body string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.rest.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header[k] = v
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
	return resp, raw
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	return out
}

// TestGetBindsPathAndQuery is the round trip: a RESTful URI, a path variable
// bound onto a field, a nested query parameter bound into a sub-message, and
// snake_case coming back.
func TestGetBindsPathAndQuery(t *testing.T) {
	s := newStack(t)

	resp, raw := s.do(t, http.MethodGet, "/v1/probes/probe_7?page.page_size=25", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type is %q", ct)
	}

	body := decode(t, raw)
	probe, ok := body["probe"].(map[string]any)
	if !ok {
		t.Fatalf("no probe in %s", raw)
	}
	if got := probe["probe_id"]; got != "probe_7" {
		t.Errorf("probe_id = %v, want the path variable", got)
	}
	if got := probe["display_name"]; got != "probe:probe_7" {
		t.Errorf("display_name = %v", got)
	}
	if got := probe["attempt_count"]; got != float64(25) {
		t.Errorf("attempt_count = %v, want the nested query parameter", got)
	}
}

// TestRESTIsSnakeCase is §08's decision, asserted rather than assumed:
// camelCase on the RPC surface, snake_case on REST. The RPC road is checked in
// the same test so a change to one is visibly a change to the other.
func TestRESTIsSnakeCase(t *testing.T) {
	s := newStack(t)

	_, raw := s.do(t, http.MethodGet, "/v1/probes/probe_7", "", nil)
	body := decode(t, raw)
	probe, ok := body["probe"].(map[string]any)
	if !ok {
		t.Fatalf("no probe in %s", raw)
	}
	for _, name := range []string{"probe_id", "display_name", "attempt_count"} {
		if _, ok := probe[name]; !ok {
			t.Errorf("REST response has no %s: %s", name, raw)
		}
	}
	for _, name := range []string{"probeId", "displayName", "attemptCount"} {
		if _, ok := probe[name]; ok {
			t.Errorf("REST response carries camelCase %s", name)
		}
	}

	rpcResp, err := http.Post(s.rpc.URL+testfixture.GetProbeProcedure, "application/json",
		strings.NewReader(`{"probe_id":"probe_7"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer rpcResp.Body.Close()
	rpcRaw, _ := io.ReadAll(rpcResp.Body)
	if rpcResp.StatusCode != http.StatusOK {
		t.Fatalf("rpc status %d: %s", rpcResp.StatusCode, rpcRaw)
	}
	rpcProbe, ok := decode(t, rpcRaw)["probe"].(map[string]any)
	if !ok {
		t.Fatalf("no probe in %s", rpcRaw)
	}
	if _, ok := rpcProbe["probeId"]; !ok {
		t.Errorf("the RPC surface stopped being camelCase: %s", rpcRaw)
	}
}

// TestPostBindsBody: a JSON body binds onto the request message, in either
// casing -- a partner already sending camelCase is not broken by the decision
// to emit snake_case.
func TestPostBindsBody(t *testing.T) {
	s := newStack(t)

	for _, body := range []string{
		`{"display_name":"north","tags":["a","b"]}`,
		`{"displayName":"north","tags":["a","b"]}`,
	} {
		resp, raw := s.do(t, http.MethodPost, "/v1/probes", body, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d for %s: %s", resp.StatusCode, body, raw)
		}
		probe := decode(t, raw)["probe"].(map[string]any)
		if got := probe["display_name"]; got != "north" {
			t.Errorf("%s: display_name = %v", body, got)
		}
		tags, _ := probe["tags"].([]any)
		if len(tags) != 2 || tags[0] != "a" {
			t.Errorf("%s: tags = %v", body, tags)
		}
	}
}

// TestChainSymmetry is the point of the phase. The chain is configured once
// and handed to Register once; neither binding is given it. Both roads run
// the same stages in the same order.
func TestChainSymmetry(t *testing.T) {
	s := newStack(t)

	_, raw := s.do(t, http.MethodGet, "/v1/probes/probe_7", "", http.Header{"X-Trace": {"rest"}})
	restProbe, ok := decode(t, raw)["probe"].(map[string]any)
	if !ok {
		t.Fatalf("no probe in %s", raw)
	}

	req := testfixture.NewMessage("GetProbeRequest")
	testfixture.SetString(req, "probe_id", "probe_7")
	out := testfixture.NewMessage("GetProbeResponse")
	if err := s.client.InvokeMethod(context.Background(), s.method(t, testfixture.GetProbeProcedure),
		req, out, interchange.Metadata{"x-trace": "rpc"}); err != nil {
		t.Fatalf("rpc call: %v", err)
	}

	if got := testfixture.GetString(out, "probe", "display_name"); got != restProbe["display_name"] {
		t.Fatalf("the two roads disagree: rpc=%q rest=%v", got, restProbe["display_name"])
	}

	want := s.chain.Names()
	if got := s.tracer.trace("rest"); !slices.Equal(got, want) {
		t.Fatalf("the REST road ran %v, the chain is %v", got, want)
	}
	if got := s.tracer.trace("rpc"); !slices.Equal(got, want) {
		t.Fatalf("the RPC road ran %v, the chain is %v", got, want)
	}
}

// TestErrorIsProblemJSON: the REST road reports status as problem+json (§04),
// and the reason it carries is the one the same error produces over Connect.
func TestErrorIsProblemJSON(t *testing.T) {
	s := newStack(t)

	resp, raw := s.do(t, http.MethodGet, "/v1/probes/probe_7/failure", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != errors.ProblemContentType {
		t.Errorf("content-type is %q, want %q", ct, errors.ProblemContentType)
	}
	if got := resp.Header.Get(errors.ReasonHeader); got != testfixture.FailReason {
		t.Errorf("%s header is %q", errors.ReasonHeader, got)
	}

	body := decode(t, raw)
	if got := body["reason"]; got != testfixture.FailReason {
		t.Errorf("reason = %v", got)
	}
	if got := body["status"]; got != float64(http.StatusNotFound) {
		t.Errorf("status = %v", got)
	}
	if got, _ := body["detail"].(string); !strings.Contains(got, "no such probe") {
		t.Errorf("detail = %q", got)
	}

	// The same error over Connect. One reason, whichever road it came back on.
	err := s.client.InvokeMethod(context.Background(), s.method(t, testfixture.FailProbeProcedure),
		testfixture.NewMessage("FailProbeRequest"), testfixture.NewMessage("FailProbeResponse"), nil)
	if err == nil {
		t.Fatal("the RPC road succeeded")
	}
	if got := interchange.ReasonOf(err); got != testfixture.FailReason {
		t.Errorf("the RPC road reports reason %q", got)
	}
	if got := interchange.CodeOf(err); got != interchange.CodeNotFound {
		t.Errorf("the RPC road reports code %v", got)
	}
}

// TestTransportAnnotationIsLoadBearing: RpcOnlyProbe carries a google.api.http
// rule and does not declare TRANSPORT_REST. The rule is what a gateway would
// route on; the annotation is what says no.
func TestTransportAnnotationIsLoadBearing(t *testing.T) {
	s := newStack(t)

	resp, raw := s.do(t, http.MethodGet, "/v1/rpc-only/probe_7", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a method not declared on TRANSPORT_REST answered %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != errors.ProblemContentType {
		t.Errorf("a 404 is still an error body: content-type is %q", ct)
	}

	out := testfixture.NewMessage("RpcOnlyProbeResponse")
	if err := s.client.InvokeMethod(context.Background(), s.method(t, testfixture.RpcOnlyProbeProcedure),
		testfixture.NewMessage("RpcOnlyProbeRequest"), out, nil); err != nil {
		t.Fatalf("the same method must still answer over RPC: %v", err)
	}
	if got := testfixture.GetString(out, "probe", "display_name"); got != "rpc-only" {
		t.Errorf("display_name = %q", got)
	}
}

// TestInternalIsOffTheRoad: `internal` keeps a method off every public
// binding, by its REST URI and by its procedure alike.
func TestInternalIsOffTheRoad(t *testing.T) {
	s := newStack(t)

	resp, _ := s.do(t, http.MethodPost, "/v1/reconcile", `{"tenant_id":"t1"}`, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an internal method answered %d over REST", resp.StatusCode)
	}
	resp, _ = s.do(t, http.MethodPost, testfixture.ReconcileProbesProcedure, `{}`, nil)
	if resp.StatusCode == http.StatusOK {
		t.Error("an internal method answered on its procedure path")
	}
}

// TestMountRefusesAServiceWithNoDescriptor: a ServiceDesc with no descriptor
// has no annotations to transcode from, and saying so is better than mounting
// nothing and reporting success.
func TestMountRefusesAServiceWithNoDescriptor(t *testing.T) {
	sd, err := testfixture.Desc()
	if err != nil {
		t.Fatal(err)
	}
	sd.Desc = nil
	reg := interchange.NewRegistry()
	b := rest.New(reg)
	if err := b.Register(sd, testfixture.New(), nil); err == nil {
		t.Fatal("Mount accepted a service with no descriptor")
	} else if !strings.Contains(err.Error(), "no descriptor") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// TestMountRefusesAROADWithNoURI: a method that declares REST and carries no
// google.api.http rule is a contract nobody can call. Mounting it silently
// would mean the annotation said one thing and the surface did another.
func TestMountRefusesARoadWithNoURI(t *testing.T) {
	sd, err := testfixture.Desc()
	if err != nil {
		t.Fatal(err)
	}
	for i := range sd.Methods {
		if sd.Methods[i].Name == "PingProbe" {
			sd.Methods[i].Transports = append(sd.Methods[i].Transports, transportv1.Transport_TRANSPORT_REST)
		}
	}
	if err := rest.New(interchange.NewRegistry()).Register(sd, testfixture.New(), nil); err == nil {
		t.Fatal("Mount accepted a REST method with no URI")
	} else if !strings.Contains(err.Error(), "google.api.http") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

// TestTranscoderFailureIsProblemJSON: a body the transcoder cannot bind never
// reaches a handler, so there is no code and no reason to project -- but the
// answer is still an error body of the shape this surface promises.
func TestTranscoderFailureIsProblemJSON(t *testing.T) {
	s := newStack(t)

	resp, raw := s.do(t, http.MethodPost, "/v1/probes", `{"display_name":`, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != errors.ProblemContentType {
		t.Fatalf("content-type is %q: %s", ct, raw)
	}
	body := decode(t, raw)
	if got := body["status"]; got != float64(http.StatusBadRequest) {
		t.Errorf("status = %v", got)
	}
	if got, _ := body["detail"].(string); got == "" {
		t.Error("the transcoder's own message was dropped")
	}
}

// TestProblemWriterIsASeam: an adopter with its own error taxonomy is not
// forced into ours.
func TestProblemWriterIsASeam(t *testing.T) {
	var seen rest.Failure
	s := newStack(t, rest.WithProblemWriter(func(w http.ResponseWriter, _ *http.Request, f rest.Failure) {
		seen = f
		w.Header().Set("Content-Type", "application/vnd.example.error+json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"oops":true}`))
	}))

	resp, raw := s.do(t, http.MethodGet, "/v1/probes/probe_7/failure", "", nil)
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if got := string(raw); got != `{"oops":true}` {
		t.Errorf("body = %s", got)
	}
	if got := interchange.ReasonOf(seen.Err); got != testfixture.FailReason {
		t.Errorf("the writer was handed reason %q", got)
	}
	if got := interchange.CodeOf(seen.Err); got != interchange.CodeNotFound {
		t.Errorf("the writer was handed code %v", got)
	}
}

// TestSuccessIsNotBuffered: only a failed response is held back, so a success
// reaches the client as the transcoder wrote it.
func TestSuccessIsNotBuffered(t *testing.T) {
	s := newStack(t)
	resp, raw := s.do(t, http.MethodGet, "/v1/probes/probe_7", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type is %q", ct)
	}
}
