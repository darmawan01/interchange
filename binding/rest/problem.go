package rest

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/errors"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
)

// Failure is everything the REST surface knows about a call that did not
// succeed.
type Failure struct {
	// Err is what the handler returned, recovered from below the transcoder.
	// It is nil when the transcoder refused the request before any handler
	// ran -- an unroutable URI, a body it could not bind onto the request
	// message.
	Err error

	// Status is the status the transcoder had chosen. It is what a writer
	// should use when Err is nil; when Err is not nil the error's own code
	// decides, so that the same handler error gets the same status on both
	// HTTP roads.
	Status int

	// Detail is the transcoder's own message, when it produced one.
	Detail string
}

// ProblemWriter renders a failed call onto the REST surface. It is the seam a
// service with its own error taxonomy replaces: nothing below here knows what
// an error body looks like.
type ProblemWriter func(w http.ResponseWriter, r *http.Request, f Failure)

// DefaultProblemWriter projects the failure through the /errors module's RFC
// 9457 mapping -- the same status table connect-go uses, so a client gets one
// answer from both HTTP roads for one handler error, and the machine-readable
// reason travels in the same header on both.
func DefaultProblemWriter(w http.ResponseWriter, r *http.Request, f Failure) {
	// Whatever the transcoder had decided to say is being replaced.
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")

	if f.Err != nil {
		_ = errors.WriteProblem(w, f.Err, errors.WithInstance(r.URL.Path))
		return
	}

	// No handler ran, so there is no code and no reason to project -- only
	// the status the transcoder chose. Inventing a reason here would put a
	// value on the wire that is in nobody's taxonomy.
	p := &commonv1.Problem{
		Type:     "about:blank",
		Title:    http.StatusText(f.Status),
		Status:   int32(f.Status),
		Detail:   f.Detail,
		Instance: r.URL.Path,
	}
	body, err := errors.MarshalProblem(p)
	if err != nil {
		http.Error(w, http.StatusText(f.Status), f.Status)
		return
	}
	w.Header().Set("Content-Type", errors.ProblemContentType)
	w.WriteHeader(f.Status)
	_, _ = w.Write(body)
}

// serve runs the transcoder with an error body it does not get to choose.
//
// Vanguard renders a failed call as the JSON form of a gRPC status, which is
// correct for a transcoder and wrong for this surface: §04 says the REST road
// reports status as problem+json. So the response is held back the moment the
// status says failure, and the accurate error -- code, reason and message --
// is picked up from below the transcoder, where the handler's own error is
// still intact.
func (b *Binding) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	c := new(capture)
	// A clone, not a WithContext: the transcoder rewrites URL.Path in place
	// on its way to the Connect handler, and WithContext shares the URL
	// pointer -- so the problem body would name the procedure the caller
	// never asked for instead of the URI they did.
	inner := r.Clone(context.WithValue(r.Context(), captureKey{}, c))
	rec := &recorder{ResponseWriter: w}

	next.ServeHTTP(rec, inner)

	if !rec.held {
		return
	}
	b.problem(w, r, Failure{
		Err:    c.error(),
		Status: rec.status,
		Detail: transcoderDetail(rec.body.Bytes()),
	})
}

// recorder passes a successful response straight through and holds a failed
// one, so its body can be replaced without buffering the responses that are
// fine.
type recorder struct {
	http.ResponseWriter
	status int
	held   bool
	wrote  bool
	body   bytes.Buffer
}

func (rc *recorder) WriteHeader(status int) {
	if rc.wrote {
		return
	}
	rc.wrote = true
	rc.status = status
	if status >= http.StatusBadRequest {
		rc.held = true
		return
	}
	rc.ResponseWriter.WriteHeader(status)
}

func (rc *recorder) Write(p []byte) (int, error) {
	if !rc.wrote {
		rc.WriteHeader(http.StatusOK)
	}
	if rc.held {
		return rc.body.Write(p)
	}
	return rc.ResponseWriter.Write(p)
}

// Flush keeps a streaming response streaming. A held response has nothing to
// flush yet -- its body is still being decided.
func (rc *recorder) Flush() {
	if rc.held {
		return
	}
	if f, ok := rc.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type captureKey struct{}

type capture struct{ err *connect.Error }

// error rebuilds the handler's error from the Connect error the transcoder
// saw. The two carry the same information -- the code is the same number and
// the reason travels in the header both HTTP roads use -- so this is a change
// of type, not of meaning.
func (c *capture) error() error {
	if c == nil || c.err == nil {
		return nil
	}
	out := interchange.Errorf(interchange.Code(c.err.Code()), "%s", c.err.Message())
	meta := interchange.Metadata{}
	for key, values := range c.err.Meta() {
		if len(values) == 0 {
			continue
		}
		if http.CanonicalHeaderKey(key) == http.CanonicalHeaderKey(rpc.ErrorReasonHeader) {
			out = out.WithReason(values[0])
			continue
		}
		meta.Set(key, values[0])
	}
	if len(meta) > 0 {
		out = out.WithMeta(meta)
	}
	return out
}

// captureInterceptor is how the handler's error survives the transcoder. It
// adds nothing to the call and changes nothing about it: the error it saw is
// the error it returns.
func captureInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err != nil {
				if c, ok := ctx.Value(captureKey{}).(*capture); ok {
					c.err = asConnectError(err)
				}
			}
			return resp, err
		}
	})
}

func asConnectError(err error) *connect.Error {
	var ce *connect.Error
	if stderrors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeUnknown, err)
}

// transcoderDetail pulls a human-readable message out of whatever the
// transcoder wrote: the JSON form of a gRPC status when it got far enough to
// know the method, plain text when it did not.
func transcoderDetail(body []byte) string {
	var status struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &status); err == nil && status.Message != "" {
		return status.Message
	}
	return strings.TrimSpace(string(body))
}
