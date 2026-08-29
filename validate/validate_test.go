package validate_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	"github.com/darmawan01/interchange/validate"
	testdatav1 "github.com/darmawan01/interchange/validate/internal/testpb/interchange/validate/testdata/v1"
	"github.com/darmawan01/interchange/validate/internal/testsvc"
	"google.golang.org/protobuf/encoding/protojson"
)

// fixture wires one registry, one chain and two roads out of it: a Connect
// binding over httptest and the message engine over the in-process bus.
// Everything below asserts that the two roads answer identically, which is
// only interesting because nothing in either of them holds the chain.
type fixture struct {
	impl *testsvc.Impl
	http *httptest.Server
	bus  *engine.Client
}

func newFixture(t *testing.T, opts ...validate.Option) *fixture {
	t.Helper()
	ctx := context.Background()

	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}

	impl := &testsvc.Impl{}
	chain := interchange.DefaultChain(interchange.Config{}).Append(validate.Stage(v, opts...))
	if err := chain.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(chain.Names(), validate.StageName) {
		t.Fatalf("the stage did not land in the chain by name: %v", chain.Names())
	}

	reg := interchange.NewRegistry()
	binding := rpc.New(reg)
	if err := binding.Register(testsvc.Desc(), impl, chain); err != nil {
		t.Fatal(err)
	}

	bus := memory.New()
	srv := engine.NewServer(bus.Driver("server"), reg)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	cli, err := engine.NewClient(ctx, bus.Driver("client"), engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	hs := httptest.NewServer(binding)
	t.Cleanup(hs.Close)

	return &fixture{impl: impl, http: hs, bus: cli}
}

// overHTTP posts the request as Connect JSON and returns the status, the
// reason header and the metadata the response carried.
func (f *fixture) overHTTP(t *testing.T, req *testdatav1.CreateProviderRequest) (int, interchange.Metadata) {
	t.Helper()
	body, err := protojson.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(f.http.URL+testsvc.CreateProviderProcedure, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	md := interchange.Metadata{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			md.Set(k, v[0])
		}
	}
	return resp.StatusCode, md
}

// TestSameRuleOnEveryTransport is the module's whole claim: the rule is
// written once in fixture.proto, and the violation that comes back is the
// same violation on the Connect road and on the bus -- same code, same
// reason, same field paths, same rule ids.
func TestSameRuleOnEveryTransport(t *testing.T) {
	f := newFixture(t)
	bad := &testdatav1.CreateProviderRequest{Name: "ab", Email: "not-an-email", RateLimit: -1}

	var out testdatav1.CreateProviderResponse
	busErr := f.bus.Invoke(context.Background(), testsvc.CreateProviderProcedure, bad, &out)
	if busErr == nil {
		t.Fatal("the bus accepted a message that breaks three rules")
	}
	if got := interchange.CodeOf(busErr); got != interchange.CodeInvalidArgument {
		t.Errorf("bus code = %v, want invalid_argument", got)
	}
	if got := interchange.ReasonOf(busErr); got != validate.DefaultReason {
		t.Errorf("bus reason = %q", got)
	}

	status, headers := f.overHTTP(t, bad)
	if status != http.StatusBadRequest {
		t.Errorf("HTTP status = %d, want 400", status)
	}
	if got := headers.Get(rpc.ErrorReasonHeader); got != validate.DefaultReason {
		t.Errorf("%s = %q", rpc.ErrorReasonHeader, got)
	}

	busViolations := validate.ViolationsOf(busErr)
	httpViolations := validate.ViolationsFrom(headers)
	if !slices.Equal(busViolations, httpViolations) {
		t.Fatalf("the two roads disagree:\n bus: %+v\nhttp: %+v", busViolations, httpViolations)
	}

	want := []validate.Violation{
		{Field: "name", Rule: "string.min_len"},
		{Field: "email", Rule: "string.email"},
		{Field: "rate_limit", Rule: "int32.gte"},
	}
	if len(busViolations) != len(want) {
		t.Fatalf("got %d violations, want %d: %+v", len(busViolations), len(want), busViolations)
	}
	for i, w := range want {
		got := busViolations[i]
		if got.Field != w.Field || got.Rule != w.Rule {
			t.Errorf("violation %d = %s/%s, want %s/%s", i, got.Field, got.Rule, w.Field, w.Rule)
		}
		if got.Message == "" {
			t.Errorf("violation %d carries no message", i)
		}
	}
	if validate.Count(busErr) != len(want) {
		t.Errorf("%s = %d", validate.MetaCount, validate.Count(busErr))
	}

	if len(f.impl.Called) != 0 {
		t.Errorf("an invalid message reached the handler %d time(s)", len(f.impl.Called))
	}
}

// TestValidMessagePasses: the interceptor is not simply refusing everything.
func TestValidMessagePasses(t *testing.T) {
	f := newFixture(t)
	good := &testdatav1.CreateProviderRequest{Name: "acme", Email: "ops@acme.example", RateLimit: 10}

	var out testdatav1.CreateProviderResponse
	if err := f.bus.Invoke(context.Background(), testsvc.CreateProviderProcedure, good, &out); err != nil {
		t.Fatalf("bus rejected a valid message: %v", err)
	}
	if out.GetId() != "provider/acme" {
		t.Errorf("id = %q", out.GetId())
	}
	if status, _ := f.overHTTP(t, good); status != http.StatusOK {
		t.Errorf("HTTP status = %d, want 200", status)
	}
	if len(f.impl.Called) != 2 {
		t.Errorf("the handler ran %d times, want 2", len(f.impl.Called))
	}
}

// TestMaxDetails: the count stays exact even when the detail list is capped,
// so a caller can tell "one bad field" from "twelve, here are the first
// three".
func TestMaxDetails(t *testing.T) {
	f := newFixture(t, validate.WithMaxDetails(1))
	bad := &testdatav1.CreateProviderRequest{Name: "ab", Email: "not-an-email", RateLimit: -1}

	var out testdatav1.CreateProviderResponse
	err := f.bus.Invoke(context.Background(), testsvc.CreateProviderProcedure, bad, &out)
	if got := len(validate.ViolationsOf(err)); got != 1 {
		t.Errorf("reported %d violations, want 1", got)
	}
	if got := validate.Count(err); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

// TestInjectedValidator: the validator is the caller's, not the module's. A
// fail-fast instance stops at the first violation, which is observable
// through the same metadata.
func TestInjectedValidator(t *testing.T) {
	v, err := protovalidate.New(protovalidate.WithFailFast())
	if err != nil {
		t.Fatal(err)
	}
	call := validate.Interceptor(v)(func(context.Context, *interchange.Envelope) (*interchange.Envelope, error) {
		t.Error("the handler ran")
		return nil, nil
	})
	env := interchange.NewEnvelope("/interchange.validate.testdata.v1.ProviderService/CreateProvider")
	env.Msg = &testdatav1.CreateProviderRequest{Name: "ab", Email: "not-an-email", RateLimit: -1}

	_, verr := call(context.Background(), env)
	if got := validate.Count(verr); got != 1 {
		t.Errorf("a fail-fast validator reported %d violations", got)
	}
}

// TestNilValidatorUsesTheGlobal keeps the zero-configuration path honest.
func TestNilValidatorUsesTheGlobal(t *testing.T) {
	call := validate.Interceptor(nil)(func(context.Context, *interchange.Envelope) (*interchange.Envelope, error) {
		return &interchange.Envelope{}, nil
	})
	env := interchange.NewEnvelope("/interchange.validate.testdata.v1.ProviderService/CreateProvider")
	env.Msg = &testdatav1.CreateProviderRequest{Name: "ab"}
	if _, err := call(context.Background(), env); interchange.CodeOf(err) != interchange.CodeInvalidArgument {
		t.Errorf("err = %v", err)
	}
}
