// Package gentest runs the committed golden output. The goldens are Go
// packages rather than blobs, so this is what proves the generators emit code
// that registers, dispatches, and calls -- a byte comparison only proves a
// generator repeated itself.
//
// It is also why the goldens compile: `go build ./...` skips testdata, so
// nothing but an importer would ever type-check them.
package gentest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/darmawan01/interchange/tools/clisupport"
	fixturev1 "github.com/darmawan01/interchange/tools/testdata/gen/interchange/fixture/v1"
	"github.com/darmawan01/interchange/tools/testdata/gen/interchange/fixture/v1/fixturev1bus"
	"github.com/darmawan01/interchange/tools/testdata/gen/interchange/fixture/v1/fixturev1cli"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type impl struct{}

func (impl) GetItem(_ context.Context, in *fixturev1.GetItemRequest) (*fixturev1.GetItemResponse, error) {
	return &fixturev1.GetItemResponse{Id: in.GetId(), Kind: in.GetKind()}, nil
}

func (impl) PublishItem(_ context.Context, in *fixturev1.PublishItemRequest) (*fixturev1.PublishItemResponse, error) {
	return &fixturev1.PublishItemResponse{Id: in.GetId()}, nil
}

func (impl) Reindex(context.Context, *fixturev1.ReindexRequest) (*fixturev1.ReindexResponse, error) {
	return &fixturev1.ReindexResponse{Indexed: 7}, nil
}

func registry(t *testing.T) *interchange.Registry {
	t.Helper()
	reg := interchange.NewRegistry()
	if err := fixturev1bus.RegisterFixtureService(reg, impl{}, nil); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg
}

// TestServiceDescDispatches is the point of the whole plugin: one descriptor,
// registered once, dispatches the handler.
func TestServiceDescDispatches(t *testing.T) {
	reg := registry(t)
	resp, err := reg.Dispatch(context.Background(), &interchange.Envelope{
		Procedure: fixturev1bus.FixtureServiceGetItemProcedure,
		Codec:     interchange.CodecProto,
		Msg:       &fixturev1.GetItemRequest{Id: "42", Kind: fixturev1.Kind_KIND_BOOK},
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := resp.Msg.(*fixturev1.GetItemResponse)
	if !ok {
		t.Fatalf("handler returned %T", resp.Msg)
	}
	if got.GetId() != "42" || got.GetKind() != fixturev1.Kind_KIND_BOOK {
		t.Errorf("round trip lost the request: %v", got)
	}
}

// TestResolvedAnnotationsReachTheRegistry: the annotations are only real if
// they arrive on MethodDesc, where a binding reads them.
func TestResolvedAnnotationsReachTheRegistry(t *testing.T) {
	reg := registry(t)
	for _, tc := range []struct {
		procedure  string
		transports []transportv1.Transport
		group      string
		idempotent bool
		internal   bool
	}{
		{
			procedure:  fixturev1bus.FixtureServiceGetItemProcedure,
			transports: []transportv1.Transport{transportv1.Transport_TRANSPORT_RPC, transportv1.Transport_TRANSPORT_BUS},
			group:      "fixtures",
			idempotent: true,
		},
		{
			procedure:  fixturev1bus.FixtureServicePublishItemProcedure,
			transports: []transportv1.Transport{transportv1.Transport_TRANSPORT_BUS, transportv1.Transport_TRANSPORT_MQTT},
			group:      "publishers",
		},
		{
			procedure:  fixturev1bus.FixtureServiceReindexProcedure,
			transports: []transportv1.Transport{transportv1.Transport_TRANSPORT_RPC, transportv1.Transport_TRANSPORT_BUS},
			group:      "fixtures",
			internal:   true,
		},
	} {
		md, ok := reg.Method(tc.procedure)
		if !ok {
			t.Fatalf("%s is not registered", tc.procedure)
		}
		for _, want := range tc.transports {
			if !md.ExposedOn(want) {
				t.Errorf("%s: not exposed on %s", tc.procedure, want)
			}
		}
		if len(md.Transports) != len(tc.transports) {
			t.Errorf("%s: transports %v, want %v", tc.procedure, md.Transports, tc.transports)
		}
		if md.Group != tc.group {
			t.Errorf("%s: group %q, want %q", tc.procedure, md.Group, tc.group)
		}
		if md.Idempotent != tc.idempotent {
			t.Errorf("%s: idempotent %v, want %v", tc.procedure, md.Idempotent, tc.idempotent)
		}
		if md.Internal != tc.internal {
			t.Errorf("%s: internal %v, want %v", tc.procedure, md.Internal, tc.internal)
		}
		if md.Desc == nil {
			t.Fatalf("%s: no MethodDescriptor; an optional module could not read its own annotation", tc.procedure)
		}
		if got := string(md.Desc.FullName()); !strings.HasSuffix(tc.procedure, string(md.Desc.Name())) || got == "" {
			t.Errorf("%s: descriptor is %s", tc.procedure, got)
		}
	}
}

// The two seams the generated code leans on, asserted where they would break
// first: a binding is a Registrar, and both clients are Invokers, so a
// generated command tree needs no adapter to reach either road.
var (
	_ interchange.Registrar = (*interchange.Registry)(nil)
	_ interchange.Registrar = (*rpc.Binding)(nil)
	_ interchange.Mounter   = (*rpc.Binding)(nil)
	_ clisupport.Invoker    = (*rpc.Client)(nil)
	_ clisupport.Invoker    = (*engine.Client)(nil)
)

// TestRegisterAcceptsAnRPCBinding registers through the interface rather than
// the concrete type, which is the call generated code makes.
func TestRegisterAcceptsAnRPCBinding(t *testing.T) {
	var r interchange.Registrar = rpc.New(interchange.NewRegistry())
	if err := fixturev1bus.RegisterFixtureService(r, impl{}, interchange.Chain()); err != nil {
		t.Fatalf("register on an rpc.Binding: %v", err)
	}
}

// TestBusClient runs the generated client against the engine over the memory
// driver: the same ServiceDesc serves it.
func TestBusClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus := memory.New()
	srv := engine.NewServer(bus.Driver("server"), registry(t))
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	ec, err := engine.NewClient(ctx, bus.Driver("client"))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer ec.Close()

	cl := fixturev1bus.NewFixtureServiceBusClient(ec, fixturev1bus.WithFixtureServiceTimeout(2*time.Second))
	resp, err := cl.GetItem(ctx, &fixturev1.GetItemRequest{Id: "7"})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if resp.GetId() != "7" {
		t.Errorf("GetItem returned %q", resp.GetId())
	}

	// The Within form is the one that puts the network deadline where a
	// caller can see it.
	if _, err := cl.PublishItemWithin(ctx, time.Second, &fixturev1.PublishItemRequest{Id: "9"}); err != nil {
		t.Fatalf("PublishItemWithin: %v", err)
	}
}

type recorder struct {
	procedure string
	req       proto.Message
	resp      proto.Message
}

func (r *recorder) Invoke(_ context.Context, procedure string, in, out proto.Message) error {
	r.procedure = procedure
	r.req = proto.Clone(in)
	if r.resp != nil {
		proto.Merge(out, r.resp)
	}
	return nil
}

// TestCommandTree drives the generated Cobra tree the way a user does.
func TestCommandTree(t *testing.T) {
	rec := &recorder{resp: &fixturev1.GetItemResponse{Id: "42", Kind: fixturev1.Kind_KIND_BOOK}}
	root := &cobra.Command{Use: "ix"}
	fixturev1cli.RegisterFixtureServiceCommands(root, rec)
	fixturev1cli.RegisterExtraServiceCommands(root, rec)
	fixturev1cli.RegisterPlainServiceCommands(root, rec)

	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"fixture", "items", "get", "42",
		"--limit", "3",
		"--kind", "KIND_BOOK",
		"--include-archived",
		"--request-json", `{"tags":["a","b"]}`,
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}

	if rec.procedure != "/interchange.fixture.v1.FixtureService/GetItem" {
		t.Errorf("invoked %q", rec.procedure)
	}
	req, ok := rec.req.(*fixturev1.GetItemRequest)
	if !ok {
		t.Fatalf("request was %T", rec.req)
	}
	if req.GetId() != "42" {
		t.Errorf("positional argument did not reach the request: %q", req.GetId())
	}
	if req.GetLimit() != 3 || !req.GetIncludeArchived() || req.GetKind() != fixturev1.Kind_KIND_BOOK {
		t.Errorf("flags did not reach the request: %v", req)
	}
	// The fields no flag can carry still arrive.
	if len(req.GetTags()) != 2 {
		t.Errorf("--request-json did not reach the request: %v", req.GetTags())
	}
	if !strings.Contains(out.String(), `"id": "42"`) {
		t.Errorf("response was not printed as JSON:\n%s", out.String())
	}

	// Two services mounted under one path prefix share the parent command.
	if n := len(childNames(t, root, "fixture")); n != 2 {
		t.Errorf("`fixture` has %d children, want items and noop", n)
	}
}

func childNames(t *testing.T, root *cobra.Command, name string) []string {
	t.Helper()
	for _, c := range root.Commands() {
		if c.Name() != name {
			continue
		}
		var out []string
		for _, g := range c.Commands() {
			out = append(out, g.Name())
		}
		return out
	}
	t.Fatalf("no %q command", name)
	return nil
}

// TestUnknownEnumValueIsRefused: a typo in an enum flag must name the values
// that exist, not fall back to zero.
func TestUnknownEnumValueIsRefused(t *testing.T) {
	root := &cobra.Command{Use: "ix"}
	fixturev1cli.RegisterFixtureServiceCommands(root, &recorder{})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"fixture", "items", "get", "1", "--kind", "KIND_NOPE"})
	err := root.Execute()
	if err == nil {
		t.Fatal("accepted an unknown enum value")
	}
	if !strings.Contains(err.Error(), "KIND_BOOK") {
		t.Errorf("error should list the valid values, got: %v", err)
	}
}

// TestCoverage: the report is the whole answer to "a CLI covering 80% of the
// RPCs may be worse than none".
func TestCoverage(t *testing.T) {
	if c := fixturev1cli.FixtureServiceCoverage(); !c.Complete() {
		t.Errorf("FixtureService should be fully accounted for: %s", c)
	}
	plain := fixturev1cli.PlainServiceCoverage()
	if plain.Complete() {
		t.Fatal("PlainService has an unannotated RPC and must not report complete")
	}
	if !strings.Contains(plain.String(), "PlainService/Ping") {
		t.Errorf("report must name the hole: %s", plain)
	}
}
