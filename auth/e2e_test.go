package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	"google.golang.org/protobuf/proto"
)

// roads is one registry, one chain, and two ways in: Connect over HTTP and the
// message engine over an in-process bus. Neither holds the chain -- both go
// through Registry.Dispatch, which is the only place an interceptor runs.
type roads struct {
	reg  *interchange.Registry
	http *rpc.Client
	bus  *memory.Bus
}

func newRoads(t *testing.T, c *interchange.ChainSpec) *roads {
	t.Helper()
	reg := interchange.NewRegistry()
	if err := reg.Register(pingDesc, echo, c); err != nil {
		t.Fatalf("register: %v", err)
	}

	binding := rpc.New(reg)
	if err := binding.Mount(pingDesc); err != nil {
		t.Fatalf("mount: %v", err)
	}
	srv := httptest.NewServer(binding.Handler())
	t.Cleanup(srv.Close)

	bus := memory.New()
	busSrv := engine.NewServer(bus.Driver("server"), reg)
	if err := busSrv.Start(context.Background()); err != nil {
		t.Fatalf("bus server: %v", err)
	}
	t.Cleanup(func() { _ = busSrv.Stop() })

	return &roads{reg: reg, http: rpc.NewClient(http.DefaultClient, srv.URL), bus: bus}
}

// call makes the same call on both roads and returns both outcomes. The
// credential travels as metadata on both -- an HTTP header on one, an envelope
// field on the other -- because the chain sees metadata, not a header.
func (r *roads) call(t *testing.T, procedure string, req proto.Message, md interchange.Metadata) (httpErr, busErr error) {
	t.Helper()
	method := methodDesc(procedure)

	httpErr = r.http.InvokeMethod(context.Background(), method, req, newResponse(procedure), md)

	cli, err := engine.NewClient(context.Background(), r.bus.Driver("client"),
		engine.WithStaticMetadata(md), engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("bus client: %v", err)
	}
	defer cli.Close()
	busErr = cli.Invoke(context.Background(), procedure, req, newResponse(procedure))
	return httpErr, busErr
}

// TestAuthorizationFiresOnBothRoads is the phase's real claim. The chain is
// configured once and handed to Register once; the HTTP binding and the bus
// engine share nothing at the network layer; and the authorization outcome --
// code and reason, not merely "it failed" -- is identical on both.
//
// The classic multi-transport failure is a check enforced in the HTTP handler
// and silently absent on the bus. That is what this rules out.
func TestAuthorizationFiresOnBothRoads(t *testing.T) {
	c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	r := newRoads(t, c)

	t.Run("allowed on both", func(t *testing.T) {
		req := request(procPing, map[string]string{"tenant_id": "acme", "note": "hello"})
		httpErr, busErr := r.call(t, procPing, req, bearer(tokenReader))
		if httpErr != nil || busErr != nil {
			t.Fatalf("http=%v bus=%v", httpErr, busErr)
		}
	})

	cases := []struct {
		name      string
		procedure string
		req       proto.Message
		md        interchange.Metadata
		code      interchange.Code
		reason    string
	}{
		{
			// A verified workload credential of the kind the RPC accepts,
			// holding a role that does not grant the atom it declares.
			name:      "denied by permission",
			procedure: procPlatform,
			req:       request(procPlatform, nil),
			md:        interchange.NewMetadata(map[string]string{"x-api-key": tokenWeakKey}),
			code:      interchange.CodePermissionDenied,
			reason:    auth.ReasonPermissionDenied,
		},
		{
			// A verified credential of a kind this RPC does not accept.
			name:      "denied by credential kind",
			procedure: procPlatform,
			req:       request(procPlatform, nil),
			md:        bearer(tokenWriter),
			code:      interchange.CodeUnauthenticated,
			reason:    auth.ReasonAuthTypeRejected,
		},
		{
			name:      "denied by tenant",
			procedure: procPing,
			req:       request(procPing, map[string]string{"tenant_id": "acme"}),
			md:        bearer(tokenNoTenant),
			code:      interchange.CodePermissionDenied,
			reason:    auth.ReasonTenantDenied,
		},
		{
			name:      "denied because nothing was declared",
			procedure: procUnannotated,
			req:       request(procUnannotated, map[string]string{"tenant_id": "acme"}),
			md:        bearer(tokenReader),
			code:      interchange.CodePermissionDenied,
			reason:    auth.ReasonAnnotationMissing,
		},
		{
			name:      "denied because no credential was presented",
			procedure: procPing,
			req:       request(procPing, map[string]string{"tenant_id": "acme"}),
			md:        nil,
			code:      interchange.CodeUnauthenticated,
			reason:    auth.ReasonUnauthenticated,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpErr, busErr := r.call(t, tc.procedure, tc.req, tc.md)
			assertDenied(t, httpErr, tc.code, tc.reason)
			assertDenied(t, busErr, tc.code, tc.reason)
		})
	}

	t.Run("public is public on both", func(t *testing.T) {
		httpErr, busErr := r.call(t, procPublic, request(procPublic, map[string]string{"note": "hi"}), nil)
		if httpErr != nil || busErr != nil {
			t.Fatalf("a public RPC must serve an anonymous caller on every road: http=%v bus=%v", httpErr, busErr)
		}
	})
}

// TestMissingAnnotationDeniesOnBothRoads isolates the first rule end to end:
// with authn removed, the denial is the annotation policy's rather than the
// credential check's, and it is the same denial on both roads.
func TestMissingAnnotationDeniesOnBothRoads(t *testing.T) {
	c := authzOnly(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	r := newRoads(t, c)

	httpErr, busErr := r.call(t, procUnannotated, request(procUnannotated, map[string]string{"tenant_id": "acme"}), nil)
	assertDenied(t, httpErr, interchange.CodePermissionDenied, auth.ReasonAnnotationMissing)
	assertDenied(t, busErr, interchange.CodePermissionDenied, auth.ReasonAnnotationMissing)
}

// TestCoreServesWithoutTheModule: the same registry, the same bindings, an
// empty chain. Core does not require this module, and a service that installs
// none of it works -- which is the property that makes authorization optional
// rather than nominally optional.
func TestCoreServesWithoutTheModule(t *testing.T) {
	r := newRoads(t, interchange.Chain())
	req := request(procPing, map[string]string{"tenant_id": "acme", "note": "hello"})
	httpErr, busErr := r.call(t, procPing, req, nil)
	if httpErr != nil || busErr != nil {
		t.Fatalf("an empty chain must serve on every road: http=%v bus=%v", httpErr, busErr)
	}
}
