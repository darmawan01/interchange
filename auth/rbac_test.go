package auth_test

import (
	"context"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
)

func TestRBACDecidesByAtom(t *testing.T) {
	r := rbac(t)
	read := auth.Annotation{Present: true, Permission: auth.Permission{Resource: "ping", Verb: authv1.Verb_VERB_READ}}
	edit := auth.Annotation{Present: true, Permission: auth.Permission{Resource: "ping", Verb: authv1.Verb_VERB_EDIT}}
	other := auth.Annotation{Present: true, Permission: auth.Permission{Resource: "providers", Verb: authv1.Verb_VERB_READ}}

	cases := []struct {
		name    string
		roles   []string
		ann     auth.Annotation
		allowed bool
	}{
		{"reader holds ping.read", []string{"reader"}, read, true},
		{"reader does not hold ping.edit", []string{"reader"}, edit, false},
		{"writer holds both", []string{"writer"}, edit, true},
		{"a resource wildcard covers every verb on it", []string{"platform"}, edit, true},
		{"a resource wildcard covers nothing else", []string{"platform"}, other, false},
		{"an unknown role grants nothing", []string{"ghost"}, read, false},
		{"no roles grant nothing", nil, read, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u", Roles: tc.roles})
			err := r.Authorize(ctx, procPing, tc.ann, nil, nil)
			if tc.allowed && err != nil {
				t.Fatalf("expected allow, got %v", err)
			}
			if !tc.allowed {
				assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonPermissionDenied)
			}
		})
	}
}

// TestRBACIsAnonymousSafe: with no principal in the context there is nothing
// to check roles against, and that is a denial rather than a nil dereference.
func TestRBACIsAnonymousSafe(t *testing.T) {
	ann := auth.Annotation{Present: true, Permission: auth.Permission{Resource: "ping", Verb: authv1.Verb_VERB_READ}}
	err := rbac(t).Authorize(context.Background(), procPing, ann, nil, nil)
	assertDenied(t, err, interchange.CodeUnauthenticated, auth.ReasonUnauthenticated)
}

// TestRBACRefusesAHalfPermission: an annotation naming a resource and no verb
// is a typo. Matching it loosely would authorize by accident.
func TestRBACRefusesAHalfPermission(t *testing.T) {
	ann := auth.Annotation{Present: true, Permission: auth.Permission{Resource: "ping"}}
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u", Roles: []string{"platform"}})
	err := rbac(t).Authorize(ctx, procPing, ann, nil, nil)
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonAnnotationMissing)
}

// TestRBACRejectsATypoInTheRoleTable: a role table is configuration, and a
// misspelt atom in it fails at construction rather than at the first request
// that needed it.
func TestRBACRejectsATypoInTheRoleTable(t *testing.T) {
	for _, bad := range []string{"providers.reed", "providers", "providers.", ".read", "*.read"} {
		if _, err := auth.NewRBAC(map[string][]string{"role": {bad}}); err == nil {
			t.Fatalf("role table with %q was accepted", bad)
		}
	}
	r, err := auth.NewRBAC(map[string][]string{"admin": {"*", "providers.*", "providers.read"}})
	if err != nil {
		t.Fatalf("legal grants rejected: %v", err)
	}
	if got := r.Grants("admin"); len(got) != 3 {
		t.Fatalf("grants are %v", got)
	}
	if got := r.Roles(); len(got) != 1 || got[0] != "admin" {
		t.Fatalf("roles are %v", got)
	}
}

func TestPermissionAtoms(t *testing.T) {
	p := auth.Permission{Resource: "providers", Verb: authv1.Verb_VERB_READ}
	if got := p.Atom(); got != "providers.read" {
		t.Fatalf("atom is %q", got)
	}
	round, err := auth.ParseAtom("providers.read")
	if err != nil || round != p {
		t.Fatalf("ParseAtom round trip: %+v %v", round, err)
	}
	if got := (auth.Permission{Resource: "providers"}).Atom(); got != "" {
		t.Fatalf("a verbless permission has no atom, got %q", got)
	}
	if !(auth.Permission{}).IsZero() {
		t.Fatal("the empty permission is zero")
	}
	for _, bad := range []string{"", "providers", "providers.reed", "."} {
		if _, err := auth.ParseAtom(bad); err == nil {
			t.Fatalf("ParseAtom(%q) must fail", bad)
		}
	}
}

// TestProviderRegistry: rbac is reachable by config name, and the providers
// this module does not implement are named extension points rather than
// silent no-ops.
func TestProviderRegistry(t *testing.T) {
	az, err := auth.NewAuthorizer(auth.Config{
		Provider: auth.ProviderRBAC,
		Options:  map[string]string{"reader": "ping.read", "writer": "ping.read,ping.edit"},
	})
	if err != nil {
		t.Fatalf("provider rbac: %v", err)
	}
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u", Roles: []string{"writer"}})
	ann := auth.Annotation{Present: true, Permission: auth.Permission{Resource: "ping", Verb: authv1.Verb_VERB_EDIT}}
	if err := az.Authorize(ctx, procPing, ann, nil, nil); err != nil {
		t.Fatalf("the config-built authorizer denied a granted atom: %v", err)
	}

	if _, err := auth.NewAuthorizer(auth.Config{Provider: auth.ProviderOPA}); err == nil {
		t.Fatal("an unregistered provider must be an error, not an empty authorizer")
	}
	if _, err := auth.NewAuthorizer(auth.Config{Provider: auth.ProviderCustom}); err == nil {
		t.Fatal("a custom provider is constructed in Go, not from config")
	}

	// The seam OPA and Cedar would arrive through: a name and a factory.
	auth.RegisterProvider("test-engine", func(map[string]string) (auth.Authorizer, error) {
		return rbac(t), nil
	})
	if _, err := auth.NewAuthorizer(auth.Config{Provider: "test-engine"}); err != nil {
		t.Fatalf("a registered provider must build: %v", err)
	}
}
