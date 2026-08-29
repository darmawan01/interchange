// Package catalog is the worked example: one contract, one handler, every
// road.
//
// Two files are written by hand -- api/catalog/v1/catalog.proto and this one.
// Everything else under gen/ is generated from the first, and wire.go is the
// composition root that puts them together. The handler below implements the
// generated CatalogServiceHandler interface and nothing else: no transport
// type appears in a signature, no envelope, no metadata, no knowledge that a
// message bus exists. That is the property the acceptance tests exist to make
// falsifiable.
package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/darmawan01/interchange/errors"
	catalogv1 "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reasons this service raises. They are Go constants here because the example
// contract carries no reason enum of its own; a real service declares them in
// an enum in its own .proto and installs it with errors.EnumSet, so a
// TypeScript client can enumerate them too (see errors/README.md).
const (
	ReasonProviderNotFound      = "PROVIDER_NOT_FOUND"
	ReasonProviderAlreadyExists = "PROVIDER_ALREADY_EXISTS"
	ReasonDisplayNameRequired   = "DISPLAY_NAME_REQUIRED"
)

// Server is the catalog service, backed by an in-memory store.
type Server struct {
	mu    sync.RWMutex
	byID  map[string]*catalogv1.Provider
	seq   int
	now   func() time.Time
	minID func(n int) string
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces time.Now. A handler that reads the wall clock directly is
// a handler whose responses cannot be compared byte for byte across two roads,
// which is exactly what the acceptance tests do.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// NewServer returns an empty catalog.
func NewServer(opts ...Option) *Server {
	s := &Server{
		byID: map[string]*catalogv1.Provider{},
		now:  time.Now,
		// Ids are strings and minted here rather than by the store's index,
		// so a client can never infer how many rows exist. A real service
		// mints a ULID; the example stays deterministic on purpose.
		minID: func(n int) string { return fmt.Sprintf("prov_%08d", n) },
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Seed adds providers at start-up. It is the only way in besides the RPCs, so
// a test fixture and the running service exercise the same store.
func (s *Server) Seed(tenantID string, displayNames ...string) []*catalogv1.Provider {
	out := make([]*catalogv1.Provider, 0, len(displayNames))
	for _, name := range displayNames {
		p, err := s.insert(tenantID, name)
		if err != nil {
			panic(err)
		}
		out = append(out, p)
	}
	return out
}

// ListProviders returns every provider registered to a tenant.
func (s *Server) ListProviders(_ context.Context, req *catalogv1.ListProvidersRequest) (*catalogv1.ListProvidersResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*catalogv1.Provider
	for _, p := range s.byID {
		if p.GetTenantId() == req.GetTenantId() {
			out = append(out, proto.Clone(p).(*catalogv1.Provider))
		}
	}
	// Sorted because two roads compare their responses for equality, and an
	// iteration order that depends on the map is not a response, it is a
	// coin toss.
	slices.SortFunc(out, func(a, b *catalogv1.Provider) int {
		return strings.Compare(a.GetProviderId(), b.GetProviderId())
	})
	return &catalogv1.ListProvidersResponse{Providers: out}, nil
}

// GetProvider returns one provider by id.
func (s *Server) GetProvider(_ context.Context, req *catalogv1.GetProviderRequest) (*catalogv1.GetProviderResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.byID[req.GetProviderId()]
	if !ok || p.GetTenantId() != req.GetTenantId() {
		// The reason is what a client branches on; the message is for a
		// human. A tenant mismatch answers "not found" rather than "denied"
		// so the id space of another tenant is not enumerable.
		return nil, errors.NotFound(ReasonProviderNotFound, "no provider %q in tenant %q",
			req.GetProviderId(), req.GetTenantId())
	}
	return &catalogv1.GetProviderResponse{Provider: proto.Clone(p).(*catalogv1.Provider)}, nil
}

// CreateProvider registers a provider.
func (s *Server) CreateProvider(_ context.Context, req *catalogv1.CreateProviderRequest) (*catalogv1.CreateProviderResponse, error) {
	p, err := s.insert(req.GetTenantId(), req.GetDisplayName())
	if err != nil {
		return nil, err
	}
	return &catalogv1.CreateProviderResponse{Provider: proto.Clone(p).(*catalogv1.Provider)}, nil
}

// SyncProvider schedules a sync. It is service-to-service work: the
// (transports) annotation puts it on the bus and nowhere else, and no code
// here says so.
func (s *Server) SyncProvider(_ context.Context, req *catalogv1.SyncProviderRequest) (*catalogv1.SyncProviderResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.byID[req.GetProviderId()]
	if !ok || p.GetTenantId() != req.GetTenantId() {
		return nil, errors.NotFound(ReasonProviderNotFound, "no provider %q in tenant %q",
			req.GetProviderId(), req.GetTenantId())
	}
	return &catalogv1.SyncProviderResponse{JobId: "job_" + p.GetProviderId()}, nil
}

// Reconcile is cross-tenant maintenance: the request carries no tenant, which
// is what platform: true in the annotation declares.
func (s *Server) Reconcile(_ context.Context, _ *catalogv1.ReconcileRequest) (*catalogv1.ReconcileResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &catalogv1.ReconcileResponse{Reconciled: int32(len(s.byID))}, nil
}

func (s *Server) insert(tenantID, displayName string) (*catalogv1.Provider, error) {
	if displayName == "" {
		return nil, errors.InvalidArgument(ReasonDisplayNameRequired, "display_name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range s.byID {
		if p.GetTenantId() == tenantID && p.GetDisplayName() == displayName {
			return nil, errors.AlreadyExists(ReasonProviderAlreadyExists,
				"tenant %q already has a provider named %q", tenantID, displayName)
		}
	}
	s.seq++
	p := &catalogv1.Provider{
		ProviderId:  s.minID(s.seq),
		TenantId:    tenantID,
		DisplayName: displayName,
		Status:      catalogv1.ProviderStatus_PROVIDER_STATUS_ACTIVE,
		CreatedAt:   timestamppb.New(s.now()),
	}
	s.byID[p.ProviderId] = p
	return p, nil
}
