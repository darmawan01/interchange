package catalog

import (
	"context"
	"fmt"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/engine"
	"github.com/darmawan01/interchange/errors"
	catalogv1bus "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1/catalogv1bus"
	"github.com/darmawan01/interchange/validate"
)

// Roles is the example's RBAC table. Every atom in it is one the contract
// declares -- protoc-gen-authz was run with known_atoms, so an atom that is
// not in the generated table fails the build rather than granting nothing at
// runtime.
func Roles() (*auth.RBAC, error) {
	return auth.NewRBAC(map[string][]string{
		"reader":   {"providers.read"},
		"writer":   {"providers.read", "providers.create", "providers.edit"},
		"syncer":   {"providers.read", "providers.edit"},
		"platform": {"providers.*"},
	})
}

// Demo credentials. A real deployment verifies a JWT or calls an identity
// service behind the same auth.Authenticator interface; the point of the
// static table is that swapping it changes nothing else.
const (
	TokenReader        = "reader-token"        // session, tenant acme
	TokenWriter        = "writer-token"        // session, tenant acme
	TokenOtherTenant   = "globex-token"        // session, tenant globex
	KeyReadOnlyWorload = "workload-read-key"   // workload, tenant acme, read only
	KeySyncWorkload    = "workload-sync-key"   // workload, tenant acme, may edit
	KeyPlatform        = "workload-plat-key"   // workload, cross-tenant
	KeyBrowserAPI      = "api-key-reader-acme" // api key, tenant acme
)

// Credentials is the stock authenticator's table.
func Credentials() *auth.TokenAuthenticator {
	return auth.NewTokenAuthenticator(map[string]*auth.Principal{
		TokenReader:        {Subject: "user:reader", AuthType: authv1.AuthType_AUTH_TYPE_SESSION, Roles: []string{"reader"}, Tenants: []string{"acme"}},
		TokenWriter:        {Subject: "user:writer", AuthType: authv1.AuthType_AUTH_TYPE_SESSION, Roles: []string{"writer"}, Tenants: []string{"acme"}},
		TokenOtherTenant:   {Subject: "user:globex", AuthType: authv1.AuthType_AUTH_TYPE_SESSION, Roles: []string{"writer"}, Tenants: []string{"globex"}},
		KeyBrowserAPI:      {Subject: "key:acme-dashboard", AuthType: authv1.AuthType_AUTH_TYPE_API_KEY, Roles: []string{"reader"}, Tenants: []string{"acme"}},
		KeyReadOnlyWorload: {Subject: "svc:indexer", AuthType: authv1.AuthType_AUTH_TYPE_WORKLOAD, Roles: []string{"reader"}, Tenants: []string{"acme"}},
		KeySyncWorkload:    {Subject: "svc:syncer", AuthType: authv1.AuthType_AUTH_TYPE_WORKLOAD, Roles: []string{"syncer"}, Tenants: []string{"acme"}},
		// No tenants: Reconcile is platform: true, so it is not tenant-scoped
		// and the scoper is never asked.
		KeyPlatform: {Subject: "svc:reconciler", AuthType: authv1.AuthType_AUTH_TYPE_WORKLOAD, Roles: []string{"platform"}},
	})
}

// Reasons is the closed set of reasons this service may put on the wire: the
// stock taxonomy, plus /auth's denial reasons, plus this service's own.
//
// SetOf rather than EnumSet because the example contract declares no reason
// enum. A real service declares one in its .proto and passes
// errors.EnumSet(catalogv1.CatalogReason(0).Descriptor()) instead, which is
// what lets a TypeScript client enumerate the same list.
func Reasons() errors.Set {
	return errors.SetOf(
		ReasonProviderNotFound,
		ReasonProviderAlreadyExists,
		ReasonDisplayNameRequired,
		auth.ReasonAnnotationMissing,
		auth.ReasonNotWired,
		auth.ReasonTenantMissing,
		auth.ReasonTenantDenied,
		auth.ReasonAuthTypeRejected,
	)
}

type chainConfig struct {
	authn auth.Authenticator
	authz auth.Authorizer
}

// ChainOption configures the composed chain. Both seams exist so a deployment
// can swap the decider without touching core, the contract or any binding --
// and so the acceptance tests can wrap them to record what they were asked.
type ChainOption func(*chainConfig)

// WithAuthenticator replaces the credential verifier.
func WithAuthenticator(a auth.Authenticator) ChainOption {
	return func(c *chainConfig) { c.authn = a }
}

// WithAuthorizer replaces the permission decider.
func WithAuthorizer(a auth.Authorizer) ChainOption {
	return func(c *chainConfig) { c.authz = a }
}

// Chain is the one chain, configured once. Whatever is in it runs identically
// on every road that serves the registry below -- that is core's single
// behavioural invariant, and nothing here has to do anything to get it.
//
// The order, outermost first, and why:
//
//	telemetry  core. One observation per call, labelled by procedure.
//	errors     directly inside telemetry and OUTSIDE recover, per
//	           errors/README.md: everything it normalises has to be below it,
//	           including the internal error recover makes out of a panic. The
//	           task sketch Appends it innermost instead; that placement works
//	           but never sees a panic, so it is not the one used here.
//	recover    core. A panic on a bus takes the subscriber with it.
//	deadline   core. The one thing HTTP gives free and a bus does not.
//	authn      who is calling. Before validate so an anonymous caller is
//	           turned away without spending CPU on their payload.
//	validate   declarative field rules. Before authz because the authz stage
//	           reads tenant_id off the message: a permission decision should
//	           not run against a request that was never checked
//	           (validate/README.md). The example contract declares no
//	           protovalidate rules yet, so today this stage is a no-op -- it
//	           is wired anyway, because the day a rule is added is not the day
//	           anyone should be editing the chain.
//	authz      may they do this, to this tenant.
func Chain(cfg interchange.Config, opts ...ChainOption) (*interchange.ChainSpec, error) {
	c := chainConfig{authn: Credentials()}
	for _, o := range opts {
		o(&c)
	}
	if c.authz == nil {
		roles, err := Roles()
		if err != nil {
			return nil, fmt.Errorf("catalog: roles: %w", err)
		}
		c.authz = roles
	}

	authCfg := auth.Config{
		Provider: auth.ProviderRBAC,
		// The default, spelled out: a method with no (auth) annotation is
		// denied. absent != public.
		OnMissingAnnotation: auth.StrictError,
	}

	chain := interchange.DefaultChain(cfg).
		After(interchange.StageTelemetry, errors.Stage(errors.WithReasons(Reasons()))).
		After(interchange.StageDeadline, auth.Authn(authCfg, c.authn)).
		After(auth.StageAuthn, validate.Stage(nil)).
		Append(auth.Authz(authCfg, c.authz, auth.WithTenantScoper(auth.PrincipalTenantScoper())))
	if err := chain.Err(); err != nil {
		return nil, fmt.Errorf("catalog: chain: %w", err)
	}
	return chain, nil
}

// Service is one composed catalog: one chain, one registry, and however many
// roads are bound to it. Nothing below holds the chain -- every road goes
// through Registry.Dispatch, which is the only place an interceptor runs.
type Service struct {
	Registry *interchange.Registry
	Chain    *interchange.ChainSpec

	// RPC is the Connect-over-HTTP binding. Serve RPC.Handler() wherever you
	// serve HTTP handlers.
	RPC *rpc.Binding

	busses []*engine.Server
}

// Wire is the composition root: register once, mount everywhere.
//
// This is the whole of what an adopter writes to get a service on more than
// one road. The registration is a single call, and every binding constructed
// over the returned registry inherits the same dispatch and the same chain.
func Wire(impl catalogv1bus.CatalogServiceHandler, chain *interchange.ChainSpec) (*Service, error) {
	reg := interchange.NewRegistry()
	if err := catalogv1bus.RegisterCatalogService(reg, impl, chain); err != nil {
		return nil, fmt.Errorf("catalog: register: %w", err)
	}

	binding := rpc.New(reg)
	if err := binding.Mount(catalogv1bus.NewCatalogServiceDesc()); err != nil {
		return nil, fmt.Errorf("catalog: mount rpc: %w", err)
	}

	// A rest.Binding would be mounted here, over the same registry and with
	// no second Register call. binding/rest is still empty as of this commit
	// (module, no Go files), so there is nothing to mount yet -- and the
	// (transports) annotation already names TRANSPORT_REST on four of the
	// five RPCs, so adding it is a mount, not a contract change.

	return &Service{Registry: reg, Chain: chain, RPC: binding}, nil
}

// ServeBus mounts the same registry on a broker. The driver is the only thing
// that differs between an in-process bus and NATS; nothing above this line
// changes, and the engine has no idea which one it was handed.
func (s *Service) ServeBus(ctx context.Context, drv interchange.Driver, opts ...engine.ServerOption) (*engine.Server, error) {
	srv := engine.NewServer(drv, s.Registry, opts...)
	if err := srv.Start(ctx); err != nil {
		return nil, fmt.Errorf("catalog: serve bus: %w", err)
	}
	s.busses = append(s.busses, srv)
	return srv, nil
}

// Close stops every bus server this service started.
func (s *Service) Close() error {
	for _, srv := range s.busses {
		if err := srv.Stop(); err != nil {
			return err
		}
	}
	s.busses = nil
	return nil
}
