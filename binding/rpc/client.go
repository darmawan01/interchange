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
	http    connect.HTTPClient
	baseURL string
	codec   string
	opts    []connect.ClientOption
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithClientCodec picks the wire codec. Default: "proto".
func WithClientCodec(name string) ClientOption {
	return func(c *Client) { c.codec = name }
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

// Invoke calls one method. md supplies the procedure and the response
// factory; nothing else about the service is needed.
func (c *Client) Invoke(ctx context.Context, md *interchange.MethodDesc, in, out proto.Message, header interchange.Metadata) error {
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
