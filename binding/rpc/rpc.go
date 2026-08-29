// Package rpc is the request/response binding: Connect over HTTP, for
// browsers, CLIs and SDKs.
//
// It is the other half of the chain symmetry proof. Nothing here holds a
// chain: like every driver, it builds an envelope and hands it to
// Registry.Dispatch, which is the only place an interceptor runs.
package rpc

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
)

// Binding serves a registry over HTTP using the Connect protocol. One
// Binding is one http.Handler; mount it wherever you mount handlers.
type Binding struct {
	reg    *interchange.Registry
	mux    *http.ServeMux
	expose transportv1.Transport
	opts   []connect.HandlerOption
}

// Option configures a Binding.
type Option func(*Binding)

// Expose overrides which road's procedures this binding serves. The default
// is TRANSPORT_RPC.
func Expose(t transportv1.Transport) Option {
	return func(b *Binding) { b.expose = t }
}

// WithHandlerOptions passes options through to connect-go.
func WithHandlerOptions(opts ...connect.HandlerOption) Option {
	return func(b *Binding) { b.opts = append(b.opts, opts...) }
}

// New returns a binding serving reg.
func New(reg *interchange.Registry, opts ...Option) *Binding {
	b := &Binding{reg: reg, mux: http.NewServeMux(), expose: transportv1.Transport_TRANSPORT_RPC}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Register binds a service implementation behind a chain and mounts its
// procedures. The registry rejects a duplicate, so registering the same
// service on two bindings is done by registering once and constructing both
// bindings over the same registry.
func (b *Binding) Register(sd *interchange.ServiceDesc, impl any, chain *interchange.ChainSpec) error {
	if err := b.reg.Register(sd, impl, chain); err != nil {
		return err
	}
	return b.Mount(sd)
}

// Mount exposes a service that is already registered. Use it when two
// bindings serve one registry: register once, mount on each.
func (b *Binding) Mount(sd *interchange.ServiceDesc) error {
	for i := range sd.Methods {
		md := &sd.Methods[i]
		if !md.ExposedOn(b.expose) || md.Internal {
			continue
		}
		h, err := b.handler(md)
		if err != nil {
			return err
		}
		b.mux.Handle(md.Procedure, h)
	}
	return nil
}

// Handler returns the http.Handler serving every mounted procedure.
func (b *Binding) Handler() http.Handler { return b.mux }

// ServeHTTP implements http.Handler.
func (b *Binding) ServeHTTP(w http.ResponseWriter, r *http.Request) { b.mux.ServeHTTP(w, r) }

func (b *Binding) handler(md *interchange.MethodDesc) (http.Handler, error) {
	protoC, err := interchange.CodecFor(interchange.CodecProto)
	if err != nil {
		return nil, err
	}
	jsonC, err := interchange.CodecFor(interchange.CodecJSON)
	if err != nil {
		return nil, err
	}
	opts := []connect.HandlerOption{
		connect.WithCodec(&codec{inner: protoC, newMsg: md.NewRequest, binary: true}),
		connect.WithCodec(&codec{inner: jsonC, newMsg: md.NewRequest}),
	}
	if md.Idempotent {
		// Lets a Connect client issue a real cacheable GET.
		opts = append(opts, connect.WithIdempotency(connect.IdempotencyNoSideEffects))
	}
	opts = append(opts, b.opts...)

	reg := b.reg
	procedure := md.Procedure
	return connect.NewUnaryHandler(procedure,
		func(ctx context.Context, req *connect.Request[anyMessage]) (*connect.Response[anyMessage], error) {
			env := &interchange.Envelope{
				Procedure: procedure,
				Metadata:  metadataFromHeader(req.Header()),
				Codec:     codecName(req.Peer().Protocol, req.Header().Get("Content-Type")),
				Msg:       req.Msg.msg,
			}
			// HTTP supplies the deadline; the envelope carries it so the same
			// deadline interceptor runs here as on a bus.
			if dl, ok := ctx.Deadline(); ok {
				env.Deadline = dl
			}
			if id := env.Metadata.Get("ix-correlation-id"); id != "" {
				env.CorrelationID = id
			}

			resp, err := reg.Dispatch(ctx, env)
			if err != nil {
				return nil, toConnectError(err)
			}
			out := connect.NewResponse(&anyMessage{msg: resp.Msg})
			for k, v := range resp.Metadata {
				out.Header().Set(k, v)
			}
			return out, nil
		}, opts...), nil
}

func codecName(_, contentType string) string {
	if len(contentType) >= len("application/json") && contentType[len("application/"):] == "json" {
		return interchange.CodecJSON
	}
	return interchange.CodecProto
}

func metadataFromHeader(h http.Header) interchange.Metadata {
	md := make(interchange.Metadata, len(h))
	for k, v := range h {
		if len(v) > 0 {
			md.Set(k, v[0])
		}
	}
	return md
}

// ErrorReasonHeader carries the machine-readable reason on the wire. A client
// branches on this, never on the message.
const ErrorReasonHeader = "Ix-Reason"

func toConnectError(err error) error {
	code := interchange.CodeOf(err)
	ce := connect.NewError(connect.Code(code), fmt.Errorf("%s", interchange.MessageOf(err)))
	if reason := interchange.ReasonOf(err); reason != "" {
		ce.Meta().Set(ErrorReasonHeader, reason)
	}
	for k, v := range interchange.MetaOf(err) {
		ce.Meta().Set(k, v)
	}
	return ce
}

// anyMessage lets one generic Connect handler serve every method: the codec
// is bound to the method's message factory, so the type parameter carries no
// information and does not have to.
//
// This is what "the HTTP binding is generated, nothing to write" means in
// practice -- the concrete types come from the descriptor, not from a
// per-service handler file.
type anyMessage struct{ msg proto.Message }

type codec struct {
	inner  interchange.Codec
	newMsg func() proto.Message
	binary bool
}

func (c *codec) Name() string { return c.inner.Name() }

func (c *codec) Marshal(v any) ([]byte, error) {
	am, ok := v.(*anyMessage)
	if !ok || am.msg == nil {
		return nil, fmt.Errorf("rpc: cannot marshal %T", v)
	}
	return c.inner.Marshal(am.msg)
}

func (c *codec) Unmarshal(b []byte, v any) error {
	am, ok := v.(*anyMessage)
	if !ok {
		return fmt.Errorf("rpc: cannot unmarshal into %T", v)
	}
	msg := c.newMsg()
	if err := c.inner.Unmarshal(b, msg); err != nil {
		return err
	}
	am.msg = msg
	return nil
}

// MarshalStable and IsBinary implement connect's stableCodec, which is what
// makes an idempotent method reachable by a cacheable GET.
func (c *codec) MarshalStable(v any) ([]byte, error) { return c.Marshal(v) }

// IsBinary reports whether the encoding is binary, so connect knows to
// base64 the query parameter.
func (c *codec) IsBinary() bool { return c.binary }
