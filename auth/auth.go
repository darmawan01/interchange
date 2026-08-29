// Package auth is authorization as a module, not as a framework requirement.
//
// Core takes no position on authorization: it does not know what a permission
// is, it never parses the (auth) annotation, and it does not import this
// package -- a CI check asserts the last part. This module imports core and
// gets no privileged access in return. Its interceptors are ordinary
// interchange.Interceptor values, and they read this module's own annotation
// off MethodDesc.Desc, which core hands them through
// interchange.MethodFromContext.
//
// Everything strict here -- a missing annotation is an error, an unwired
// resolver denies -- is a policy of THIS module, declared in its Config. An
// adopter who authorizes at a gateway or runs mTLS-only internal services
// imports none of it and loses nothing.
//
// Three rules the module holds itself to, taken from docs/06:
//
//   - absent != public. A method with no annotation is denied under the
//     default policy; a public method says public: true, so it is greppable.
//   - fail closed on nil. An optional resolver that is unwired denies. An RPC
//     needing a resolver it does not have is a wiring bug, not an open door.
//   - enforce twice. protoc-gen-authz turns the annotation into a build-time
//     gate; the authz interceptor turns the same annotation into the runtime
//     check. One declaration, two enforcement points.
package auth

import (
	"context"
	"fmt"
	"strings"

	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"google.golang.org/protobuf/proto"
)

// AuthType is the credential kind an RPC accepts, as declared by the
// annotation.
type AuthType = authv1.AuthType

// The credential kinds, re-exported so wiring code names them here rather
// than reaching into this module's generated package. The generated names are
// correct and unreadable; a composition root should read as prose.
const (
	AuthTypeUnspecified = authv1.AuthType_AUTH_TYPE_UNSPECIFIED
	AuthTypeAPIKey      = authv1.AuthType_AUTH_TYPE_API_KEY
	AuthTypeSession     = authv1.AuthType_AUTH_TYPE_SESSION
	AuthTypeWorkload    = authv1.AuthType_AUTH_TYPE_WORKLOAD
)

// Verb is the action half of a permission.
type Verb = authv1.Verb

// Permission is the structured permission the annotation carries. It is
// structured rather than a free-form string so a typo cannot mint a phantom
// permission and the generated catalog can group by resource.
type Permission struct {
	Resource string
	Verb     Verb
}

// Atom renders the permission as the string a policy engine matches on:
// {resource: "providers", verb: VERB_READ} is "providers.read". A permission
// missing either half has no atom -- it is a typo, and it renders as "".
func (p Permission) Atom() string {
	if p.Resource == "" || p.Verb == authv1.Verb_VERB_UNSPECIFIED {
		return ""
	}
	return p.Resource + "." + verbName(p.Verb)
}

// IsZero reports an unset permission, which is what an annotation on a public
// RPC carries.
func (p Permission) IsZero() bool {
	return p.Resource == "" && p.Verb == authv1.Verb_VERB_UNSPECIFIED
}

func (p Permission) String() string {
	if a := p.Atom(); a != "" {
		return a
	}
	return fmt.Sprintf("{resource:%q verb:%s}", p.Resource, p.Verb)
}

func verbName(v Verb) string {
	return strings.ToLower(strings.TrimPrefix(v.String(), "VERB_"))
}

// ParseAtom turns "providers.read" back into a Permission. Role tables and the
// plugin's known-atom list are written as atoms, and both have to agree with
// what the annotation renders.
func ParseAtom(s string) (Permission, error) {
	resource, verb, ok := strings.Cut(s, ".")
	if !ok || resource == "" || verb == "" {
		return Permission{}, fmt.Errorf("auth: %q is not a permission atom (want \"resource.verb\")", s)
	}
	v, ok := authv1.Verb_value["VERB_"+strings.ToUpper(verb)]
	if !ok || Verb(v) == authv1.Verb_VERB_UNSPECIFIED {
		return Permission{}, fmt.Errorf("auth: %q names no verb (have %s)", s, strings.Join(Verbs(), ", "))
	}
	return Permission{Resource: resource, Verb: Verb(v)}, nil
}

// Verbs lists the verb names an atom may use, sorted by enum number so the
// list is stable.
func Verbs() []string {
	out := make([]string, 0, len(authv1.Verb_name))
	for n := int32(1); ; n++ {
		name, ok := authv1.Verb_name[n]
		if !ok {
			break
		}
		out = append(out, strings.ToLower(strings.TrimPrefix(name, "VERB_")))
	}
	return out
}

// Annotation is this module's decoded view of the (auth) option. It is the
// module's own type, not the generated one, because Present is the field the
// whole strictness policy turns on and the generated message cannot carry it:
// an absent annotation and an empty one decode to the same zero message.
type Annotation struct {
	// Present reports that the method carries an (auth) option at all.
	// absent != public, so this is not the same question as Public.
	Present bool

	// AuthTypes is the set of credential kinds the RPC accepts. Empty means
	// the annotation named none, which authn treats as "any verified
	// credential".
	AuthTypes []AuthType

	// Permission is the atom the authorizer decides on.
	Permission Permission

	// Public is the explicit opt-out.
	Public bool

	// Platform marks a cross-tenant RPC: the request carries no tenant, so no
	// tenant scope is required of it.
	Platform bool
}

// TenantScoped reports that the RPC is expected to name a tenant -- everything
// that is neither public nor explicitly cross-tenant.
func (a Annotation) TenantScoped() bool { return a.Present && !a.Public && !a.Platform }

// Accepts reports whether a credential kind satisfies the annotation. An
// annotation naming no kinds accepts any verified credential.
func (a Annotation) Accepts(t AuthType) bool {
	if len(a.AuthTypes) == 0 {
		return true
	}
	for _, want := range a.AuthTypes {
		if want == t {
			return true
		}
	}
	return false
}

// Authorizer is the decision. The annotation is the declaration; what it means
// is yours.
//
// This is the seam the module exists to prove: a team with a bespoke
// permission service implements this one method and wires it into the chain,
// without touching core, the contract, or any binding. RBAC ships in the box;
// OPA and Cedar adapters are the obvious next ones and need nothing new here.
//
// Returning nil allows the call. Returning an *interchange.Error carries its
// code and reason to the caller unchanged on every road; any other error is
// reported as permission denied.
type Authorizer interface {
	Authorize(ctx context.Context, procedure string, ann Annotation, md map[string]string, msg proto.Message) error
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, procedure string, ann Annotation, md map[string]string, msg proto.Message) error

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, procedure string, ann Annotation, md map[string]string, msg proto.Message) error {
	return f(ctx, procedure, ann, md, msg)
}

// Machine-readable reasons this module attaches to its denials. A client
// branches on the reason rather than on a message that gets reworded next
// sprint, and the reason is the same string on every road.
const (
	// ReasonAnnotationMissing: the method carries no (auth) annotation and
	// the module's policy is error. absent != public.
	ReasonAnnotationMissing = "AUTHZ_ANNOTATION_MISSING"

	// ReasonNotWired: an authorizer or a resolver the RPC needs is nil. A
	// wiring bug, reported as a denial rather than as an open door.
	ReasonNotWired = "AUTHZ_NOT_WIRED"

	// ReasonPermissionDenied: the principal does not hold the permission.
	ReasonPermissionDenied = "PERMISSION_DENIED"

	// ReasonTenantMissing: a tenant-scoped RPC whose request names no tenant.
	ReasonTenantMissing = "AUTHZ_TENANT_MISSING"

	// ReasonTenantDenied: the principal may not act in the request's tenant.
	ReasonTenantDenied = "AUTHZ_TENANT_DENIED"

	// ReasonUnauthenticated: no credential, or one that did not verify.
	ReasonUnauthenticated = "UNAUTHENTICATED"

	// ReasonAuthTypeRejected: a verified credential of a kind this RPC does
	// not accept.
	ReasonAuthTypeRejected = "AUTH_TYPE_REJECTED"
)
