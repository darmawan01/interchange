package errors

import (
	"net/http"
	"strings"

	"github.com/darmawan01/interchange"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// ProblemContentType is the media type of an RFC 9457 problem detail.
const ProblemContentType = "application/problem+json"

// HTTPStatus projects a status code onto an HTTP one. The table is the one
// connect-go uses for the Connect protocol, deliberately: the Connect binding
// and the REST binding are two roads out of the same dispatch, and a client
// that gets 504 from one and 408 from the other for the same handler error
// has been told two different things.
//
//	ok                  200    resource_exhausted  429
//	canceled            499    failed_precondition 400
//	unknown             500    aborted             409
//	invalid_argument    400    out_of_range        400
//	deadline_exceeded   504    unimplemented       501
//	not_found           404    internal            500
//	already_exists      409    unavailable         503
//	permission_denied   403    data_loss           500
//	unauthenticated     401
func HTTPStatus(c interchange.Code) int {
	switch c {
	case interchange.CodeOK:
		return http.StatusOK
	case interchange.CodeCanceled:
		return 499 // nginx's "client closed request"; connect-go emits it too
	case interchange.CodeInvalidArgument, interchange.CodeFailedPrecondition, interchange.CodeOutOfRange:
		return http.StatusBadRequest
	case interchange.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case interchange.CodeNotFound:
		return http.StatusNotFound
	case interchange.CodeAlreadyExists, interchange.CodeAborted:
		return http.StatusConflict
	case interchange.CodePermissionDenied:
		return http.StatusForbidden
	case interchange.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case interchange.CodeUnimplemented:
		return http.StatusNotImplemented
	case interchange.CodeUnavailable:
		return http.StatusServiceUnavailable
	case interchange.CodeUnauthenticated:
		return http.StatusUnauthorized
	default:
		// unknown, internal, data_loss, and anything a future code space adds.
		return http.StatusInternalServerError
	}
}

type problemConfig struct {
	instance string
	typeBase string
}

// ProblemOption configures the projection.
type ProblemOption func(*problemConfig)

// WithInstance sets the problem's instance -- the URI of the specific
// occurrence, usually the request path.
func WithInstance(uri string) ProblemOption {
	return func(c *problemConfig) { c.instance = uri }
}

// WithTypeBase turns the reason into a documentation URI: base + the
// lower-cased reason. Without it the type stays "about:blank", which RFC 9457
// defines as "no type beyond the status code".
func WithTypeBase(base string) ProblemOption {
	return func(c *problemConfig) { c.typeBase = base }
}

// Problem projects an error onto an RFC 9457 problem detail and the HTTP
// status that goes with it. This is the third of the four surfaces in §06:
// the canonical form is Response{code, reason} and this is its projection,
// not a second source of truth. `reason` is the field a client branches on;
// `title` and `detail` are prose and will be reworded.
//
// The REST binding calls this. It lives here rather than in that binding
// because the taxonomy is this module's business, and a service that does not
// install this module gets to decide its own error body.
func Problem(err error, opts ...ProblemOption) (*commonv1.Problem, int) {
	var cfg problemConfig
	for _, o := range opts {
		o(&cfg)
	}

	code := interchange.CodeOf(err)
	status := HTTPStatus(code)
	reason := interchange.ReasonOf(err)

	p := &commonv1.Problem{
		Type:     "about:blank",
		Title:    http.StatusText(status),
		Status:   int32(status),
		Detail:   interchange.MessageOf(err),
		Instance: cfg.instance,
		Reason:   reason,
	}
	if cfg.typeBase != "" && reason != "" {
		p.Type = cfg.typeBase + strings.ToLower(reason)
	}
	if md := interchange.MetaOf(err); len(md) > 0 {
		p.Metadata = md.AsMap()
	}
	return p, status
}

// MarshalProblem encodes a problem detail as problem+json.
func MarshalProblem(p *commonv1.Problem) ([]byte, error) {
	return protojson.MarshalOptions{UseProtoNames: true}.Marshal(p)
}

// WriteProblem writes err as an RFC 9457 response. It also sets the reason
// header the Connect binding sets, so a client reads the reason the same way
// on both HTTP roads without parsing a body.
func WriteProblem(w http.ResponseWriter, err error, opts ...ProblemOption) error {
	p, status := Problem(err, opts...)
	body, merr := MarshalProblem(p)
	if merr != nil {
		return merr
	}
	if p.GetReason() != "" {
		w.Header().Set(ReasonHeader, p.GetReason())
	}
	w.Header().Set("Content-Type", ProblemContentType)
	w.WriteHeader(status)
	_, werr := w.Write(body)
	return werr
}

// ReasonHeader is the header the reason travels in over HTTP. It repeats
// binding/rpc's ErrorReasonHeader as a string rather than importing it: this
// module must not depend on a binding, and the value is contract, not code.
const ReasonHeader = "Ix-Reason"
