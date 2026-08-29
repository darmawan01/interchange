package dsl_test

import (
	"context"
	"testing"

	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"github.com/darmawan01/interchange/frontend/dsl"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	cliv1 "github.com/darmawan01/interchange/tools/gen/go/interchange/cli/v1"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// This is the test that proves the claim in §09: a DSL user gets the same
// contract a proto user gets. The descriptors go through a real compiler and
// come back out with the extension values readable by the ordinary generated
// accessors -- the same call the authz interceptor and every binding plugin
// makes.
func TestRoundTripThroughTheToolchain(t *testing.T) {
	set, diags, err := dsl.New().Parse(context.Background(), sources(t, "catalog.ix.yaml", ""), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("diagnostics: %v", diags)
	}

	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatalf("the emitted descriptors do not form a valid registry: %v", err)
	}

	sd := find[protoreflect.ServiceDescriptor](t, files, "catalog.v1.CatalogService")
	svcOpts := normalise(t, sd.Options()).(*descriptorpb.ServiceOptions)
	st := proto.GetExtension(svcOpts, transportv1.E_ServiceTransports).(*transportv1.ServiceTransportOptions)
	if got := st.GetOn(); len(got) != 2 || got[0] != transportv1.Transport_TRANSPORT_RPC || got[1] != transportv1.Transport_TRANSPORT_REST {
		t.Errorf("service_transports on = %v", got)
	}

	list := find[protoreflect.MethodDescriptor](t, files, "catalog.v1.CatalogService.ListProviders")
	mo := normalise(t, list.Options()).(*descriptorpb.MethodOptions)

	tr := proto.GetExtension(mo, transportv1.E_Transports).(*transportv1.TransportOptions)
	want := []transportv1.Transport{
		transportv1.Transport_TRANSPORT_RPC,
		transportv1.Transport_TRANSPORT_REST,
		transportv1.Transport_TRANSPORT_BUS,
	}
	if got := tr.GetOn(); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("transports on = %v, want %v", got, want)
	}
	if tr.GetGroup() != "catalog" {
		t.Errorf("transports group = %q", tr.GetGroup())
	}

	au := proto.GetExtension(mo, authv1.E_Auth).(*authv1.AuthOptions)
	if got := au.GetAuthTypes(); len(got) != 3 || got[0] != authv1.AuthType_AUTH_TYPE_SESSION {
		t.Errorf("auth_types = %v", got)
	}
	if au.GetPermission().GetResource() != "providers" || au.GetPermission().GetVerb() != authv1.Verb_VERB_READ {
		t.Errorf("permission = %v", au.GetPermission())
	}

	cmd := proto.GetExtension(mo, cliv1.E_Command).(*cliv1.CommandOptions)
	if got := cmd.GetPath(); len(got) != 2 || got[0] != "catalog" || got[1] != "providers" {
		t.Errorf("cli path = %v", got)
	}

	rule := proto.GetExtension(mo, annotations.E_Http).(*annotations.HttpRule)
	if rule.GetGet() != "/v1/catalog/providers" {
		t.Errorf("http rule = %v", rule)
	}
	if mo.GetIdempotencyLevel() != descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
		t.Errorf("idempotency_level = %v", mo.GetIdempotencyLevel())
	}

	// Reconcile carries the two annotations that keep an RPC off every public
	// road -- the ones a reviewer most needs to see survive the transform.
	rec := find[protoreflect.MethodDescriptor](t, files, "catalog.v1.CatalogService.Reconcile")
	ro := normalise(t, rec.Options()).(*descriptorpb.MethodOptions)
	if !proto.GetExtension(ro, transportv1.E_Internal).(bool) {
		t.Error("internal = false on Reconcile")
	}
	if !proto.GetExtension(ro, authv1.E_Auth).(*authv1.AuthOptions).GetPlatform() {
		t.Error("platform = false on Reconcile")
	}
	if !proto.GetExtension(ro, cliv1.E_Command).(*cliv1.CommandOptions).GetSkip() {
		t.Error("cli skip = false on Reconcile")
	}

	// The nested message and its enum came through as real nested types.
	find[protoreflect.MessageDescriptor](t, files, "catalog.v1.Provider.Endpoint")
	find[protoreflect.EnumDescriptor](t, files, "catalog.v1.Provider.Endpoint.Scheme")
}

// The sidecar route has to produce the same descriptors as the inline route,
// or the "universal fallback" is a second-class citizen.
func TestSidecarAnnotationsReachTheDescriptor(t *testing.T) {
	set, _, err := dsl.New().Parse(context.Background(), sources(t, "bare.ix.yaml", "bare.annotations.yaml"), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}
	md := find[protoreflect.MethodDescriptor](t, files, "catalog.v1.PingService.Ping")
	mo := normalise(t, md.Options()).(*descriptorpb.MethodOptions)
	au := proto.GetExtension(mo, authv1.E_Auth).(*authv1.AuthOptions)
	if au.GetPermission().GetResource() != "ping" {
		t.Errorf("permission from sidecar = %v", au.GetPermission())
	}
	if proto.GetExtension(mo, transportv1.E_Transports).(*transportv1.TransportOptions).GetGroup() != "ping" {
		t.Error("transports group from sidecar did not survive")
	}
}

// normalise re-parses an options message against the global type registry.
// A compiler stores extensions it resolved from source as dynamic values;
// this is what turns them back into the generated types a consumer holds.
func normalise(t *testing.T, opts proto.Message) proto.Message {
	t.Helper()
	b, err := proto.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	out := opts.ProtoReflect().New().Interface()
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func find[T protoreflect.Descriptor](t *testing.T, files *protoregistry.Files, name string) T {
	t.Helper()
	d, err := files.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, ok := d.(T)
	if !ok {
		t.Fatalf("%s is a %T", name, d)
	}
	return out
}
