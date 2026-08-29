package auth

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/darmawan01/interchange"
	"google.golang.org/protobuf/proto"
)

// RBAC is the in-the-box Authorizer: a role holds a set of permission atoms,
// a principal holds roles, and an RPC needs the atom its annotation declares.
//
// It is one implementation of Authorizer, not the interface's reason for
// existing. Swapping it for OPA, Cedar or a bespoke permission service is
// implementing Authorize and passing the result to Authz -- no change to core,
// to the contract, or to any binding.
type RBAC struct {
	// roles maps a role name to the atoms it grants. A grant may be an atom
	// ("providers.read"), a resource wildcard ("providers.*") or "*".
	roles map[string]map[string]struct{}
}

// NewRBAC builds a role table from role -> atoms. Every atom is parsed, so a
// typo in a role table fails at construction rather than at the first request
// that needed it.
func NewRBAC(roles map[string][]string) (*RBAC, error) {
	r := &RBAC{roles: make(map[string]map[string]struct{}, len(roles))}
	for _, role := range slices.Sorted(maps.Keys(roles)) {
		grants := make(map[string]struct{}, len(roles[role]))
		for _, g := range roles[role] {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if err := validGrant(g); err != nil {
				return nil, fmt.Errorf("auth: role %q: %w", role, err)
			}
			grants[g] = struct{}{}
		}
		r.roles[role] = grants
	}
	return r, nil
}

func validGrant(g string) error {
	if g == "*" {
		return nil
	}
	resource, verb, ok := strings.Cut(g, ".")
	if resource == "*" {
		// "*.read" would parse and then match nothing: the grant table is
		// keyed by atom and by "resource.*", never by verb alone. Refusing it
		// beats granting a role something it will never hold.
		return fmt.Errorf("%q wildcards the resource; write \"*\" to grant everything", g)
	}
	if ok && verb == "*" {
		if resource == "" {
			return fmt.Errorf("%q names no resource", g)
		}
		return nil
	}
	_, err := ParseAtom(g)
	return err
}

// Grants lists the atoms a role holds, sorted. `ix describe` and a role
// review both want this.
func (r *RBAC) Grants(role string) []string {
	return slices.Sorted(maps.Keys(r.roles[role]))
}

// Roles lists the configured role names, sorted.
func (r *RBAC) Roles() []string { return slices.Sorted(maps.Keys(r.roles)) }

// Authorize implements Authorizer.
func (r *RBAC) Authorize(ctx context.Context, procedure string, ann Annotation, _ map[string]string, _ proto.Message) error {
	if ann.Public {
		return nil
	}
	atom := ann.Permission.Atom()
	if atom == "" {
		// A permission with a missing half is a typo that would otherwise
		// authorize nothing and everything at once, depending on how the
		// atom was matched. Refuse to guess.
		return interchange.Errorf(interchange.CodePermissionDenied,
			"%s: annotation declares no usable permission (%s)", procedure, ann.Permission).
			WithReason(ReasonAnnotationMissing)
	}
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return interchange.Errorf(interchange.CodeUnauthenticated,
			"%s: requires %s and the call is anonymous", procedure, atom).
			WithReason(ReasonUnauthenticated)
	}
	for _, role := range p.Roles {
		if r.granted(role, ann.Permission) {
			return nil
		}
	}
	return interchange.Errorf(interchange.CodePermissionDenied,
		"%s: %s requires %s", procedure, p.Subject, atom).
		WithReason(ReasonPermissionDenied)
}

func (r *RBAC) granted(role string, p Permission) bool {
	grants, ok := r.roles[role]
	if !ok {
		return false
	}
	if _, ok := grants["*"]; ok {
		return true
	}
	if _, ok := grants[p.Resource+".*"]; ok {
		return true
	}
	_, ok = grants[p.Atom()]
	return ok
}

func init() {
	// provider: rbac in interchange.yaml, with the role table as the
	// provider's options block: one key per role, atoms comma-separated.
	RegisterProvider(ProviderRBAC, func(options map[string]string) (Authorizer, error) {
		roles := make(map[string][]string, len(options))
		for role, atoms := range options {
			roles[role] = strings.Split(atoms, ",")
		}
		return NewRBAC(roles)
	})
}
