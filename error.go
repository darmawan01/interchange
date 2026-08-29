package interchange

import (
	"context"
	"errors"
	"fmt"
)

// Code is an RPC status code -- NOT an HTTP status. HTTP bindings project it
// onto one; other bindings carry it as-is. The numbering matches the gRPC and
// Connect code spaces so that projection is a table lookup rather than a
// translation.
type Code int32

// The canonical code space.
const (
	CodeOK                 Code = 0
	CodeCanceled           Code = 1
	CodeUnknown            Code = 2
	CodeInvalidArgument    Code = 3
	CodeDeadlineExceeded   Code = 4
	CodeNotFound           Code = 5
	CodeAlreadyExists      Code = 6
	CodePermissionDenied   Code = 7
	CodeResourceExhausted  Code = 8
	CodeFailedPrecondition Code = 9
	CodeAborted            Code = 10
	CodeOutOfRange         Code = 11
	CodeUnimplemented      Code = 12
	CodeInternal           Code = 13
	CodeUnavailable        Code = 14
	CodeDataLoss           Code = 15
	CodeUnauthenticated    Code = 16
)

var codeNames = map[Code]string{
	CodeOK:                 "ok",
	CodeCanceled:           "canceled",
	CodeUnknown:            "unknown",
	CodeInvalidArgument:    "invalid_argument",
	CodeDeadlineExceeded:   "deadline_exceeded",
	CodeNotFound:           "not_found",
	CodeAlreadyExists:      "already_exists",
	CodePermissionDenied:   "permission_denied",
	CodeResourceExhausted:  "resource_exhausted",
	CodeFailedPrecondition: "failed_precondition",
	CodeAborted:            "aborted",
	CodeOutOfRange:         "out_of_range",
	CodeUnimplemented:      "unimplemented",
	CodeInternal:           "internal",
	CodeUnavailable:        "unavailable",
	CodeDataLoss:           "data_loss",
	CodeUnauthenticated:    "unauthenticated",
}

// String returns the snake_case name of the code.
func (c Code) String() string {
	if n, ok := codeNames[c]; ok {
		return n
	}
	return fmt.Sprintf("code_%d", int32(c))
}

// Error is the canonical error: a code, a human message, and a
// machine-readable reason a client can branch on. Core assigns Reason no
// meaning -- it moves it. Bring your own taxonomy or install /errors.
type Error struct {
	Code    Code
	Message string
	Reason  string
	Meta    Metadata

	cause error
}

// Errorf builds an *Error with a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WrapError builds an *Error carrying cause, so errors.Is and errors.As keep
// working through the chain.
func WrapError(code Code, cause error) *Error {
	if cause == nil {
		return nil
	}
	return &Error{Code: code, Message: cause.Error(), cause: cause}
}

// WithReason returns a copy of err carrying a machine-readable reason.
func (e *Error) WithReason(reason string) *Error {
	c := *e
	c.Reason = reason
	return &c
}

// WithMeta returns a copy of err carrying response metadata.
func (e *Error) WithMeta(md Metadata) *Error {
	c := *e
	c.Meta = md
	return &c
}

func (e *Error) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.cause }

// CodeOf reports the status code carried by err. A nil error is CodeOK; a
// context error maps to its canonical code; anything else is CodeUnknown.
func CodeOf(err error) Code {
	if err == nil {
		return CodeOK
	}
	var ie *Error
	if errors.As(err, &ie) {
		return ie.Code
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return CodeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	}
	return CodeUnknown
}

// ReasonOf reports the machine-readable reason carried by err, if any.
func ReasonOf(err error) string {
	var ie *Error
	if errors.As(err, &ie) {
		return ie.Reason
	}
	return ""
}

// MessageOf reports the human-readable message carried by err.
func MessageOf(err error) string {
	if err == nil {
		return ""
	}
	var ie *Error
	if errors.As(err, &ie) {
		return ie.Message
	}
	return err.Error()
}

// MetaOf reports response metadata carried by err, if any.
func MetaOf(err error) Metadata {
	var ie *Error
	if errors.As(err, &ie) {
		return ie.Meta
	}
	return nil
}
