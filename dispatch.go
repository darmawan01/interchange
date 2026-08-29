package interchange

import (
	"context"
	"fmt"
	"slices"
	"sync"

	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// MethodDesc describes one RPC. Generated code emits it; core never builds
// one by hand. Everything a binding or an interceptor needs to know about a
// method is here or reachable from Desc.
type MethodDesc struct {
	// Name is the bare method name, "ListProviders".
	Name string

	// Service is the fully-qualified service name, "catalog.v1.CatalogService".
	Service string

	// Procedure is "/catalog.v1.CatalogService/ListProviders".
	Procedure string

	// NewRequest and NewResponse mint empty messages for decoding.
	NewRequest  func() proto.Message
	NewResponse func() proto.Message

	// Handler calls the method on impl. Generated code supplies the type
	// assertion, which is why nothing below this line needs generics.
	Handler func(ctx context.Context, impl any, req proto.Message) (proto.Message, error)

	// Transports is the resolved fan-out for this method: the per-RPC
	// (transports) annotation if present, the service-level default
	// otherwise, and [RPC, REST] if neither is set.
	Transports []transportv1.Transport

	// Group is the competing-consumer group name, from the annotation.
	Group string

	// Idempotent reports idempotency_level = NO_SIDE_EFFECTS, which lets a
	// Connect client issue a real cacheable GET.
	Idempotent bool

	// Internal reports the (internal) annotation: skipped by every public
	// binding.
	Internal bool

	// Desc is the method descriptor, which is how an optional module reads
	// its own annotation at runtime without core knowing what the annotation
	// means.
	Desc protoreflect.MethodDescriptor
}

// ExposedOn reports whether this method travels a given road.
func (m *MethodDesc) ExposedOn(t transportv1.Transport) bool {
	return slices.Contains(m.Transports, t)
}

// ServiceDesc describes one service. Generated code emits it.
type ServiceDesc struct {
	// Name is the fully-qualified proto service name.
	Name string

	Methods []MethodDesc

	// Desc is the service descriptor.
	Desc protoreflect.ServiceDescriptor
}

type boundMethod struct {
	md    *MethodDesc
	impl  any
	chain *ChainSpec
	call  UnaryFunc
}

// Registry is the dispatch table: procedure to handler, behind the chain.
//
// Every binding dispatches through here and no binding is given the chain
// itself. That is structural chain symmetry: a driver cannot add, skip or
// reorder a stage because it never holds one.
type Registry struct {
	mu      sync.RWMutex
	methods map[string]*boundMethod
	svcs    map[string]*ServiceDesc
	order   []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{methods: map[string]*boundMethod{}, svcs: map[string]*ServiceDesc{}}
}

// Register binds a service implementation behind a chain. Passing a nil chain
// means an empty chain, which is valid.
//
// Registering the same service twice, or two services claiming the same
// procedure, is an error rather than a silent overwrite: a shadowed handler
// is the kind of bug that only shows up in production.
func (r *Registry) Register(sd *ServiceDesc, impl any, chain *ChainSpec) error {
	if sd == nil {
		return fmt.Errorf("interchange: Register called with a nil ServiceDesc")
	}
	if impl == nil {
		return fmt.Errorf("interchange: Register(%s) called with a nil implementation", sd.Name)
	}
	if chain == nil {
		chain = Chain()
	}
	if err := chain.Err(); err != nil {
		return fmt.Errorf("interchange: Register(%s): %w", sd.Name, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.svcs[sd.Name]; dup {
		return fmt.Errorf("interchange: service %s is already registered", sd.Name)
	}
	bound := make([]*boundMethod, 0, len(sd.Methods))
	for i := range sd.Methods {
		md := &sd.Methods[i]
		if _, dup := r.methods[md.Procedure]; dup {
			return fmt.Errorf("interchange: procedure %s is already registered", md.Procedure)
		}
		b := &boundMethod{md: md, impl: impl, chain: chain}
		call, err := chain.Wrap(b.invoke)
		if err != nil {
			return fmt.Errorf("interchange: Register(%s): %w", sd.Name, err)
		}
		b.call = call
		bound = append(bound, b)
	}
	for _, b := range bound {
		r.methods[b.md.Procedure] = b
		r.order = append(r.order, b.md.Procedure)
	}
	r.svcs[sd.Name] = sd
	slices.Sort(r.order)
	return nil
}

// invoke is the innermost step: decode is already done, so this is the
// handler call and nothing else.
func (b *boundMethod) invoke(ctx context.Context, req *Envelope) (*Envelope, error) {
	resp, err := b.md.Handler(ctx, b.impl, req.Msg)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, Errorf(CodeInternal, "%s: handler returned no response and no error", b.md.Procedure)
	}
	return &Envelope{
		Procedure:     req.Procedure,
		Metadata:      Metadata{},
		Codec:         req.Codec,
		CorrelationID: req.CorrelationID,
		Msg:           resp,
	}, nil
}

// Dispatch decodes the request, runs the configured chain, and calls the
// handler. Every binding on every transport enters here.
//
// The returned envelope carries Msg; it does not carry Payload. Encoding is
// the binding's business -- the Connect binding hands the message straight to
// connect-go, and the message engine encodes it with the request's codec.
func (r *Registry) Dispatch(ctx context.Context, req *Envelope) (*Envelope, error) {
	if req == nil {
		return nil, Errorf(CodeInvalidArgument, "dispatch: nil envelope")
	}
	r.mu.RLock()
	b, ok := r.methods[req.Procedure]
	r.mu.RUnlock()
	if !ok {
		return nil, Errorf(CodeUnimplemented, "unknown procedure %q", req.Procedure)
	}

	if req.Msg == nil {
		codec, err := CodecFor(req.Codec)
		if err != nil {
			return nil, err
		}
		msg := b.md.NewRequest()
		if err := codec.Unmarshal(req.Payload, msg); err != nil {
			return nil, WrapError(CodeInvalidArgument, fmt.Errorf("decode %s: %w", req.Procedure, err))
		}
		req.Msg = msg
	}
	if req.Metadata == nil {
		req.Metadata = Metadata{}
	}

	ctx = withMethod(ctx, b.md)
	resp, err := b.call(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, Errorf(CodeInternal, "%s: chain returned no response and no error", req.Procedure)
	}
	if resp.Metadata == nil {
		resp.Metadata = Metadata{}
	}
	return resp, nil
}

// Method returns the descriptor bound to a procedure.
func (r *Registry) Method(procedure string) (*MethodDesc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.methods[procedure]
	if !ok {
		return nil, false
	}
	return b.md, true
}

// Procedures lists every registered procedure, sorted.
func (r *Registry) Procedures() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.order)
}

// Services lists every registered service descriptor, sorted by name.
func (r *Registry) Services() []*ServiceDesc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ServiceDesc, 0, len(r.svcs))
	for _, sd := range r.svcs {
		out = append(out, sd)
	}
	slices.SortFunc(out, func(a, b *ServiceDesc) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}

// ChainNames returns the chain a procedure runs behind, outermost first. A
// chain symmetry test compares this across bindings; `ix describe` prints it.
func (r *Registry) ChainNames(procedure string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.methods[procedure]
	if !ok {
		return nil, false
	}
	return b.chain.Names(), true
}

// MethodsOn lists the procedures exposed on a road, sorted.
func (r *Registry) MethodsOn(t transportv1.Transport) []*MethodDesc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*MethodDesc
	for _, p := range r.order {
		if b := r.methods[p]; b.md.ExposedOn(t) {
			out = append(out, b.md)
		}
	}
	return out
}

type methodKey struct{}

func withMethod(ctx context.Context, md *MethodDesc) context.Context {
	return context.WithValue(ctx, methodKey{}, md)
}

// MethodFromContext returns the method being dispatched. An optional module
// reads its own annotation off MethodDesc.Desc from here -- which is how the
// auth interceptor works without core knowing what a permission is.
func MethodFromContext(ctx context.Context) (*MethodDesc, bool) {
	md, ok := ctx.Value(methodKey{}).(*MethodDesc)
	return md, ok
}
