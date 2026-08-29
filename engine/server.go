package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
)

// Server serves a registry over one driver. Everything that differs between
// brokers is read from the driver's Capabilities; everything else is the same
// code for all of them.
type Server struct {
	drv interchange.Driver
	reg *interchange.Registry
	cfg serverConfig

	caps   interchange.Capabilities
	reasm  *reassembler
	dedupe *dedupe

	mu      sync.Mutex
	subs    []interchange.Unsubscribe
	started bool

	wg  sync.WaitGroup
	sem chan struct{}
}

type serverConfig struct {
	logger      *slog.Logger
	expose      transportv1.Transport
	concurrency int
	maxMessage  int
	reasmTTL    time.Duration
	dedupeTTL   time.Duration
	replyTimout time.Duration
}

// ServerOption configures a Server.
type ServerOption func(*serverConfig)

// WithLogger sets the logger. Default: slog.Default().
func WithLogger(l *slog.Logger) ServerOption {
	return func(c *serverConfig) { c.logger = l }
}

// Expose overrides which road's procedures this server subscribes. The
// default is the driver's declared transport. This is routing, not
// behaviour: nothing downstream branches on the value.
func Expose(t transportv1.Transport) ServerOption {
	return func(c *serverConfig) { c.expose = t }
}

// WithConcurrency caps in-flight handler calls. Zero means unlimited.
func WithConcurrency(n int) ServerOption {
	return func(c *serverConfig) { c.concurrency = n }
}

// WithMaxMessage caps a reassembled message. Zero means unlimited. Set it on
// any transport reachable by something you do not trust: chunking otherwise
// lets a peer stream an unbounded body into memory a chunk at a time.
func WithMaxMessage(n int) ServerOption {
	return func(c *serverConfig) { c.maxMessage = n }
}

// NewServer builds a server. It does not subscribe until Start.
func NewServer(drv interchange.Driver, reg *interchange.Registry, opts ...ServerOption) *Server {
	caps := drv.Caps()
	cfg := serverConfig{
		logger:      slog.Default(),
		expose:      caps.Transport,
		reasmTTL:    30 * time.Second,
		dedupeTTL:   2 * time.Minute,
		replyTimout: 30 * time.Second,
	}
	for _, o := range opts {
		o(&cfg)
	}
	s := &Server{
		drv:   drv,
		reg:   reg,
		cfg:   cfg,
		caps:  caps,
		reasm: newReassembler(cfg.reasmTTL, cfg.maxMessage),
	}
	if caps.AtLeastOnce {
		s.dedupe = newDedupe(cfg.dedupeTTL)
	}
	if cfg.concurrency > 0 {
		s.sem = make(chan struct{}, cfg.concurrency)
	}
	return s
}

// Subscriptions is what Start will subscribe, in sorted order. `ix describe`
// prints it and tests assert on it.
type Subscription struct {
	Pattern string
	Group   string
}

// Plan reports the subscriptions this server will make. A service whose
// methods all share one group is one wildcard subscription; a service that
// mixes groups is subscribed per procedure, because a queue group is a
// property of the subscription and not of the message.
func (s *Server) Plan() []Subscription {
	var out []Subscription
	for _, sd := range s.reg.Services() {
		var exposed []*interchange.MethodDesc
		groups := map[string]struct{}{}
		for i := range sd.Methods {
			m := &sd.Methods[i]
			if !m.ExposedOn(s.cfg.expose) {
				continue
			}
			exposed = append(exposed, m)
			groups[m.Group] = struct{}{}
		}
		if len(exposed) == 0 {
			continue
		}
		if len(groups) == 1 {
			out = append(out, Subscription{
				Pattern: s.drv.ServiceWildcard(sd.Name),
				Group:   s.group(exposed[0].Group),
			})
			continue
		}
		for _, m := range exposed {
			out = append(out, Subscription{
				Pattern: s.drv.Address(m.Procedure),
				Group:   s.group(m.Group),
			})
		}
	}
	return out
}

func (s *Server) group(g string) string {
	if !s.caps.CompetingGroup {
		return ""
	}
	return g
}

// Start subscribes. It returns as soon as the subscriptions are live; the
// driver delivers on its own goroutines.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("engine: server already started")
	}
	plan := s.Plan()
	if len(plan) == 0 {
		return fmt.Errorf("engine: no procedure is exposed on %s -- check the (transports) annotation", s.cfg.expose)
	}
	for _, sub := range plan {
		unsub, err := s.drv.Subscribe(ctx, sub.Pattern, sub.Group, s.handle)
		if err != nil {
			s.stopLocked()
			return fmt.Errorf("engine: subscribe %s: %w", sub.Pattern, err)
		}
		s.subs = append(s.subs, unsub)
	}
	s.started = true
	return nil
}

// Stop unsubscribes and waits for in-flight calls to finish.
func (s *Server) Stop() error {
	s.mu.Lock()
	err := s.stopLocked()
	s.started = false
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) stopLocked() error {
	var errs []error
	for _, u := range s.subs {
		if u == nil {
			continue
		}
		if err := u(); err != nil {
			errs = append(errs, err)
		}
	}
	s.subs = nil
	return errors.Join(errs...)
}

func (s *Server) handle(in interchange.Inbound) {
	kind, body, err := unframe(in.Body)
	if err != nil {
		s.cfg.logger.Warn("engine: dropping malformed message", slog.String("err", err.Error()))
		return
	}
	if kind == kindFrame {
		var f transportv1.Frame
		if err := proto.Unmarshal(body, &f); err != nil {
			s.cfg.logger.Warn("engine: dropping malformed frame", slog.String("err", err.Error()))
			return
		}
		whole, err := s.reasm.accept(&f)
		if err != nil {
			s.cfg.logger.Warn("engine: dropping oversized message",
				slog.String("correlation_id", f.GetCorrelationId()), slog.String("err", err.Error()))
			return
		}
		if whole == nil {
			return
		}
		if kind, body, err = unframe(whole); err != nil {
			s.cfg.logger.Warn("engine: dropping malformed reassembly", slog.String("err", err.Error()))
			return
		}
	}
	if kind != kindRequest {
		s.cfg.logger.Warn("engine: dropping non-request on a server subscription")
		return
	}

	var req transportv1.Request
	if err := proto.Unmarshal(body, &req); err != nil {
		s.cfg.logger.Warn("engine: dropping undecodable request", slog.String("err", err.Error()))
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if s.sem != nil {
			s.sem <- struct{}{}
			defer func() { <-s.sem }()
		}
		s.serve(&req, in)
	}()
}

func (s *Server) serve(req *transportv1.Request, in interchange.Inbound) {
	// Metadata fallback: native headers where the transport has them, folded
	// into the envelope where it does not. Above this line nothing else in
	// the system learns which of the two happened.
	md := interchange.Metadata{}
	if s.caps.NativeHeaders {
		for k, v := range in.Header {
			md.Set(k, v)
		}
	}
	for k, v := range req.GetMetadata() {
		md.Set(k, v)
	}

	replyTo := md.Get(interchange.MetaReplyTo)
	md.Del(interchange.MetaReplyTo)

	reply := func(resp *transportv1.Response) {
		if err := s.reply(in, replyTo, resp); err != nil {
			s.cfg.logger.Warn("engine: reply failed",
				slog.String("procedure", req.GetProcedure()),
				slog.String("err", err.Error()))
		}
	}

	// Replay suppression, enabled only where the transport needs it.
	var entry *dedupeEntry
	if s.dedupe != nil && req.GetCorrelationId() != "" {
		e, claimed := s.dedupe.begin(req.GetCorrelationId())
		if !claimed {
			if cached, ok := e.wait(s.cfg.replyTimout); ok {
				s.replayCached(in, replyTo, cached)
			}
			return
		}
		entry = e
	}

	resp := s.dispatch(req, md)
	if entry != nil {
		if b, err := proto.Marshal(resp); err == nil {
			s.dedupe.complete(entry, b)
		} else {
			s.dedupe.abandon(req.GetCorrelationId(), entry)
		}
	}
	reply(resp)
}

func (s *Server) replayCached(in interchange.Inbound, replyTo string, cached []byte) {
	var resp transportv1.Response
	if err := proto.Unmarshal(cached, &resp); err != nil {
		return
	}
	if err := s.reply(in, replyTo, &resp); err != nil {
		s.cfg.logger.Warn("engine: replayed reply failed", slog.String("err", err.Error()))
	}
}

func (s *Server) dispatch(req *transportv1.Request, md interchange.Metadata) *transportv1.Response {
	env := &interchange.Envelope{
		Procedure:     req.GetProcedure(),
		Metadata:      md,
		Payload:       req.GetPayload(),
		Codec:         req.GetCodec(),
		CorrelationID: req.GetCorrelationId(),
	}
	if ms := req.GetDeadlineUnixMs(); ms > 0 {
		env.Deadline = time.UnixMilli(ms)
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if !env.Deadline.IsZero() {
		// The server MUST derive its handler context from the envelope's
		// deadline -- that is the whole point of carrying it.
		ctx, cancel = context.WithDeadline(ctx, env.Deadline)
		defer cancel()
	}

	// A road not declared in the (transports) annotation is not a road. A
	// wildcard subscription can deliver a procedure this transport does not
	// expose, and answering it would make the annotation decorative.
	if mdesc, ok := s.reg.Method(env.Procedure); ok && !mdesc.ExposedOn(s.cfg.expose) {
		return errorResponse(env.CorrelationID, interchange.Errorf(interchange.CodeUnimplemented,
			"%s is not exposed on %s", env.Procedure, s.cfg.expose))
	}

	resp, err := s.reg.Dispatch(ctx, env)
	if err != nil {
		return errorResponse(env.CorrelationID, err)
	}

	codec, cerr := interchange.CodecFor(resp.Codec)
	if cerr != nil {
		return errorResponse(env.CorrelationID, cerr)
	}
	payload, merr := codec.Marshal(resp.Msg)
	if merr != nil {
		return errorResponse(env.CorrelationID, interchange.WrapError(interchange.CodeInternal, merr))
	}
	return &transportv1.Response{
		CorrelationId: env.CorrelationID,
		Code:          int32(interchange.CodeOK),
		Metadata:      resp.Metadata.AsMap(),
		Payload:       payload,
	}
}

func errorResponse(correlationID string, err error) *transportv1.Response {
	return &transportv1.Response{
		CorrelationId: correlationID,
		Code:          int32(interchange.CodeOf(err)),
		Message:       interchange.MessageOf(err),
		Reason:        interchange.ReasonOf(err),
		Metadata:      interchange.MetaOf(err).AsMap(),
	}
}

func (s *Server) reply(in interchange.Inbound, replyTo string, resp *transportv1.Response) error {
	framed, err := frame(kindResponse, resp)
	if err != nil {
		return err
	}
	parts, err := chunk(resp.GetCorrelationId(), framed, s.caps.MaxPayload)
	if err != nil {
		return err
	}

	hdr := map[string]string{}
	if s.caps.NativeHeaders {
		for k, v := range resp.GetMetadata() {
			hdr[k] = v
		}
	}

	for _, part := range parts {
		switch {
		case in.Reply != nil:
			if err := in.Reply(part, hdr); err != nil {
				return err
			}
		case replyTo != "":
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.replyTimout)
			err := s.drv.Publish(ctx, replyTo, part, hdr)
			cancel()
			if err != nil {
				return err
			}
		default:
			return errors.New("engine: no reply channel: the transport has no native reply and the request carried no " + interchange.MetaReplyTo)
		}
	}
	return nil
}
