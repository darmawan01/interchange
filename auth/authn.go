package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/darmawan01/interchange"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
)

// StageAuthn is the chain anchor for credential verification. Chains are
// extended by name, so this constant is API.
const StageAuthn = "authn"

// ErrNoCredential tells the authn stage that the request carried no
// credential at all, as distinct from one that failed to verify. The stage
// lets it through only on an RPC explicitly marked public.
var ErrNoCredential = errors.New("auth: request carries no credential")

// Authenticator turns request metadata into a principal. It sees the
// transport-neutral metadata map, which is why one implementation covers HTTP
// headers, NATS headers and a WebSocket handshake frame alike.
//
// Returning ErrNoCredential means "nothing was presented". Returning any other
// error means "something was presented and it did not verify" -- the two are
// different, and only the first can be anonymous on a public RPC.
type Authenticator interface {
	Authenticate(ctx context.Context, md map[string]string) (*Principal, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, md map[string]string) (*Principal, error)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, md map[string]string) (*Principal, error) {
	return f(ctx, md)
}

// Authn verifies credentials and puts the principal in the context. It fails
// closed in three ways worth naming:
//
//   - a nil Authenticator denies every call. An unwired authn stage is a
//     wiring bug, not an open door.
//   - an authenticator that returns neither a principal nor an error is
//     treated as having found no credential. "Nothing happened" never means
//     "authenticated".
//   - a request with no credential is denied unless the annotation says
//     public: true. absent != public, so an unannotated RPC still needs one.
//
// The stage does not decide permissions. It records who is calling and leaves
// the decision to authz, which is a separate stage so a deployment can run one
// without the other.
func Authn(cfg Config, a Authenticator) interchange.Stage {
	cache := newAnnotationCache()
	return interchange.Named(StageAuthn, func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			ann := annotationFor(ctx, cache, req)
			if a == nil {
				return nil, interchange.Errorf(interchange.CodeUnauthenticated,
					"%s: authn is installed with no Authenticator", req.Procedure).
					WithReason(ReasonNotWired)
			}

			p, err := a.Authenticate(ctx, req.Metadata.AsMap())
			switch {
			case errors.Is(err, ErrNoCredential), err == nil && p == nil:
				if ann.Present && ann.Public {
					return next(ctx, req)
				}
				return nil, interchange.Errorf(interchange.CodeUnauthenticated,
					"%s: no credential", req.Procedure).WithReason(ReasonUnauthenticated)
			case err != nil:
				return nil, asError(err, interchange.CodeUnauthenticated, ReasonUnauthenticated,
					"%s: credential did not verify", req.Procedure)
			}

			// auth_types is the annotation saying which credential kinds this
			// RPC accepts. A session cookie on a workload-only RPC is a
			// verified caller presenting the wrong kind of proof.
			if ann.Present && !ann.Accepts(p.authType()) {
				return nil, interchange.Errorf(interchange.CodeUnauthenticated,
					"%s: does not accept credentials of kind %s", req.Procedure, p.authType()).
					WithReason(ReasonAuthTypeRejected)
			}
			return next(WithPrincipal(ctx, p), req)
		}
	})
}

// Metadata keys the stock authenticator reads. They are lower case because
// core canonicalises metadata keys, which is what makes a header-based
// transport and an envelope-based one agree.
const (
	MetaAuthorization = "authorization"
	MetaAPIKey        = "x-api-key"
)

// TokenAuthenticator verifies bearer tokens and API keys against a static
// table. It is deliberately the simplest thing that resolves a credential to a
// principal: a real deployment verifies a JWT signature or calls an identity
// service, and does it behind this same interface.
type TokenAuthenticator struct {
	// digests maps sha256(token) to the principal it resolves to. Tokens are
	// hashed so a heap dump of a running server does not hand out
	// credentials.
	digests map[string]*Principal
}

// NewTokenAuthenticator builds an authenticator from token -> principal. The
// principal's AuthType should say which kind of credential the token is, since
// that is what the annotation's auth_types list is checked against.
func NewTokenAuthenticator(tokens map[string]*Principal) *TokenAuthenticator {
	a := &TokenAuthenticator{digests: make(map[string]*Principal, len(tokens))}
	for token, p := range tokens {
		a.digests[digest(token)] = p
	}
	return a
}

func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Authenticate implements Authenticator.
func (a *TokenAuthenticator) Authenticate(_ context.Context, md map[string]string) (*Principal, error) {
	token, kind := credentialFrom(md)
	if token == "" {
		return nil, ErrNoCredential
	}
	want := digest(token)
	for have, p := range a.digests {
		// Constant-time on the digest: a map lookup would leak nothing an
		// attacker can use, but the comparison is the one place where being
		// careless is free to avoid.
		if subtle.ConstantTimeCompare([]byte(have), []byte(want)) == 1 {
			out := *p
			if out.AuthType == authv1.AuthType_AUTH_TYPE_UNSPECIFIED {
				out.AuthType = kind
			}
			return &out, nil
		}
	}
	return nil, interchange.Errorf(interchange.CodeUnauthenticated, "unknown credential").
		WithReason(ReasonUnauthenticated)
}

// credentialFrom pulls a token out of metadata and reports which kind of
// credential it was presented as.
func credentialFrom(md map[string]string) (string, AuthType) {
	for k, v := range md {
		switch strings.ToLower(k) {
		case MetaAuthorization:
			if token, ok := bearer(v); ok {
				return token, authv1.AuthType_AUTH_TYPE_SESSION
			}
		case MetaAPIKey:
			if v != "" {
				return v, authv1.AuthType_AUTH_TYPE_API_KEY
			}
		}
	}
	return "", authv1.AuthType_AUTH_TYPE_UNSPECIFIED
}

func bearer(v string) (string, bool) {
	scheme, token, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") || token == "" {
		return "", false
	}
	return token, true
}
