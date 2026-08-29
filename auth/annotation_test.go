package auth_test

import (
	"testing"

	"github.com/darmawan01/interchange/auth"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
)

// TestAnnotationOf reads the module's own option off the descriptor core
// carries. Core never parses this: MethodDesc.Desc is a protoreflect
// descriptor, and what the annotation means is entirely this module's affair.
func TestAnnotationOf(t *testing.T) {
	cases := []struct {
		procedure string
		want      auth.Annotation
	}{
		{procPing, auth.Annotation{
			Present:    true,
			AuthTypes:  []auth.AuthType{authv1.AuthType_AUTH_TYPE_SESSION},
			Permission: auth.Permission{Resource: "ping", Verb: authv1.Verb_VERB_READ},
		}},
		{procPublic, auth.Annotation{Present: true, Public: true}},
		{procPlatform, auth.Annotation{
			Present:    true,
			AuthTypes:  []auth.AuthType{authv1.AuthType_AUTH_TYPE_WORKLOAD},
			Permission: auth.Permission{Resource: "ping", Verb: authv1.Verb_VERB_EDIT},
			Platform:   true,
		}},
		{procUnannotated, auth.Annotation{}},
	}
	for _, tc := range cases {
		t.Run(tc.procedure, func(t *testing.T) {
			got := auth.AnnotationOf(methodDesc(tc.procedure).Desc)
			if got.Present != tc.want.Present || got.Public != tc.want.Public ||
				got.Platform != tc.want.Platform || got.Permission != tc.want.Permission ||
				len(got.AuthTypes) != len(tc.want.AuthTypes) {
				t.Fatalf("decoded %+v, want %+v", got, tc.want)
			}
			for i := range tc.want.AuthTypes {
				if got.AuthTypes[i] != tc.want.AuthTypes[i] {
					t.Fatalf("auth types are %v, want %v", got.AuthTypes, tc.want.AuthTypes)
				}
			}
		})
	}
}

// TestAnnotationOfNilDescriptor: generated code that predates the module, or a
// hand-written ServiceDesc, carries no descriptor. That decodes to absent --
// which the default policy denies rather than crashes on.
func TestAnnotationOfNilDescriptor(t *testing.T) {
	if got := auth.AnnotationOf(nil); got.Present {
		t.Fatalf("a nil descriptor decoded to %+v", got)
	}
}

func TestAnnotationPredicates(t *testing.T) {
	scoped := auth.AnnotationOf(methodDesc(procPing).Desc)
	if !scoped.TenantScoped() {
		t.Fatal("an ordinary annotated RPC is tenant scoped")
	}
	if !scoped.Accepts(authv1.AuthType_AUTH_TYPE_SESSION) || scoped.Accepts(authv1.AuthType_AUTH_TYPE_WORKLOAD) {
		t.Fatalf("auth_types are not being read: %+v", scoped)
	}

	platform := auth.AnnotationOf(methodDesc(procPlatform).Desc)
	if platform.TenantScoped() {
		t.Fatal("platform: true means the request carries no tenant")
	}

	public := auth.AnnotationOf(methodDesc(procPublic).Desc)
	if public.TenantScoped() {
		t.Fatal("a public RPC is not tenant scoped")
	}
	if !public.Accepts(authv1.AuthType_AUTH_TYPE_API_KEY) {
		t.Fatal("an annotation naming no auth types accepts any verified credential")
	}

	var absent auth.Annotation
	if absent.TenantScoped() {
		t.Fatal("an absent annotation declares nothing, including tenancy")
	}
}
