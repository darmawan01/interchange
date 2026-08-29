package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/darmawan01/interchange"
)

// StageAuthz is the chain anchor for the permission decision.
const StageAuthz = "authz"

// TenantScoper resolves whether the caller may act in the scope the request
// names. It is optional -- and being optional is exactly why an unwired one
// has to deny: a tenant-scoped RPC that silently skips the check because
// nobody passed a resolver is the failure this rule exists to prevent.
type TenantScoper interface {
	ScopeAllowed(ctx context.Context, scope TenantScope) error
}

// TenantScoperFunc adapts a function to TenantScoper.
type TenantScoperFunc func(ctx context.Context, scope TenantScope) error

// ScopeAllowed implements TenantScoper.
func (f TenantScoperFunc) ScopeAllowed(ctx context.Context, scope TenantScope) error {
	return f(ctx, scope)
}

type authzConfig struct {
	scoper TenantScoper
}

// AuthzOption configures the authz stage.
type AuthzOption func(*authzConfig)

// WithTenantScoper wires the tenant check. Without it, every tenant-scoped
// RPC -- one that is neither public nor platform -- is denied.
func WithTenantScoper(s TenantScoper) AuthzOption {
	return func(c *authzConfig) { c.scoper = s }
}

// PrincipalTenantScoper is the stock tenant check: the request's tenant and
// project must be ones the verified principal may act in.
func PrincipalTenantScoper() TenantScoper {
	return TenantScoperFunc(func(ctx context.Context, scope TenantScope) error {
		p, ok := PrincipalFromContext(ctx)
		if !ok {
			return interchange.Errorf(interchange.CodeUnauthenticated,
				"tenant %q requires a verified principal", scope.TenantID).
				WithReason(ReasonUnauthenticated)
		}
		if scope.TenantID != "" && !p.HasTenant(scope.TenantID) {
			return interchange.Errorf(interchange.CodePermissionDenied,
				"%s may not act in tenant %q", p.Subject, scope.TenantID).
				WithReason(ReasonTenantDenied)
		}
		if scope.ProjectID != "" && len(p.Projects) > 0 && !p.HasProject(scope.ProjectID) {
			return interchange.Errorf(interchange.CodePermissionDenied,
				"%s may not act in project %q", p.Subject, scope.ProjectID).
				WithReason(ReasonTenantDenied)
		}
		return nil
	})
}

// Authz is the runtime half of "enforce twice": it reads the same (auth)
// annotation protoc-gen-authz reads at build time, off the method descriptor
// core carries, and hands it to the Authorizer.
//
// It is an ordinary interceptor. Core does not know it exists, gives it no
// privileged access, and runs it wherever the chain runs -- which is what
// makes the check identical on HTTP and on a bus rather than merely intended
// to be.
//
// The order of the decisions is the module's policy, and each step fails
// closed:
//
//  1. no method descriptor in the context -- dispatch did not come through a
//     registry, so nothing can be read. Denied.
//  2. no annotation -- Config.OnMissingAnnotation decides: deny (default),
//     warn and allow, or allow. absent != public.
//  3. public: true -- allowed, with no authorizer consulted.
//  4. no Authorizer -- denied. A wiring bug.
//  5. tenant-scoped with no TenantScoper -- denied. Also a wiring bug.
//  6. the Authorizer's decision.
func Authz(cfg Config, az Authorizer, opts ...AuthzOption) interchange.Stage {
	var ac authzConfig
	for _, o := range opts {
		o(&ac)
	}
	cache := newAnnotationCache()
	log := cfg.logger()
	strictness := cfg.Strictness()

	return interchange.Named(StageAuthz, func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			method, ok := interchange.MethodFromContext(ctx)
			if !ok {
				return nil, interchange.Errorf(interchange.CodePermissionDenied,
					"%s: no method descriptor in context; authz cannot read its annotation", req.Procedure).
					WithReason(ReasonNotWired)
			}
			ann := cache.get(req.Procedure, method.Desc)

			if !ann.Present {
				switch strictness {
				case StrictWarn:
					log.WarnContext(ctx, "auth: no (auth) annotation; allowed by policy",
						slog.String("procedure", req.Procedure),
						slog.String("on_missing_annotation", string(StrictWarn)))
					return next(ctx, req)
				case StrictIgnore:
					return next(ctx, req)
				default:
					return nil, interchange.Errorf(interchange.CodePermissionDenied,
						"%s: no (auth) annotation; a public RPC must say public: true", req.Procedure).
						WithReason(ReasonAnnotationMissing)
				}
			}

			if ann.Public {
				return next(ctx, req)
			}

			if az == nil {
				return nil, interchange.Errorf(interchange.CodePermissionDenied,
					"%s: authz is installed with no Authorizer", req.Procedure).
					WithReason(ReasonNotWired)
			}

			if ann.TenantScoped() {
				if ac.scoper == nil {
					return nil, interchange.Errorf(interchange.CodePermissionDenied,
						"%s: tenant-scoped RPC and no TenantScoper is wired", req.Procedure).
						WithReason(ReasonNotWired)
				}
				scope, err := TenantScopeOf(req.Msg)
				if err != nil {
					return nil, asError(err, interchange.CodePermissionDenied, ReasonTenantMissing,
						"%s: tenant scope", req.Procedure)
				}
				if scope.IsZero() {
					return nil, interchange.Errorf(interchange.CodePermissionDenied,
						"%s: request names no tenant; mark the RPC platform: true if it is cross-tenant", req.Procedure).
						WithReason(ReasonTenantMissing)
				}
				if err := ac.scoper.ScopeAllowed(ctx, scope); err != nil {
					return nil, asError(err, interchange.CodePermissionDenied, ReasonTenantDenied,
						"%s: tenant %q", req.Procedure, scope.TenantID)
				}
			}

			if err := az.Authorize(ctx, req.Procedure, ann, req.Metadata.AsMap(), req.Msg); err != nil {
				return nil, asError(err, interchange.CodePermissionDenied, ReasonPermissionDenied,
					"%s: denied", req.Procedure)
			}
			return next(ctx, req)
		}
	})
}

// annotationFor decodes the annotation for the method being dispatched, or the
// absent annotation when there is no method in the context.
func annotationFor(ctx context.Context, cache *annotationCache, req *interchange.Envelope) Annotation {
	method, ok := interchange.MethodFromContext(ctx)
	if !ok {
		return Annotation{}
	}
	return cache.get(req.Procedure, method.Desc)
}

// asError keeps an *interchange.Error's own code and reason -- an authorizer
// that says "not found" to avoid leaking existence should be believed -- and
// gives anything else this module's code and reason so a client can still
// branch on it.
func asError(err error, code interchange.Code, reason string, format string, args ...any) error {
	var ie *interchange.Error
	if errors.As(err, &ie) {
		return err
	}
	return interchange.WrapError(code, fmt.Errorf(format+": %w", append(args, err)...)).
		WithReason(reason)
}
