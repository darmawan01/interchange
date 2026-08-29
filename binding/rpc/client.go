package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/darmawan01/interchange"
	"google.golang.org/protobuf/proto"
)

// Client calls procedures over Connect without generated client code. A
// service that has generated clients should use them; this exists for `ix
// dev`, for tests, and for tools that only have a descriptor.
type Client struct {
	http     connect.HTTPClient
	baseURL  string
	codec    string
	opts     []connect.ClientOption
	metadata []func(context.Context) (interchange.Metadata, error)
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithClientCodec picks the wire codec. Default: "proto".
func WithClientCodec(name string) ClientOption {
	return func(c *Client) { c.codec = name }
}

// WithMetadata contributes metadata to every call: credentials, tenant hint,
// trace context. It mirrors the bus client's option of the same name, so
// wiring a client is the same work whichever road it takes.
func WithMetadata(f func(context.Context) (interchange.Metadata, error)) ClientOption {
	return func(c *Client) { c.metadata = append(c.metadata, f) }
}

// WithStaticMetadata is WithMetadata for values that do not change.
func WithStaticMetadata(md interchange.Metadata) ClientOption {
	return WithMetadata(func(context.Context) (interchange.Metadata, error) { return md, nil })
}

// WithClientOptions passes options through to connect-go.
func WithClientOptions(opts ...connect.ClientOption) ClientOption {
	return func(c *Client) { c.opts = append(c.opts, opts...) }
}

// NewClient returns a client against a base URL such as
// "http://localhost:8080".
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...ClientOption) *Client {
	c := &Client{http: httpClient, baseURL: baseURL, codec: interchange.CodecProto}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Invoke calls a procedure. The response is decoded into out, so no
// descriptor is needed -- which is what lets a generated CLI drive this
// client and a bus client interchangeably: the two Invoke signatures are the
// same.
func (c *Client) Invoke(ctx context.Context, procedure string, in, out proto.Message) error {
	return c.InvokeMethod(ctx, &interchange.MethodDesc{
		Procedure:   procedure,
		NewResponse: func() proto.Message { return out.ProtoReflect().New().Interface() },
	}, in, out, nil)
}

// InvokeMethod calls one method with per-call metadata. md supplies the
// procedure, the response factory and the idempotency hint that lets an
// idempotent method travel as a cacheable GET.
func (c *Client) InvokeMethod(ctx context.Context, md *interchange.MethodDesc, in, out proto.Message, header interchange.Metadata) error {
	inner, err := interchange.CodecFor(c.codec)
	if err != nil {
		return err
	}
	opts := append([]connect.ClientOption{
		connect.WithCodec(&codec{inner: inner, newMsg: md.NewResponse, binary: c.codec == interchange.CodecProto}),
	}, c.opts...)
	if md.Idempotent {
		opts = append(opts, connect.WithIdempotency(connect.IdempotencyNoSideEffects))
	}

	client := connect.NewClient[anyMessage, anyMessage](c.http, c.baseURL+md.Procedure, opts...)
	req := connect.NewRequest(&anyMessage{msg: in})
	for _, f := range c.metadata {
		extra, err := f(ctx)
		if err != nil {
			return interchange.WrapError(interchange.CodeUnauthenticated, err)
		}
		for k, v := range extra {
			req.Header().Set(k, v)
		}
	}
	for k, v := range header {
		req.Header().Set(k, v)
	}
	resp, err := client.CallUnary(ctx, req)
	if err != nil {
		return fromConnectError(err)
	}
	proto.Reset(out)
	proto.Merge(out, resp.Msg.msg)
	return nil
}

func fromConnectError(err error) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	e := &interchange.Error{
		Code:    interchange.Code(ce.Code()),
		Message: ce.Message(),
		Meta:    interchange.Metadata{},
	}
	for k, v := range ce.Meta() {
		if len(v) == 0 {
			continue
		}
		if k == ErrorReasonHeader {
			e.Reason = v[0]
			continue
		}
		e.Meta.Set(k, v[0])
	}
	return e
}
