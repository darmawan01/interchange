package interchange

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// Observer is core's telemetry seam. Core ships no OpenTelemetry dependency
// -- you supply the adapter, or take the slog one below.
type Observer interface {
	// ObserveCall starts an observation for a procedure and returns the
	// context handlers should use plus a function to end it.
	ObserveCall(ctx context.Context, procedure string) (context.Context, func(err error))
}

// Config configures the stock chain. The zero value is usable and silent.
type Config struct {
	// Observer receives one observation per call. Nil means no telemetry.
	Observer Observer

	// Logger receives recovered panics. Nil means slog.Default().
	Logger *slog.Logger

	// DefaultTimeout applies when a caller supplied no deadline. Zero means
	// no default: a caller with no deadline gets none.
	DefaultTimeout time.Duration
}

func (c Config) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// Stage names for the three interceptors core ships. They are the anchors
// After/Before/Replace refer to, so they are API.
const (
	StageTelemetry = "telemetry"
	StageRecover   = "recover"
	StageDeadline  = "deadline"
)

// Telemetry labels spans and metrics by procedure -- the same label on every
// road, because the procedure string is the same on every road.
func Telemetry(cfg Config) Stage {
	return Named(StageTelemetry, func(next UnaryFunc) UnaryFunc {
		return func(ctx context.Context, req *Envelope) (*Envelope, error) {
			if cfg.Observer == nil {
				return next(ctx, req)
			}
			ctx, end := cfg.Observer.ObserveCall(ctx, req.Procedure)
			resp, err := next(ctx, req)
			end(err)
			return resp, err
		}
	})
}

// Recover turns a handler panic into an error rather than a dropped
// connection. On a bus a dropped connection takes the subscriber with it, so
// this matters more here than it does behind an HTTP server that restarts the
// goroutine for you.
func Recover(cfg Config) Stage {
	return Named(StageRecover, func(next UnaryFunc) UnaryFunc {
		return func(ctx context.Context, req *Envelope) (resp *Envelope, err error) {
			defer func() {
				if r := recover(); r != nil {
					cfg.logger().ErrorContext(ctx, "interchange: recovered panic",
						slog.String("procedure", req.Procedure),
						slog.Any("panic", r),
						slog.String("stack", string(debug.Stack())),
					)
					resp = nil
					err = Errorf(CodeInternal, "panic in %s: %v", req.Procedure, r)
				}
			}()
			return next(ctx, req)
		}
	})
}

// Deadline enforces the envelope's deadline -- the one thing HTTP gives free
// and a bus does not. It also drops a call whose deadline has already passed
// in flight, so a redelivered message does not run work nobody is waiting for.
func Deadline(cfg Config) Stage {
	return Named(StageDeadline, func(next UnaryFunc) UnaryFunc {
		return func(ctx context.Context, req *Envelope) (*Envelope, error) {
			deadline := req.Deadline
			if deadline.IsZero() && cfg.DefaultTimeout > 0 {
				deadline = time.Now().Add(cfg.DefaultTimeout)
			}
			if deadline.IsZero() {
				return next(ctx, req)
			}
			if remaining := time.Until(deadline); remaining <= 0 {
				return nil, Errorf(CodeDeadlineExceeded,
					"%s: deadline exceeded before dispatch (by %s)", req.Procedure, -remaining)
			}
			if existing, ok := ctx.Deadline(); ok && !existing.After(deadline) {
				return next(ctx, req)
			}
			ctx, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			return next(ctx, req)
		}
	})
}

// DefaultChain is core's stock chain: telemetry, recover, deadline. These are
// properties of dispatch rather than of any security or business model, which
// is why they are the only three in core.
//
// Everything else -- authn, authz, validation, error taxonomy, rate limiting,
// idempotency -- is an ordinary module with no privileged access.
func DefaultChain(cfg Config) *ChainSpec {
	return Chain(Telemetry(cfg), Recover(cfg), Deadline(cfg))
}

// SlogObserver is a minimal Observer for development: one log line per call
// with its duration and status. Replace it with an OpenTelemetry adapter in
// anything that matters.
func SlogObserver(l *slog.Logger) Observer { return slogObserver{l: l} }

type slogObserver struct{ l *slog.Logger }

func (o slogObserver) ObserveCall(ctx context.Context, procedure string) (context.Context, func(error)) {
	start := time.Now()
	return ctx, func(err error) {
		l := o.l
		if l == nil {
			l = slog.Default()
		}
		l.InfoContext(ctx, "rpc",
			slog.String("procedure", procedure),
			slog.String("code", CodeOf(err).String()),
			slog.Duration("took", time.Since(start)),
		)
	}
}
