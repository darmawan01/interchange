package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
)

// Client is the calling half of the engine: correlation, deadlines, chunking
// and metadata fallback for outbound calls. Generated bus clients are thin
// wrappers over Invoke.
type Client struct {
	drv  interchange.Driver
	caps interchange.Capabilities
	cfg  clientConfig

	reasm *reassembler

	mu      sync.Mutex
	pending map[string]chan *transportv1.Response
	unsub   interchange.Unsubscribe
	closed  bool
}

type clientConfig struct {
	logger     *slog.Logger
	codec      string
	timeout    time.Duration
	metadata   []func(context.Context) (interchange.Metadata, error)
	maxMessage int
}

// ClientOption configures a Client.
type ClientOption func(*clientConfig)

// WithClientLogger sets the logger.
func WithClientLogger(l *slog.Logger) ClientOption {
	return func(c *clientConfig) { c.logger = l }
}

// WithCodec picks the codec for outbound calls. Default: "proto".
func WithCodec(name string) ClientOption {
	return func(c *clientConfig) { c.codec = name }
}

// WithTimeout applies a deadline to calls whose context has none. A bus call
// that looks local but is not should never wait forever.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.timeout = d }
}

// WithMetadata contributes metadata to every call: credentials, tenant hint,
// trace context. This is where a workload identity is wired in.
func WithMetadata(f func(context.Context) (interchange.Metadata, error)) ClientOption {
	return func(c *clientConfig) { c.metadata = append(c.metadata, f) }
}

// WithStaticMetadata is WithMetadata for values that do not change.
func WithStaticMetadata(md interchange.Metadata) ClientOption {
	return WithMetadata(func(context.Context) (interchange.Metadata, error) { return md, nil })
}

// NewClient subscribes to the driver's reply address and starts matching
// responses by correlation id.
func NewClient(ctx context.Context, drv interchange.Driver, opts ...ClientOption) (*Client, error) {
	cfg := clientConfig{logger: slog.Default(), codec: interchange.CodecProto}
	for _, o := range opts {
		o(&cfg)
	}
	c := &Client{
		drv:     drv,
		caps:    drv.Caps(),
		cfg:     cfg,
		reasm:   newReassembler(30*time.Second, cfg.maxMessage),
		pending: map[string]chan *transportv1.Response{},
	}
	unsub, err := drv.Subscribe(ctx, drv.ReplyAddress(), "", c.onReply)
	if err != nil {
		return nil, fmt.Errorf("engine: subscribe to reply address: %w", err)
	}
	c.unsub = unsub
	return c, nil
}

// Close tears down the reply subscription and fails every pending call.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	unsub := c.unsub
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if unsub != nil {
		return unsub()
	}
	return nil
}

// Invoke calls a procedure and decodes the response into out.
func (c *Client) Invoke(ctx context.Context, procedure string, in, out proto.Message) error {
	codec, err := interchange.CodecFor(c.cfg.codec)
	if err != nil {
		return err
	}
	payload, err := codec.Marshal(in)
	if err != nil {
		return interchange.WrapError(interchange.CodeInvalidArgument, err)
	}
	resp, err := c.Do(ctx, &transportv1.Request{
		Procedure: procedure,
		Payload:   payload,
		Codec:     codec.Name(),
	})
	if err != nil {
		return err
	}
	if code := interchange.Code(resp.GetCode()); code != interchange.CodeOK {
		return &interchange.Error{
			Code:    code,
			Message: resp.GetMessage(),
			Reason:  resp.GetReason(),
			Meta:    interchange.NewMetadata(resp.GetMetadata()),
		}
	}
	if err := codec.Unmarshal(resp.GetPayload(), out); err != nil {
		return interchange.WrapError(interchange.CodeInternal, err)
	}
	return nil
}

// Do sends one request envelope and waits for its response. It fills in
// correlation, deadline and metadata; a caller may pre-set any of them.
func (c *Client) Do(ctx context.Context, req *transportv1.Request) (*transportv1.Response, error) {
	if req.GetProcedure() == "" {
		return nil, interchange.Errorf(interchange.CodeInvalidArgument, "engine: request has no procedure")
	}
	if req.GetCorrelationId() == "" {
		req.CorrelationId = newCorrelationID()
	}
	if req.GetCodec() == "" {
		req.Codec = c.cfg.codec
	}

	if _, ok := ctx.Deadline(); !ok && c.cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.timeout)
		defer cancel()
	}
	if dl, ok := ctx.Deadline(); ok && req.GetDeadlineUnixMs() == 0 {
		req.DeadlineUnixMs = dl.UnixMilli()
	}

	md := interchange.NewMetadata(req.GetMetadata())
	for _, f := range c.cfg.metadata {
		extra, err := f(ctx)
		if err != nil {
			return nil, interchange.WrapError(interchange.CodeUnauthenticated, err)
		}
		for k, v := range extra {
			md.Set(k, v)
		}
	}
	if !c.caps.NativeReply {
		// The transport cannot route a reply for us, so the envelope carries
		// the return address. Nothing above this line knows the difference.
		md.Set(interchange.MetaReplyTo, c.drv.ReplyAddress())
	}

	hdr := map[string]string{}
	if c.caps.NativeHeaders {
		hdr = md.AsMap()
		req.Metadata = nil
	} else {
		req.Metadata = md.AsMap()
	}

	framed, err := frame(kindRequest, req)
	if err != nil {
		return nil, err
	}
	parts, err := chunk(req.GetCorrelationId(), framed, c.caps.MaxPayload)
	if err != nil {
		return nil, err
	}

	ch := make(chan *transportv1.Response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("engine: client is closed")
	}
	c.pending[req.GetCorrelationId()] = ch
	c.mu.Unlock()
	defer c.forget(req.GetCorrelationId())

	addr := c.drv.Address(req.GetProcedure())
	for _, part := range parts {
		if err := c.drv.Publish(ctx, addr, part, hdr); err != nil {
			return nil, interchange.WrapError(interchange.CodeUnavailable, err)
		}
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, interchange.Errorf(interchange.CodeUnavailable, "engine: client closed while %s was in flight", req.GetProcedure())
		}
		return resp, nil
	case <-ctx.Done():
		// Cancel the pending call when the caller's context dies, rather
		// than leaking a slot until a response that nobody wants arrives.
		return nil, interchange.WrapError(interchange.CodeOf(ctx.Err()), ctx.Err())
	}
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) onReply(in interchange.Inbound) {
	kind, body, err := unframe(in.Body)
	if err != nil {
		c.cfg.logger.Warn("engine: dropping malformed reply", slog.String("err", err.Error()))
		return
	}
	if kind == kindFrame {
		var f transportv1.Frame
		if err := proto.Unmarshal(body, &f); err != nil {
			return
		}
		whole, err := c.reasm.accept(&f)
		if err != nil || whole == nil {
			return
		}
		if kind, body, err = unframe(whole); err != nil {
			return
		}
	}
	if kind != kindResponse {
		return
	}
	var resp transportv1.Response
	if err := proto.Unmarshal(body, &resp); err != nil {
		return
	}
	if c.caps.NativeHeaders && len(in.Header) > 0 {
		if resp.Metadata == nil {
			resp.Metadata = map[string]string{}
		}
		for k, v := range in.Header {
			resp.Metadata[k] = v
		}
	}

	c.mu.Lock()
	ch, ok := c.pending[resp.GetCorrelationId()]
	if ok {
		delete(c.pending, resp.GetCorrelationId())
	}
	c.mu.Unlock()
	if !ok {
		// A reply to a call that already timed out, or a redelivery.
		return
	}
	ch <- &resp
}

func newCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it did,
		// a duplicated correlation id would mismatch replies, so fail loudly.
		panic("interchange: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
