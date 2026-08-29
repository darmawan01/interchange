// Package rest is the partner-facing REST surface: RESTful URIs, snake_case
// JSON and problem+json errors, transcoded in process onto the same dispatch
// the Connect binding uses.
//
// There is no second contract. A route exists because an RPC carries a
// google.api.http annotation and declares TRANSPORT_REST; the request is
// bound onto the same request message, decoded once, and handed to
// Registry.Dispatch. Like every binding, this one holds no chain -- the
// interceptors that run on a REST call are the ones the service registered,
// in the order it registered them, because there is nowhere else for them to
// come from.
package rest

import (
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/vanguard"
	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Binding serves a registry as REST. One Binding is one http.Handler; mount
// it beside the Connect binding, on its own listener or under its own prefix.
type Binding struct {
	reg     *interchange.Registry
	rpc     *rpc.Binding
	expose  transportv1.Transport
	problem ProblemWriter
	opts    []connect.HandlerOption

	svcs       []*vanguard.Service
	mounted    map[string]bool
	transcoder *vanguard.Transcoder
}

// Option configures a Binding.
type Option func(*Binding)

// Expose overrides which road's procedures this binding serves. The default
// is TRANSPORT_REST.
func Expose(t transportv1.Transport) Option {
	return func(b *Binding) { b.expose = t }
}

// WithProblemWriter replaces the error projection. The default is the RFC
// 9457 projection from the optional /errors module; a service with its own
// error taxonomy substitutes its own here rather than inheriting ours.
func WithProblemWriter(w ProblemWriter) Option {
	return func(b *Binding) { b.problem = w }
}

// WithHandlerOptions passes options through to the connect-go handlers the
// transcoder dispatches into.
func WithHandlerOptions(opts ...connect.HandlerOption) Option {
	return func(b *Binding) { b.opts = append(b.opts, opts...) }
}

// New returns a binding serving reg.
func New(reg *interchange.Registry, opts ...Option) *Binding {
	b := &Binding{
		reg:     reg,
		expose:  transportv1.Transport_TRANSPORT_REST,
		problem: DefaultProblemWriter,
		mounted: map[string]bool{},
	}
	for _, o := range opts {
		o(b)
	}
	// The transcoder needs a Connect handler to dispatch into, and this is
	// it: the same binding, filtered to the same road, so a REST call and an
	// RPC call reach the handler by the same path through the same registry.
	rpcOpts := []rpc.Option{rpc.Expose(b.expose)}
	rpcOpts = append(rpcOpts, rpc.WithHandlerOptions(append([]connect.HandlerOption{
		connect.WithInterceptors(captureInterceptor()),
	}, b.opts...)...))
	b.rpc = rpc.New(reg, rpcOpts...)
	return b
}

// Register binds a service implementation behind a chain and mounts its REST
// routes. The registry rejects a duplicate, so serving one service over both
// HTTP roads is done by registering once and mounting on each binding.
func (b *Binding) Register(sd *interchange.ServiceDesc, impl any, chain *interchange.ChainSpec) error {
	if err := b.reg.Register(sd, impl, chain); err != nil {
		return err
	}
	return b.Mount(sd)
}

// Mount exposes a service that is already registered.
//
// It fails rather than degrades. A service with no descriptor cannot be
// transcoded at all; a method that declares the REST road but carries no
// google.api.http rule has no URI to be reached at, and silently dropping it
// would mean the annotation said one thing and the surface did another.
func (b *Binding) Mount(sd *interchange.ServiceDesc) error {
	if sd == nil {
		return fmt.Errorf("rest: Mount called with a nil ServiceDesc")
	}
	if b.mounted[sd.Name] {
		return fmt.Errorf("rest: service %s is already mounted", sd.Name)
	}
	if sd.Desc == nil {
		return fmt.Errorf("rest: service %s carries no descriptor: the REST surface is transcoded from "+
			"the google.api.http annotations on the method descriptors, so there is nothing to route on", sd.Name)
	}

	schema, n, err := restSchema(sd, b.expose)
	if err != nil {
		return err
	}
	if n == 0 {
		// Registered, but nothing on this road. Not an error: a bus-only
		// service alongside a REST one is the normal case.
		b.mounted[sd.Name] = true
		return nil
	}

	svcs := append(b.svcs[:len(b.svcs):len(b.svcs)], vanguard.NewServiceWithSchema(schema, b.rpc.Handler(),
		// The target is a Connect handler speaking binary proto. Only the
		// client side of the transcoder is REST, which is what keeps the
		// casing decision below scoped to the partner-facing surface.
		vanguard.WithTargetProtocols(vanguard.ProtocolConnect),
		vanguard.WithTargetCodecs(vanguard.CodecProto),
	))
	transcoder, err := vanguard.NewTranscoder(svcs, vanguard.WithCodec(restJSONCodec))
	if err != nil {
		return fmt.Errorf("rest: mounting %s: %w", sd.Name, err)
	}
	if err := b.rpc.Mount(sd); err != nil {
		return err
	}

	b.svcs = svcs
	b.transcoder = transcoder
	b.mounted[sd.Name] = true
	return nil
}

// Handler returns the http.Handler serving every mounted route.
func (b *Binding) Handler() http.Handler { return http.HandlerFunc(b.ServeHTTP) }

// ServeHTTP implements http.Handler.
func (b *Binding) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if b.transcoder == nil {
		b.problem(w, r, Failure{Status: http.StatusNotFound, Detail: "no REST routes are mounted"})
		return
	}
	b.serve(w, r, b.transcoder)
}

// restJSONCodec is §08's casing decision, made executable: camelCase on the
// RPC surface, snake_case on REST. The two surfaces are different audiences
// -- an SDK generated from the contract, and a partner reading a URI in a
// browser -- and pretending they are one is how a service ends up with a
// casing nobody chose.
//
// Unmarshalling needs no switch: protojson accepts a field under both its
// proto name and its JSON name, so a client already sending camelCase keeps
// working.
func restJSONCodec(res vanguard.TypeResolver) vanguard.Codec {
	return restCodec{vanguard.JSONCodec{
		MarshalOptions: protojson.MarshalOptions{
			Resolver:      res,
			UseProtoNames: true,
			// Vanguard's default, kept: a partner parsing a response should
			// not have to distinguish "absent" from "zero" on a field the
			// contract says is always there.
			EmitUnpopulated: true,
		},
		UnmarshalOptions: protojson.UnmarshalOptions{Resolver: res, DiscardUnknown: true},
	}}
}

// restCodec says whose fault a bad body is. Vanguard reports a decode failure
// as an unclassified error, which lands a partner's malformed JSON on a 500;
// the codec is the one place that knows the bytes came from the client, so it
// answers in the vocabulary vanguard already reads.
type restCodec struct{ vanguard.JSONCodec }

func (c restCodec) Unmarshal(data []byte, msg proto.Message) error {
	if err := c.JSONCodec.Unmarshal(data, msg); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

func (c restCodec) UnmarshalField(data []byte, msg proto.Message, field protoreflect.FieldDescriptor) error {
	if err := c.JSONCodec.UnmarshalField(data, msg, field); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}
