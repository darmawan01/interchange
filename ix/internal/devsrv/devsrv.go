// Package devsrv is the local loopback behind `ix dev`.
//
// It exercises the CONTRACT, not your business logic. There are no compiled
// handlers, so every RPC is served by a stub that returns a default-valued
// response built from the descriptor by reflection. What that proves is that
// the contract dispatches: the procedure resolves, the request decodes, the
// envelope makes the round trip through the real engine and the real chain,
// and the response shape is what the descriptor says it is.
//
// It runs over driver/memory -- a real driver, not a mock, implementing the
// same six methods and declaring Capabilities the same way. So the loopback
// is the same machinery a broker would drive, minus the broker.
package devsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/darmawan01/interchange/ix/internal/annot"
	"github.com/darmawan01/interchange/ix/internal/image"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// stub is the implementation every dev service is registered with. It holds
// nothing: the response comes from the descriptor.
type stub struct{}

// Loopback is a running dev server and a client pointed at it.
type Loopback struct {
	Registry *interchange.Registry
	Server   *engine.Server
	Client   *engine.Client

	bus *memory.Bus
}

// Procedures lists what the loopback serves, sorted.
func (l *Loopback) Procedures() []string { return l.Registry.Procedures() }

// Plan is what the server subscribed.
func (l *Loopback) Plan() []engine.Subscription { return l.Server.Plan() }

// Close stops the server and the client.
func (l *Loopback) Close() error {
	err := l.Server.Stop()
	if cerr := l.Client.Close(); err == nil {
		err = cerr
	}
	return err
}

// Start builds a registry from the image and runs it over an in-process bus.
//
// Every non-internal RPC is served, whatever roads it declares. The loopback
// is not a routing simulator -- an RPC that travels only rpc still has a
// shape worth exercising, and pretending otherwise would make `ix dev`
// useless on the majority of contracts.
func Start(ctx context.Context, im *image.Image, local func(string) bool) (*Loopback, error) {
	reg := interchange.NewRegistry()
	n := 0
	for _, sd := range im.Services(local) {
		desc, err := serviceDesc(sd)
		if err != nil {
			return nil, err
		}
		if len(desc.Methods) == 0 {
			continue
		}
		if err := reg.Register(desc, stub{}, nil); err != nil {
			return nil, err
		}
		n += len(desc.Methods)
	}
	if n == 0 {
		return nil, fmt.Errorf("the contract declares no RPCs to serve")
	}

	bus := memory.New()
	srv := engine.NewServer(bus.Driver("dev-server"), reg, engine.Expose(transportv1.Transport_TRANSPORT_BUS))
	if err := srv.Start(ctx); err != nil {
		return nil, err
	}
	cli, err := engine.NewClient(ctx, bus.Driver("dev-client"), engine.WithCodec(interchange.CodecJSON))
	if err != nil {
		srv.Stop()
		return nil, err
	}
	return &Loopback{Registry: reg, Server: srv, Client: cli, bus: bus}, nil
}

// Call invokes a procedure with a JSON request and returns the JSON
// response.
func (l *Loopback) Call(ctx context.Context, procedure string, reqJSON []byte) ([]byte, error) {
	md, ok := l.Registry.Method(procedure)
	if !ok {
		return nil, fmt.Errorf("unknown procedure %q", procedure)
	}
	req := md.NewRequest()
	if len(reqJSON) > 0 {
		if err := interchange.JSONCodec().Unmarshal(reqJSON, req); err != nil {
			return nil, fmt.Errorf("decoding the request: %w", err)
		}
	}
	resp := md.NewResponse()
	if err := l.Client.Invoke(ctx, procedure, req, resp); err != nil {
		return nil, err
	}
	// Defaults are emitted, unlike the wire codec: the response IS all
	// defaults, and printing "{}" would hide the shape that is the only thing
	// the loopback has to show. The indenting goes through encoding/json
	// because protojson deliberately varies its own whitespace.
	b, err := protojson.MarshalOptions{EmitDefaultValues: true}.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		return b, nil
	}
	return pretty.Bytes(), nil
}

// serviceDesc builds the ServiceDesc that generated code would emit, from
// the descriptor instead. dynamicpb is what makes that possible: a message
// type conjured from a descriptor at run time behaves like a generated one
// everywhere the engine touches it.
func serviceDesc(sd protoreflect.ServiceDescriptor) (*interchange.ServiceDesc, error) {
	out := &interchange.ServiceDesc{Name: string(sd.FullName()), Desc: sd}
	ms := sd.Methods()
	for i := 0; i < ms.Len(); i++ {
		md := ms.Get(i)
		a := annot.ForMethod(md, nil)
		if a.Internal {
			// (internal) means every public binding skips it. The loopback is
			// a public binding for this purpose: an RPC nobody outside the
			// mesh may call is not one you exercise from a CLI.
			continue
		}
		if md.IsStreamingClient() || md.IsStreamingServer() {
			continue
		}
		in, outMsg := md.Input(), md.Output()
		out.Methods = append(out.Methods, interchange.MethodDesc{
			Name:        string(md.Name()),
			Service:     string(sd.FullName()),
			Procedure:   "/" + string(sd.FullName()) + "/" + string(md.Name()),
			NewRequest:  func() proto.Message { return dynamicpb.NewMessage(in) },
			NewResponse: func() proto.Message { return dynamicpb.NewMessage(outMsg) },
			Handler: func(ctx context.Context, impl any, req proto.Message) (proto.Message, error) {
				return dynamicpb.NewMessage(outMsg), nil
			},
			// The loopback runs one road. See Start.
			Transports: []transportv1.Transport{transportv1.Transport_TRANSPORT_BUS},
			Group:      a.Group,
			Idempotent: a.Idempotent,
			Desc:       md,
		})
	}
	sort.Slice(out.Methods, func(i, j int) bool { return out.Methods[i].Name < out.Methods[j].Name })
	return out, nil
}
