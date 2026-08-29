package errors_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/errors"
	errorsv1 "github.com/darmawan01/interchange/errors/gen/go/interchange/errors/v1"
)

// TestStockSetTracksTheEnum: the Go constants are a convenience, not the
// taxonomy. If someone appends a value to reasons.proto and forgets the
// constant -- or writes a constant with no enum member -- this fails.
func TestStockSetTracksTheEnum(t *testing.T) {
	stock := errors.Stock()

	consts := []string{
		errors.ReasonInvalidArgument, errors.ReasonUnauthenticated,
		errors.ReasonPermissionDenied, errors.ReasonNotFound,
		errors.ReasonAlreadyExists, errors.ReasonFailedPrecondition,
		errors.ReasonResourceExhausted, errors.ReasonDeadlineExceeded,
		errors.ReasonUnavailable, errors.ReasonInternal,
		errors.ReasonAborted, errors.ReasonCanceled, errors.ReasonUnimplemented,
	}
	slices.Sort(consts)
	if got := stock.Reasons(); !slices.Equal(got, consts) {
		t.Errorf("the enum has %v, the constants have %v", got, consts)
	}

	// The full enum spelling is accepted too, so pasting the generated
	// constant is not a silent failure.
	if !stock.Has(errorsv1.ErrorReason_ERROR_REASON_NOT_FOUND.String()) {
		t.Error("the set rejects the enum's own value name")
	}
	// ERROR_REASON_UNSPECIFIED is not a reason: it says nothing to branch on.
	if stock.Has(errorsv1.ErrorReason_ERROR_REASON_UNSPECIFIED.String()) {
		t.Error("UNSPECIFIED is in the set")
	}
}

func TestSetComposition(t *testing.T) {
	mine := errors.SetOf("PROVIDER_NOT_FOUND")
	both := errors.Union(errors.Stock(), mine)
	if !both.Has("PROVIDER_NOT_FOUND") || !both.Has(errors.ReasonInternal) {
		t.Errorf("union lost a member: %v", both.Reasons())
	}
	if errors.Stock().Has("PROVIDER_NOT_FOUND") {
		t.Error("Union mutated the stock set")
	}
}

func TestDefaultMapper(t *testing.T) {
	ctx := context.Background()
	m := errors.DefaultMapper{}

	t.Run("a reason survives untouched", func(t *testing.T) {
		in := errors.NotFound("PROVIDER_NOT_FOUND", "no such provider")
		out := m.Map(ctx, "/svc/M", in)
		if out.Reason != "PROVIDER_NOT_FOUND" || out.Code != interchange.CodeNotFound {
			t.Errorf("got %v/%q", out.Code, out.Reason)
		}
	})

	t.Run("a bare code gets the stock reason", func(t *testing.T) {
		out := m.Map(ctx, "/svc/M", interchange.Errorf(interchange.CodePermissionDenied, "nope"))
		if out.Reason != errors.ReasonPermissionDenied {
			t.Errorf("reason = %q", out.Reason)
		}
	})

	t.Run("an opaque error is internal", func(t *testing.T) {
		out := m.Map(ctx, "/svc/M", fmt.Errorf("sql: %w", io.ErrUnexpectedEOF))
		if out.Code != interchange.CodeInternal || out.Reason != errors.ReasonInternal {
			t.Errorf("got %v/%q", out.Code, out.Reason)
		}
		if !stderrors.Is(out, io.ErrUnexpectedEOF) {
			t.Error("the cause stopped being reachable through errors.Is")
		}
	})

	t.Run("a context error keeps its own code", func(t *testing.T) {
		out := m.Map(ctx, "/svc/M", context.DeadlineExceeded)
		if out.Code != interchange.CodeDeadlineExceeded || out.Reason != errors.ReasonDeadlineExceeded {
			t.Errorf("got %v/%q", out.Code, out.Reason)
		}
	})

	t.Run("redaction hides the operator's message, not the reason", func(t *testing.T) {
		out := errors.DefaultMapper{Redact: true}.Map(ctx, "/svc/M", fmt.Errorf("sql: no rows in result set"))
		if out.Message == "sql: no rows in result set" {
			t.Error("the raw message reached the caller")
		}
		if out.Reason != errors.ReasonInternal {
			t.Errorf("reason = %q", out.Reason)
		}
	})
}

// TestUnknownReasonIsLoud: the default under `go test` is a panic, because a
// reason that is not in the contract is a programming error and a test is
// where it is cheapest to find.
func TestUnknownReasonIsLoud(t *testing.T) {
	call := run(t, errors.Interceptor(), errors.NotFound("PROVIDR_NOT_FOND", "typo"))
	defer func() {
		if r := recover(); r == nil {
			t.Error("an unknown reason did not panic under the test-binary default")
		}
	}()
	_, _ = call(context.Background(), interchange.NewEnvelope("/svc/M"))
}

// TestUnknownReasonRewrite is the runtime default: the wire keeps its promise
// that every reason is a member of the enum, and the log carries the bug.
func TestUnknownReasonRewrite(t *testing.T) {
	call := run(t, errors.Interceptor(
		errors.WithUnknownReason(errors.UnknownReasonRewrite),
		errors.WithLogger(slog.New(slog.DiscardHandler)),
	), errors.NotFound("PROVIDR_NOT_FOND", "typo"))

	_, err := call(context.Background(), interchange.NewEnvelope("/svc/M"))
	if got := interchange.ReasonOf(err); got != errors.ReasonNotFound {
		t.Errorf("reason = %q, want the code's stock reason", got)
	}
	if interchange.CodeOf(err) != interchange.CodeNotFound {
		t.Error("rewriting the reason changed the code")
	}
}

func TestDeclaredReasonPasses(t *testing.T) {
	call := run(t, errors.Interceptor(errors.WithReasons(errors.SetOf("PROVIDER_NOT_FOUND"))),
		errors.NotFound("PROVIDER_NOT_FOUND", "no such provider"))
	_, err := call(context.Background(), interchange.NewEnvelope("/svc/M"))
	if got := interchange.ReasonOf(err); got != "PROVIDER_NOT_FOUND" {
		t.Errorf("reason = %q", got)
	}
}

// TestCustomMapper: a service whose handlers return their own error type maps
// it once, here, instead of at every call site.
func TestCustomMapper(t *testing.T) {
	type domainErr struct{ error }
	call := run(t, errors.Interceptor(errors.WithMapper(
		errors.MapperFunc(func(_ context.Context, procedure string, err error) *interchange.Error {
			return errors.FailedPrecondition(errors.ReasonFailedPrecondition, "%s: %v", procedure, err)
		}))), domainErr{fmt.Errorf("quota frozen")})

	_, err := call(context.Background(), interchange.NewEnvelope("/svc/M"))
	if interchange.CodeOf(err) != interchange.CodeFailedPrecondition {
		t.Errorf("code = %v", interchange.CodeOf(err))
	}
}

func TestHTTPStatusTable(t *testing.T) {
	want := map[interchange.Code]int{
		interchange.CodeOK:                 200,
		interchange.CodeCanceled:           499,
		interchange.CodeUnknown:            500,
		interchange.CodeInvalidArgument:    400,
		interchange.CodeDeadlineExceeded:   504,
		interchange.CodeNotFound:           404,
		interchange.CodeAlreadyExists:      409,
		interchange.CodePermissionDenied:   403,
		interchange.CodeResourceExhausted:  429,
		interchange.CodeFailedPrecondition: 400,
		interchange.CodeAborted:            409,
		interchange.CodeOutOfRange:         400,
		interchange.CodeUnimplemented:      501,
		interchange.CodeInternal:           500,
		interchange.CodeUnavailable:        503,
		interchange.CodeDataLoss:           500,
		interchange.CodeUnauthenticated:    401,
	}
	for code, status := range want {
		if got := errors.HTTPStatus(code); got != status {
			t.Errorf("HTTPStatus(%v) = %d, want %d", code, got, status)
		}
	}
	if got := errors.HTTPStatus(interchange.Code(99)); got != http.StatusInternalServerError {
		t.Errorf("an unknown code projects to %d", got)
	}
}

// run wraps a handler that always fails with err in the interceptor under
// test.
func run(t *testing.T, i interchange.Interceptor, err error) interchange.UnaryFunc {
	t.Helper()
	return i(func(context.Context, *interchange.Envelope) (*interchange.Envelope, error) {
		return nil, err
	})
}
