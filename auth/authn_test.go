package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
)

// TestAuthnFailsClosed: three ways to have no verified principal, three
// denials. None of them reaches the handler.
func TestAuthnFailsClosed(t *testing.T) {
	msg := request(procPing, map[string]string{"tenant_id": "acme"})

	t.Run("no credential", func(t *testing.T) {
		c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
		_, err := dispatch(t, c, procPing, msg, nil)
		assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonUnauthenticated)
	})

	t.Run("unknown credential", func(t *testing.T) {
		c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))
		_, err := dispatch(t, c, procPing, msg, bearer("forged"))
		assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonUnauthenticated)
	})

	t.Run("no Authenticator", func(t *testing.T) {
		c := interchange.Chain(auth.Authn(auth.Config{}, nil))
		if err := c.Err(); err != nil {
			t.Fatal(err)
		}
		_, err := dispatch(t, c, procPing, msg, bearer(tokenReader))
		assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonNotWired)
	})

	t.Run("an authenticator that returns nothing is not an authentication", func(t *testing.T) {
		silent := auth.AuthenticatorFunc(func(context.Context, map[string]string) (*auth.Principal, error) {
			return nil, nil
		})
		c := interchange.Chain(auth.Authn(auth.Config{}, silent), auth.Authz(auth.Config{}, rbac(t)))
		if err := c.Err(); err != nil {
			t.Fatal(err)
		}
		_, err := dispatch(t, c, procPing, msg, bearer(tokenReader))
		assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonUnauthenticated)
	})
}

// TestAuthnAllowsAnonymousOnlyOnPublic: the credential check reads the same
// annotation the authz stage does, so "public" means the same thing in both.
func TestAuthnAllowsAnonymousOnlyOnPublic(t *testing.T) {
	c := interchange.Chain(auth.Authn(auth.Config{}, tokens()))
	if _, err := dispatch(t, c, procPublic, nil, nil); err != nil {
		t.Fatalf("anonymous must reach a public RPC: %v", err)
	}
	_, err := dispatch(t, c, procUnannotated, nil, nil)
	assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonUnauthenticated)
}

// TestAuthTypesAreEnforced: auth_types is the annotation naming which
// credential kinds an RPC accepts. A verified caller presenting the wrong kind
// is still refused -- otherwise the list is decoration.
func TestAuthTypesAreEnforced(t *testing.T) {
	c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))

	// PingPlatform accepts AUTH_TYPE_WORKLOAD only; the writer's session
	// token verifies fine and is refused on kind.
	_, err := dispatch(t, c, procPlatform, request(procPlatform, nil), bearer(tokenWriter))
	assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonAuthTypeRejected)

	// The same RPC with the workload key is allowed.
	if _, err := dispatch(t, c, procPlatform, request(procPlatform, nil),
		interchange.NewMetadata(map[string]string{"x-api-key": tokenWorkloadKey})); err != nil {
		t.Fatalf("the workload key must be accepted: %v", err)
	}
}

// TestTokenAuthenticator covers the shipped credential reader on its own: both
// metadata forms, and the difference between "nothing presented" and "that did
// not verify".
func TestTokenAuthenticator(t *testing.T) {
	a := tokens()
	ctx := context.Background()

	p, err := a.Authenticate(ctx, map[string]string{"authorization": "Bearer " + tokenReader})
	if err != nil {
		t.Fatalf("bearer token: %v", err)
	}
	if p.Subject != "user:reader" || p.AuthType != authv1.AuthType_AUTH_TYPE_SESSION {
		t.Fatalf("resolved %+v", p)
	}

	p, err = a.Authenticate(ctx, map[string]string{"x-api-key": tokenWorkloadKey})
	if err != nil {
		t.Fatalf("api key: %v", err)
	}
	if p.Subject != "svc:indexer" {
		t.Fatalf("resolved %+v", p)
	}

	if _, err := a.Authenticate(ctx, nil); !errors.Is(err, auth.ErrNoCredential) {
		t.Fatalf("an empty metadata map is ErrNoCredential, got %v", err)
	}
	if _, err := a.Authenticate(ctx, map[string]string{"authorization": "Basic abc"}); !errors.Is(err, auth.ErrNoCredential) {
		t.Fatalf("a scheme this authenticator does not read is no credential, got %v", err)
	}
	if _, err := a.Authenticate(ctx, map[string]string{"authorization": "Bearer forged"}); err == nil {
		t.Fatal("a token that does not resolve must be an error, not an anonymous principal")
	}
}

// TestPrincipalInContext: a handler reads who called it, and an anonymous call
// is distinguishable from a verified one.
func TestPrincipalInContext(t *testing.T) {
	if _, ok := auth.PrincipalFromContext(context.Background()); ok {
		t.Fatal("an empty context carries no principal")
	}
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "user:reader", Roles: []string{"reader"}})
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok || !p.HasRole("reader") || p.Anonymous() {
		t.Fatalf("principal did not survive the context: %+v ok=%v", p, ok)
	}
	var nilp *auth.Principal
	if !nilp.Anonymous() {
		t.Fatal("a nil principal is anonymous")
	}
}
