package errors_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	"github.com/darmawan01/interchange/driver/memory"
	"github.com/darmawan01/interchange/engine"
	"github.com/darmawan01/interchange/errors"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"github.com/darmawan01/interchange/internal/testsvc"
	"google.golang.org/protobuf/proto"
)

// TestOneErrorFourSurfaces is the table in §06 as an executable claim. One
// handler error -- NotFound + "PROVIDER_NOT_FOUND" -- is observed on all four
// surfaces, from ONE registry, so the bus and the HTTP road are the same
// dispatch behind the same chain rather than two implementations that happen
// to agree today.
func TestOneErrorFourSurfaces(t *testing.T) {
	ctx := context.Background()

	// The mapped error, captured outermost so this test can project the very
	// error the client saw rather than a re-created one.
	var mapped error
	capture := interchange.Named("capture", func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			resp, err := next(ctx, req)
			if err != nil {
				mapped = err
			}
			return resp, err
		}
	})

	chain := interchange.DefaultChain(interchange.Config{}).
		After(interchange.StageTelemetry, errors.Stage(
			// PROVIDER_NOT_FOUND is the service's own reason, not a stock
			// one. Declaring it is what keeps the set closed.
			errors.WithReasons(errors.SetOf("PROVIDER_NOT_FOUND")))).
		Prepend(capture)

	reg := interchange.NewRegistry()
	binding := rpc.New(reg)
	if err := binding.Register(testsvc.Desc(), testsvc.EchoImpl(), chain); err != nil {
		t.Fatal(err)
	}

	// Surface 1 -- handler. testsvc.EchoImpl().Fail returns
	// interchange.Errorf(CodeNotFound, ...).WithReason("PROVIDER_NOT_FOUND"),
	// which is the §06 example verbatim.

	// Surface 2 -- envelope, over the bus. Response{code, reason} is the
	// canonical form; every other surface is a projection of it.
	bus := memory.New()
	srv := engine.NewServer(bus.Driver("server"), reg)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	cli, err := engine.NewClient(ctx, bus.Driver("client"), engine.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	payload, err := proto.Marshal(&commonv1.Problem{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Do(ctx, &transportv1.Request{
		Procedure: testsvc.FailProcedure,
		Payload:   payload,
		Codec:     interchange.CodecProto,
	})
	if err != nil {
		t.Fatalf("bus call: %v", err)
	}
	if got, want := interchange.Code(resp.GetCode()), interchange.CodeNotFound; got != want {
		t.Errorf("bus Response.code = %v, want %v", got, want)
	}
	if got := resp.GetReason(); got != "PROVIDER_NOT_FOUND" {
		t.Errorf("bus Response.reason = %q, want PROVIDER_NOT_FOUND", got)
	}

	// Surface 3 -- HTTP. Plain net/http rather than a Connect client, so the
	// assertion is about bytes on the wire and not about a client library
	// reconstructing them.
	hs := httptest.NewServer(binding)
	defer hs.Close()
	httpResp, err := http.Post(hs.URL+testsvc.FailProcedure, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if httpResp.StatusCode != http.StatusNotFound {
		t.Errorf("HTTP status = %d, want 404 (body %s)", httpResp.StatusCode, body)
	}
	if got := httpResp.Header.Get(rpc.ErrorReasonHeader); got != "PROVIDER_NOT_FOUND" {
		t.Errorf("%s = %q, want PROVIDER_NOT_FOUND", rpc.ErrorReasonHeader, got)
	}
	// The status the Connect binding chose and the status this module's
	// projection chooses are the same number, which is the point of reusing
	// connect-go's table rather than inventing one.
	if want := errors.HTTPStatus(interchange.CodeNotFound); httpResp.StatusCode != want {
		t.Errorf("connect answered %d, errors.HTTPStatus says %d", httpResp.StatusCode, want)
	}

	// Surface 4 -- RFC 9457, the projection the REST binding serves.
	if mapped == nil {
		t.Fatal("the chain produced no error to project")
	}
	problem, status := errors.Problem(mapped,
		errors.WithInstance("/v1/providers/p1"),
		errors.WithTypeBase("https://errors.example.com/"))
	if status != http.StatusNotFound {
		t.Errorf("problem status = %d, want 404", status)
	}
	if problem.GetReason() != "PROVIDER_NOT_FOUND" {
		t.Errorf("problem reason = %q", problem.GetReason())
	}
	if problem.GetTitle() != "Not Found" {
		t.Errorf("problem title = %q", problem.GetTitle())
	}
	if problem.GetType() != "https://errors.example.com/provider_not_found" {
		t.Errorf("problem type = %q", problem.GetType())
	}

	encoded, err := errors.MarshalProblem(problem)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("problem+json is not JSON: %v (%s)", err, encoded)
	}
	if decoded["reason"] != "PROVIDER_NOT_FOUND" {
		t.Errorf("problem+json reason = %v (%s)", decoded["reason"], encoded)
	}
	if decoded["status"] != float64(404) {
		t.Errorf("problem+json status = %v (%s)", decoded["status"], encoded)
	}
	if decoded["detail"] != "no such provider" {
		t.Errorf("problem+json detail = %v (%s)", decoded["detail"], encoded)
	}
}

// TestWriteProblem covers the helper the REST binding will call: the body is
// problem+json, and the reason is also a header, so a client reads it the
// same way on both HTTP roads.
func TestWriteProblem(t *testing.T) {
	err := errors.PermissionDenied("PROVIDER_READ_DENIED", "caller may not read %s", "p1")
	rec := httptest.NewRecorder()
	if werr := errors.WriteProblem(rec, err, errors.WithInstance("/v1/providers/p1")); werr != nil {
		t.Fatal(werr)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != errors.ProblemContentType {
		t.Errorf("content-type = %q", got)
	}
	if got := rec.Header().Get(errors.ReasonHeader); got != "PROVIDER_READ_DENIED" {
		t.Errorf("%s = %q", errors.ReasonHeader, got)
	}
	if got := rec.Header().Get(rpc.ErrorReasonHeader); got != "PROVIDER_READ_DENIED" {
		t.Errorf("the REST reason header and the Connect one disagree: %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"instance":"/v1/providers/p1"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}
