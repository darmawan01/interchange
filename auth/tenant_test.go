package auth_test

import (
	"context"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
)

// TestTenantScopeByConvention: no annotation on the message, fields named
// tenant_id and project_id, found by reflection.
func TestTenantScopeByConvention(t *testing.T) {
	msg := request(procPing, map[string]string{"tenant_id": "acme", "project_id": "catalog"})
	scope, err := auth.TenantScopeOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	if scope.TenantID != "acme" || scope.ProjectID != "catalog" {
		t.Fatalf("scope is %+v", scope)
	}
	if scope.TenantField != "tenant_id" || scope.ProjectField != "project_id" {
		t.Fatalf("scope should record where it looked: %+v", scope)
	}
}

// TestTenantScopeByAnnotation: the same interceptor, a message that calls its
// tenant org_id, and the (tenant_id_field) option pointing at it.
func TestTenantScopeByAnnotation(t *testing.T) {
	msg := request(procAlias, map[string]string{"org_id": "acme", "workspace_id": "catalog"})
	scope, err := auth.TenantScopeOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	if scope.TenantID != "acme" || scope.ProjectID != "catalog" {
		t.Fatalf("scope is %+v", scope)
	}
	if scope.TenantField != "org_id" || scope.ProjectField != "workspace_id" {
		t.Fatalf("the annotated fields should be the ones reported: %+v", scope)
	}
}

// TestTenantScopeIsEnforcedOnBothShapes: the annotated message reaches the same
// resolver with the same value, which is the point -- the field name is a
// message's business, not the interceptor's.
func TestTenantScopeIsEnforcedOnBothShapes(t *testing.T) {
	c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(auth.PrincipalTenantScoper()))

	if _, err := dispatch(t, c, procAlias, request(procAlias, map[string]string{"org_id": "acme"}), bearer(tokenReader)); err != nil {
		t.Fatalf("the annotated tenant field must satisfy the scope check: %v", err)
	}
	_, err := dispatch(t, c, procAlias, request(procAlias, map[string]string{"org_id": "globex"}), bearer(tokenReader))
	assertDenied(t, err, interchange.CodePermissionDenied, auth.ReasonTenantDenied)
}

// TestTenantScopeOfAMessageWithNoTenant: a message that names no tenant is not
// an error here -- it is an empty scope, and the interceptor decides what that
// means for the RPC in question.
func TestTenantScopeOfAMessageWithNoTenant(t *testing.T) {
	scope, err := auth.TenantScopeOf(&commonv1.Problem{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !scope.IsZero() {
		t.Fatalf("scope is %+v", scope)
	}
	if scope, err = auth.TenantScopeOf(nil); err != nil || !scope.IsZero() {
		t.Fatalf("a nil message is an empty scope, got %+v %v", scope, err)
	}
}

// TestTenantScoperIsReplaceable: the tenant check is an interface too, so a
// deployment that resolves tenancy from a directory service wires that in
// instead of the stock principal check.
func TestTenantScoperIsReplaceable(t *testing.T) {
	var seen auth.TenantScope
	scoper := auth.TenantScoperFunc(func(_ context.Context, scope auth.TenantScope) error {
		seen = scope
		if scope.TenantID == "acme" {
			return nil
		}
		return interchange.Errorf(interchange.CodePermissionDenied, "tenant %q is not served here", scope.TenantID).
			WithReason(auth.ReasonTenantDenied)
	})
	c := chain(t, auth.Config{}, rbac(t), auth.WithTenantScoper(scoper))

	if _, err := dispatch(t, c, procPing, request(procPing, map[string]string{"tenant_id": "acme"}), bearer(tokenNoTenant)); err != nil {
		t.Fatalf("the replacement scoper allowed this call: %v", err)
	}
	if seen.TenantID != "acme" || seen.TenantField != "tenant_id" {
		t.Fatalf("the scoper saw %+v", seen)
	}
}
