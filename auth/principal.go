package auth

import (
	"context"
	"slices"

	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
)

// Principal is who is calling, as authn resolved them. It is this module's
// type: core moves credentials as opaque metadata and has no notion of a user.
type Principal struct {
	// Subject identifies the caller: a user id, a service account, a key id.
	Subject string

	// AuthType is the credential kind that verified. The annotation's
	// auth_types list is checked against it.
	AuthType AuthType

	// Roles are the role names RBAC resolves to permission atoms.
	Roles []string

	// Tenants and Projects are the scopes the principal may act in. The stock
	// tenant resolver checks the request's tenant against these.
	Tenants  []string
	Projects []string

	// Claims carries whatever else the authenticator learned.
	Claims map[string]string
}

// HasRole reports membership.
func (p *Principal) HasRole(role string) bool {
	return p != nil && slices.Contains(p.Roles, role)
}

// HasTenant reports that the principal may act in a tenant.
func (p *Principal) HasTenant(tenant string) bool {
	return p != nil && slices.Contains(p.Tenants, tenant)
}

// HasProject reports that the principal may act in a project.
func (p *Principal) HasProject(project string) bool {
	return p != nil && slices.Contains(p.Projects, project)
}

// Anonymous reports the absence of a verified principal. A nil *Principal is
// anonymous, which is what a public RPC sees.
func (p *Principal) Anonymous() bool { return p == nil || p.Subject == "" }

func (p *Principal) authType() AuthType {
	if p == nil {
		return authv1.AuthType_AUTH_TYPE_UNSPECIFIED
	}
	return p.AuthType
}

type principalKey struct{}

// WithPrincipal puts a verified principal in the context. authn does this; a
// test or a gateway shim may do it directly.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext returns the verified principal. The second result is
// false when the call is anonymous -- which an authorizer must read as "deny"
// unless the RPC is public.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	if !ok || p == nil {
		return nil, false
	}
	return p, true
}
