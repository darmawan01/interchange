// Package errors is the optional error-taxonomy module.
//
// Core's interchange.Error already carries a code, a message and a reason,
// and core assigns the reason no meaning -- it moves it (§06). This module is
// the opinion core refuses to have: a closed, append-only enum of
// machine-readable reasons, an interceptor that maps whatever a handler
// returned onto it, and the RFC 9457 projection the REST binding needs.
//
// Install it, replace it, or use none of it. Nothing in core imports this
// package, and nothing here has privileged access to core -- which is the
// test of whether the interceptor extension point is real.
package errors

import (
	"github.com/darmawan01/interchange"
)

// The constructors below build an *interchange.Error with a code and a
// reason. They do not check the reason against the set: a constructor is a
// value, and the value is not wrong until it leaves the process. Enforcement
// lives in the interceptor, where it can be configured once for the service
// rather than at every call site.

// InvalidArgument: the request is malformed regardless of system state.
func InvalidArgument(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeInvalidArgument, format, args...).WithReason(reason)
}

// Unauthenticated: no credential, or one that did not verify.
func Unauthenticated(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeUnauthenticated, format, args...).WithReason(reason)
}

// PermissionDenied: a verified caller that may not do this.
func PermissionDenied(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodePermissionDenied, format, args...).WithReason(reason)
}

// NotFound: no such resource.
func NotFound(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeNotFound, format, args...).WithReason(reason)
}

// AlreadyExists: a create that collided with an existing resource.
func AlreadyExists(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeAlreadyExists, format, args...).WithReason(reason)
}

// FailedPrecondition: well-formed, but the system is in the wrong state for
// it. Retrying without changing that state will fail again.
func FailedPrecondition(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeFailedPrecondition, format, args...).WithReason(reason)
}

// ResourceExhausted: a quota or a rate limit.
func ResourceExhausted(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeResourceExhausted, format, args...).WithReason(reason)
}

// Aborted: a concurrency conflict -- a lost optimistic lock, a serialization
// failure. Retrying the whole operation is the usual answer.
func Aborted(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeAborted, format, args...).WithReason(reason)
}

// Unavailable: a downstream is down or overloaded. Retry with backoff.
func Unavailable(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeUnavailable, format, args...).WithReason(reason)
}

// Unimplemented: the procedure exists in the contract but not here.
func Unimplemented(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeUnimplemented, format, args...).WithReason(reason)
}

// Internal: a bug on this side. The reason is for a client's dashboards; the
// message is for yours.
func Internal(reason, format string, args ...any) *interchange.Error {
	return interchange.Errorf(interchange.CodeInternal, format, args...).WithReason(reason)
}

// Wrap keeps cause reachable through errors.Is and errors.As while giving
// the wire a code and a reason.
func Wrap(code interchange.Code, reason string, cause error) *interchange.Error {
	if cause == nil {
		return nil
	}
	return interchange.WrapError(code, cause).WithReason(reason)
}
