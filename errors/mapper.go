package errors

import (
	"context"
	stderrors "errors"

	"github.com/darmawan01/interchange"
)

// Mapper turns whatever a handler returned into the canonical form: a code,
// a message and a reason. It is the pluggable half of this module -- a
// service with a domain error type of its own implements Mapper once and
// stops writing interchange.Error at every call site.
//
// A Mapper must not return nil for a non-nil err.
type Mapper interface {
	Map(ctx context.Context, procedure string, err error) *interchange.Error
}

// MapperFunc adapts a function to Mapper.
type MapperFunc func(ctx context.Context, procedure string, err error) *interchange.Error

// Map implements Mapper.
func (f MapperFunc) Map(ctx context.Context, procedure string, err error) *interchange.Error {
	return f(ctx, procedure, err)
}

// DefaultMapper covers the stock enum and nothing else. It is deliberately
// small: an error that already carries a reason passes through untouched, an
// error that carries only a code gets that code's stock reason, and anything
// else is internal.
type DefaultMapper struct {
	// Redact replaces the message of an error that is not an
	// *interchange.Error with a fixed string. A raw Go error is written for
	// an operator ("sql: no rows in result set"), not for a caller; the
	// reason is what the caller was supposed to read anyway.
	Redact bool

	// RedactedMessage overrides the replacement text.
	RedactedMessage string
}

const defaultRedactedMessage = "internal error"

// Map implements Mapper.
func (m DefaultMapper) Map(_ context.Context, _ string, err error) *interchange.Error {
	if err == nil {
		return nil
	}

	var ie *interchange.Error
	if stderrors.As(err, &ie) {
		out := *ie
		if out.Code == interchange.CodeOK {
			// A returned error with an OK code is a bug in the handler, not
			// a success. Refusing to move it is how it gets noticed.
			out.Code = interchange.CodeInternal
		}
		if out.Reason == "" {
			out.Reason = ReasonForCode(out.Code)
		}
		return &out
	}

	switch code := interchange.CodeOf(err); code {
	case interchange.CodeDeadlineExceeded, interchange.CodeCanceled:
		// A context error is not an internal failure; it is the caller's
		// deadline or the caller's cancellation, and a client retries the
		// two differently.
		return interchange.WrapError(code, err).WithReason(ReasonForCode(code))
	default:
		message := err.Error()
		if m.Redact {
			message = m.RedactedMessage
			if message == "" {
				message = defaultRedactedMessage
			}
		}
		// An opaque error is CodeInternal rather than CodeUnknown: "unknown"
		// tells a client nothing it can act on, and pairing it with an
		// INTERNAL reason would say two different things at once.
		out := interchange.WrapError(interchange.CodeInternal, err)
		out.Message = message
		out.Reason = ReasonInternal
		return out
	}
}

// ReasonForCode is the stock reason for a code -- the fallback for an error
// that carries a code and no reason. Codes with no stock member of their own
// (unknown, out_of_range, data_loss) fold onto the nearest one that a client
// can branch on.
func ReasonForCode(c interchange.Code) string {
	switch c {
	case interchange.CodeInvalidArgument, interchange.CodeOutOfRange:
		return ReasonInvalidArgument
	case interchange.CodeUnauthenticated:
		return ReasonUnauthenticated
	case interchange.CodePermissionDenied:
		return ReasonPermissionDenied
	case interchange.CodeNotFound:
		return ReasonNotFound
	case interchange.CodeAlreadyExists:
		return ReasonAlreadyExists
	case interchange.CodeFailedPrecondition:
		return ReasonFailedPrecondition
	case interchange.CodeResourceExhausted:
		return ReasonResourceExhausted
	case interchange.CodeDeadlineExceeded:
		return ReasonDeadlineExceeded
	case interchange.CodeUnavailable:
		return ReasonUnavailable
	case interchange.CodeAborted:
		return ReasonAborted
	case interchange.CodeCanceled:
		return ReasonCanceled
	case interchange.CodeUnimplemented:
		return ReasonUnimplemented
	default:
		return ReasonInternal
	}
}
