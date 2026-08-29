package auth_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	"google.golang.org/protobuf/proto"
)

// chain builds the module's stock arrangement: core's three stages, then
// authn, then authz. Names, not positions -- inserting a stage upstream must
// not silently reorder this.
func chain(t *testing.T, cfg auth.Config, az auth.Authorizer, opts ...auth.AuthzOption) *interchange.ChainSpec {
	t.Helper()
	c := interchange.DefaultChain(interchange.Config{}).
		Append(auth.Authn(cfg, tokens()), auth.Authz(cfg, az, opts...))
	if err := c.Err(); err != nil {
		t.Fatalf("chain: %v", err)
	}
	return c
}

// authzOnly is the authz stage with no authn in front of it, so a test of the
// annotation policy is not answered by the credential check first.
func authzOnly(t *testing.T, cfg auth.Config, az auth.Authorizer, opts ...auth.AuthzOption) *interchange.ChainSpec {
	t.Helper()
	c := interchange.Chain(auth.Authz(cfg, az, opts...))
	if err := c.Err(); err != nil {
		t.Fatalf("chain: %v", err)
	}
	return c
}

// TestAbsentIsNotPublic is the first of the module's three rules. The RPC is
// annotated nowhere, the caller is authenticated and holds every role, and it
// is still denied -- because a missing annotation is a missing decision, not
// an open door.
func TestAbsentIsNotPublic(t *testing.T) {
	c := authzOnly(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	_, err := dispatch(t, c, procUnannotated, request(procUnannotated, map[string]string{"tenant_id": "acme"}), nil)
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonAnnotationMissing)
	if !strings.Contains(interchange.MessageOf(err), "public: true") {
		t.Fatalf("the denial should say how to opt out, got %q", interchange.MessageOf(err))
	}
}

// TestPublicIsAllowed: the explicit opt-out works, and it works without an
// authorizer being consulted at all.
func TestPublicIsAllowed(t *testing.T) {
	consulted := false
	az := auth.AuthorizerFunc(func(context.Context, string, auth.Annotation, map[string]string, proto.Message) error {
		consulted = true
		return nil
	})
	c := chain(t, auth.Config{}, az)

	msg := request(procPublic, map[string]string{"note": "hi"})
	resp, err := dispatch(t, c, procPublic, msg, nil)
	if err != nil {
		t.Fatalf("a public RPC must serve an anonymous caller: %v", err)
	}
	if got := getString(resp.Msg.ProtoReflect(), "note"); got != "hi" {
		t.Fatalf("handler did not run: note is %q", got)
	}
	if consulted {
		t.Fatal("a public RPC must not reach the Authorizer")
	}
}

// TestFailClosedOnNilResolver is the second rule. RBAC would allow this call:
// the reader holds ping.read and the request names its own tenant. It is
// denied anyway, because the tenant check the annotation implies has nobody
// wired to make it.
func TestFailClosedOnNilResolver(t *testing.T) {
	msg := request(procPing, map[string]string{"tenant_id": "acme"})

	wired := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	if _, err := dispatch(t, wired, procPing, msg, bearer(tokenReader)); err != nil {
		t.Fatalf("with a resolver wired this call must be allowed: %v", err)
	}

	unwired := chain(t, auth.Config{}, rbac(t))
	_, err := dispatch(t, unwired, procPing, msg, bearer(tokenReader))
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonNotWired)
}

// TestFailClosedOnNilAuthorizer: installing the stage and forgetting the
// decider denies rather than allows.
func TestFailClosedOnNilAuthorizer(t *testing.T) {
	c := chain(t, auth.Config{}, nil, auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	_, err := dispatch(t, c, procPing, request(procPing, map[string]string{"tenant_id": "acme"}), bearer(tokenReader))
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonNotWired)
}

// TestFailClosedWithoutARegistry: an envelope that never went through
// Registry.Dispatch carries no method descriptor, so there is no annotation to
// read. Deny -- the alternative is a chain that authorizes nothing when
// somebody wires it up by hand.
func TestFailClosedWithoutARegistry(t *testing.T) {
	stage := auth.Authz(auth.Config{}, rbac(t))
	call := interchange.Chain(stage).MustWrap(func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
		return req, nil
	})
	_, err := call(context.Background(), interchange.NewEnvelope(procPing))
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonNotWired)
}

// TestStrictnessPolicy: on_missing_annotation is this module's knob, and all
// three settings do what they say.
func TestStrictnessPolicy(t *testing.T) {
	msg := request(procUnannotated, map[string]string{"tenant_id": "acme"})

	t.Run("default is error", func(t *testing.T) {
		var zero auth.Config
		if got := zero.Strictness(); got != auth.StrictError {
			t.Fatalf("the zero config is %q, want error", got)
		}
		_, err := dispatch(t, authzOnly(t, zero, rbac(t)), procUnannotated, msg, nil)
		assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonAnnotationMissing)
	})

	t.Run("warn allows and says so", func(t *testing.T) {
		var buf strings.Builder
		cfg := auth.Config{
			OnMissingAnnotation: auth.StrictWarn,
			Logger:              slog.New(slog.NewTextHandler(&buf, nil)),
		}
		if _, err := dispatch(t, authzOnly(t, cfg, rbac(t)), procUnannotated, msg, nil); err != nil {
			t.Fatalf("warn must allow: %v", err)
		}
		if !strings.Contains(buf.String(), procUnannotated) {
			t.Fatalf("warn must name the procedure, log was %q", buf.String())
		}
	})

	t.Run("ignore allows silently", func(t *testing.T) {
		var buf strings.Builder
		cfg := auth.Config{
			OnMissingAnnotation: auth.StrictIgnore,
			Logger:              slog.New(slog.NewTextHandler(&buf, nil)),
		}
		if _, err := dispatch(t, authzOnly(t, cfg, rbac(t)), procUnannotated, msg, nil); err != nil {
			t.Fatalf("ignore must allow: %v", err)
		}
		if buf.Len() != 0 {
			t.Fatalf("ignore must not log, got %q", buf.String())
		}
	})

	t.Run("an unreadable policy is refused", func(t *testing.T) {
		if err := (auth.Config{OnMissingAnnotation: "lenient"}).Validate(); err == nil {
			t.Fatal("an unknown policy value must be a config error")
		}
		if got := (auth.Config{OnMissingAnnotation: "lenient"}).Strictness(); got != auth.StrictError {
			t.Fatalf("an unreadable policy resolves to %q, want error", got)
		}
	})
}

// TestPlatformRPCSkipsTenantScope: platform: true says the request carries no
// tenant, so no resolver is required and none is consulted.
func TestPlatformRPCSkipsTenantScope(t *testing.T) {
	c := chain(t, auth.Config{}, rbac(t))
	if _, err := dispatch(t, c, procPlatform, request(procPlatform, nil),
		interchange.NewMetadata(map[string]string{"x-api-key": tokenWorkloadKey})); err != nil {
		t.Fatalf("a platform RPC must not need a tenant resolver: %v", err)
	}
}

// TestTenantScopedRPCNeedsATenant: the annotation says tenant-scoped and the
// request named none. That is a contract bug, and it is denied rather than
// waved through.
func TestTenantScopedRPCNeedsATenant(t *testing.T) {
	c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	_, err := dispatch(t, c, procPing, request(procPing, map[string]string{"note": "x"}), bearer(tokenReader))
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonTenantMissing)
}

// TestTenantOfAnotherPrincipalIsDenied: the stock resolver is a real check,
// not a placeholder.
func TestTenantOfAnotherPrincipalIsDenied(t *testing.T) {
	c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
	_, err := dispatch(t, c, procPing, request(procPing, map[string]string{"tenant_id": "acme"}), bearer(tokenNoTenant))
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonTenantDenied)
}

// TestThirdPartyAuthorizer is the extension point's own exit criterion: a
// bespoke decider, written here in the test, replaces RBAC without touching
// core, the contract, or any binding. Nothing in this test imports a core
// internal or edits a .proto -- it implements one method.
func TestThirdPartyAuthorizer(t *testing.T) {
	// A permission service that authorizes on the message body: the note
	// field has to name the tenant. No RBAC, no roles, no annotation change.
	var sawProcedure, sawAtom string
	bespoke := auth.AuthorizerFunc(func(_ context.Context, procedure string, ann auth.Annotation, md map[string]string, msg proto.Message) error {
		sawProcedure, sawAtom = procedure, ann.Permission.Atom()
		if md["authorization"] == "" {
			return interchange.Errorf(interchange.CodeUnauthenticated, "no credential").
				WithReason(auth.ReasonUnauthenticated)
		}
		if note := getString(msg.ProtoReflect(), "note"); note != "acme" {
			return interchange.Errorf(interchange.CodeNotFound, "no such note").
				WithReason("NOTE_NOT_FOUND")
		}
		return nil
	})

	c := chain(t, auth.Config{}, bespoke, auth.WithTenantScoper(auth.PrincipalTenantScoper()))

	allowed := request(procPing, map[string]string{"tenant_id": "acme", "note": "acme"})
	if _, err := dispatch(t, c, procPing, allowed, bearer(tokenReader)); err != nil {
		t.Fatalf("the bespoke authorizer allowed this call: %v", err)
	}
	if sawProcedure != procPing || sawAtom != "ping.read" {
		t.Fatalf("the authorizer saw %q/%q, want %q/ping.read", sawProcedure, sawAtom, procPing)
	}

	denied := request(procPing, map[string]string{"tenant_id": "acme", "note": "globex"})
	_, err := dispatch(t, c, procPing, denied, bearer(tokenReader))
	// Its own code and reason survive: an authorizer that says "not found" to
	// avoid leaking existence is believed rather than flattened to denied.
	assertDenied(t, err, interchange.CodeNotFound, "NOTE_NOT_FOUND")
}

// TestChainIsOrdinary: the module's stages are named stages like any other, so
// a deployment can reorder, replace or remove them by name.
func TestChainIsOrdinary(t *testing.T) {
	c := interchange.DefaultChain(interchange.Config{}).
		Append(auth.Authn(auth.Config{}, tokens()), auth.Authz(auth.Config{}, rbac(t)))
	want := []string{"telemetry", "recover", "deadline", auth.StageAuthn, auth.StageAuthz}
	for i, name := range c.Names() {
		if name != want[i] {
			t.Fatalf("chain is %v, want %v", c.Names(), want)
		}
	}
	if err := c.Remove(auth.StageAuthz).Err(); err != nil {
		t.Fatalf("authz must be removable by name: %v", err)
	}
}
