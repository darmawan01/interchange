package errors

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/darmawan01/interchange"
)

// StageName is the anchor other stages are inserted around. Core's stage
// names are API for exactly this reason (§06), and so is this one.
const StageName = "errors"

// UnknownReasonPolicy decides what happens when a handler returns a reason
// that is not a member of the configured Set. That is always a programming
// error -- a typo, or a reason someone forgot to append to the enum -- but
// what a production process should do about it is a deployment decision.
type UnknownReasonPolicy int

const (
	// UnknownReasonPanic panics. It is the default under `go test`, where a
	// reason that does not exist should fail the build rather than reach a
	// golden file.
	UnknownReasonPanic UnknownReasonPolicy = iota

	// UnknownReasonRewrite logs and substitutes the code's stock reason. It
	// is the default outside tests: an unregistered reason on the wire
	// breaks the one promise this module makes -- that a client can
	// enumerate the reasons from the contract -- so it does not go out.
	UnknownReasonRewrite

	// UnknownReasonLog logs and lets the reason through. For a service
	// migrating an existing taxonomy into the enum.
	UnknownReasonLog

	// UnknownReasonAllow disables the check.
	UnknownReasonAllow
)

type config struct {
	mapper  Mapper
	reasons []Set
	policy  UnknownReasonPolicy
	logger  *slog.Logger
}

// Option configures the interceptor.
type Option func(*config)

// WithMapper replaces the default mapper. This is the seam for a service
// whose handlers return their own error type.
func WithMapper(m Mapper) Option { return func(c *config) { c.mapper = m } }

// WithReasons accepts additional reason sets. The stock enum stays accepted:
// a generic PERMISSION_DENIED raised by an interceptor is legal in every
// service, and a service adds its own enum on top rather than replacing it.
func WithReasons(sets ...Set) Option {
	return func(c *config) { c.reasons = append(c.reasons, sets...) }
}

// WithUnknownReason sets the policy for a reason outside the set.
func WithUnknownReason(p UnknownReasonPolicy) Option {
	return func(c *config) { c.policy = p }
}

// WithLogger sets the logger. Default: slog.Default().
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.logger = l } }

// Stage returns the interceptor as a named chain stage.
//
// Put it directly inside telemetry:
//
//	interchange.DefaultChain(cfg).After(interchange.StageTelemetry, errors.Stage())
//
// Position matters in one specific way. Everything it should normalise has to
// be *below* it -- including the internal error that core's recover stage
// makes out of a panic, which is why it goes outside recover rather than at
// the end of the chain. The cost of that placement is that an
// UnknownReasonPanic is a real panic rather than a recovered one; that is the
// intent, but a chain that Appends this stage innermost instead will see
// recover quietly turn that panic into a 500.
func Stage(opts ...Option) interchange.Stage {
	return interchange.Named(StageName, Interceptor(opts...))
}

// Interceptor is Stage without the name, for a chain that names it itself.
func Interceptor(opts ...Option) interchange.Interceptor {
	cfg := config{policy: defaultPolicy()}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.mapper == nil {
		cfg.mapper = DefaultMapper{}
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	accepted := Union(append([]Set{Stock()}, cfg.reasons...)...)

	return func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			resp, err := next(ctx, req)
			if err == nil {
				return resp, nil
			}
			mapped := cfg.mapper.Map(ctx, req.Procedure, err)
			if mapped == nil {
				// A Mapper that swallows an error would turn a failure into
				// a response with no message, which is worse than the error
				// it was given.
				mapped = interchange.Errorf(interchange.CodeInternal,
					"%s: mapper returned no error for: %v", req.Procedure, err).
					WithReason(ReasonInternal)
			}
			return nil, cfg.enforce(accepted, req.Procedure, mapped)
		}
	}
}

// defaultPolicy is loud where loud is free. testing.Testing() is true only in
// a test binary, so this is a runtime check rather than a build tag nobody
// remembers to set.
func defaultPolicy() UnknownReasonPolicy {
	if testing.Testing() {
		return UnknownReasonPanic
	}
	return UnknownReasonRewrite
}

func (c config) enforce(accepted Set, procedure string, e *interchange.Error) *interchange.Error {
	if c.policy == UnknownReasonAllow || e.Reason == "" || accepted.Has(e.Reason) {
		return e
	}
	switch c.policy {
	case UnknownReasonPanic:
		panic(fmt.Sprintf("errors: %s returned reason %q, which is not a member of the closed set %v "+
			"-- append it to your ErrorReason enum or pass errors.WithReasons",
			procedure, e.Reason, accepted.Reasons()))
	case UnknownReasonRewrite:
		c.logger.Error("errors: rewriting a reason outside the closed set",
			slog.String("procedure", procedure),
			slog.String("reason", e.Reason),
			slog.String("rewritten_to", ReasonForCode(e.Code)))
		out := *e
		out.Reason = ReasonForCode(out.Code)
		return &out
	default:
		c.logger.Warn("errors: reason is outside the closed set",
			slog.String("procedure", procedure),
			slog.String("reason", e.Reason))
		return e
	}
}
