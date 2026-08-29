package openapi

import (
	"context"
	"strings"
	"testing"

	"github.com/darmawan01/interchange"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	cliv1 "github.com/darmawan01/interchange/tools/gen/go/interchange/cli/v1"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestName(t *testing.T) {
	if got := New().Name(); got != "openapi" {
		t.Errorf("Name() = %q", got)
	}
}

// Detect has to be narrow: DetectFrontend treats two claimants as an error, so
// claiming a Swagger or AsyncAPI document breaks the frontend that should have
// had it.
func TestDetect(t *testing.T) {
	cases := []struct {
		head string
		want bool
	}{
		{"openapi: 3.0.3\ninfo: {}\n", true},
		{"openapi: \"3.1.0\"\n", true},
		{"{\"openapi\": \"3.0.0\", \"info\": {}}", true},
		{"# a comment\nopenapi: 3.0.0\n", true},
		{"swagger: \"2.0\"\n", false},
		{"asyncapi: 2.6.0\n", false},
		{"openapi: 4.0.0\n", false},
		{"$schema: https://json-schema.org/draft/2020-12/schema\n", false},
		{"type Query { a: String }\n", false},
		{"description: mentions openapi: 3.0 in prose\n", false},
	}
	f := New()
	for _, tc := range cases {
		if got := f.Detect("doc.yaml", []byte(tc.head)); got != tc.want {
			t.Errorf("Detect(%q) = %v, want %v", tc.head, got, tc.want)
		}
	}
}

// The emitted proto is committed and under the drift gate, so two runs over
// the same bytes must produce the same bytes. Anything ordered by a map walk
// fails here.
func TestDeterminism(t *testing.T) {
	src, opt := goldenSources(t), goldenOptions()
	first, err := (&Frontend{}).Import(context.Background(), src, opt)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		next, err := (&Frontend{}).Import(context.Background(), src, opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Proto) != len(first.Proto) {
			t.Fatalf("run %d emitted %d files, first emitted %d", i, len(next.Proto), len(first.Proto))
		}
		for path, want := range first.Proto {
			if string(next.Proto[path]) != string(want) {
				t.Errorf("run %d: %s differs", i, path)
			}
		}
		if !proto.Equal(first.Files, next.Files) {
			t.Errorf("run %d: descriptor set differs", i)
		}
		if len(first.Diagnostics) != len(next.Diagnostics) {
			t.Errorf("run %d: %d diagnostics, first had %d", i, len(next.Diagnostics), len(first.Diagnostics))
		}
		for j := range first.Diagnostics {
			if first.Diagnostics[j].String() != next.Diagnostics[j].String() {
				t.Errorf("run %d: diagnostic %d differs", i, j)
			}
		}
	}
}

// The same document written as JSON is the same contract: YAML 1.2 is a JSON
// superset, so one parser reads both and the positions still land.
func TestJSONDocument(t *testing.T) {
	const doc = `{
  "openapi": "3.0.3",
  "info": {"title": "Payments", "version": "1"},
  "paths": {
    "/v1/payments": {
      "get": {
        "x-interchange-auth": {"public": true},
        "responses": {
          "200": {
            "description": "ok",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Payment"}}}
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "Payment": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}}
    }
  }
}`
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:   []string{"payments.json"},
		Content: map[string][]byte{"payments.json": []byte(doc)},
	}, interchange.Options{Package: "payments.v1"})
	if err != nil {
		t.Fatalf("%v\n%s", err, render(res.Diagnostics))
	}
	got := string(res.Proto["payments.v1/payments_service.proto"])
	if got == "" {
		got = string(res.Proto[res.Reports[0].ProtoFile])
	}
	if !strings.Contains(got, "rpc ListPayments(ListPaymentsRequest)") {
		t.Errorf("no ListPayments in:\n%s", got)
	}
}

// A JSON document's positions must be real, not zero: the refusal path is
// worth nothing without them.
func TestJSONDiagnosticLocation(t *testing.T) {
	const doc = `{
  "openapi": "3.0.3",
  "info": {"title": "Payments", "version": "1"},
  "paths": {
    "/v1/payments": {
      "get": {
        "responses": {"204": {"description": "ok"}}
      }
    }
  }
}`
	res, _ := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:   []string{"payments.json"},
		Content: map[string][]byte{"payments.json": []byte(doc)},
	}, interchange.Options{Package: "payments.v1"})
	for _, d := range res.Diagnostics {
		if strings.Contains(d.Message, "no authorization declared") {
			if d.Line != 6 || d.Col != 7 {
				t.Errorf("location = %d:%d, want 6:7", d.Line, d.Col)
			}
			return
		}
	}
	t.Fatalf("no missing-auth diagnostic:\n%s", render(res.Diagnostics))
}

// The report is what `ix import` renders (§11), so it is a structured value
// rather than something the CLI re-derives from descriptors.
func TestReport(t *testing.T) {
	res, err := (&Frontend{}).Import(context.Background(), goldenSources(t), goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("%d reports, want 1", len(res.Reports))
	}
	r := res.Reports[0]
	if r.Format != "OpenAPI 3.0.3" {
		t.Errorf("Format = %q", r.Format)
	}
	if r.Paths != 11 || r.RPCs != 16 {
		t.Errorf("paths/RPCs = %d/%d, want 11/16", r.Paths, r.RPCs)
	}
	// 29 schemas, 28 declarations: Currency is a named string, and proto has
	// no type alias, so it is inlined at its uses instead.
	if r.Schemas != 29 || r.Messages != 28 {
		t.Errorf("schemas/messages = %d/%d, want 29/28", r.Schemas, r.Messages)
	}
	if len(r.Unresolved) != 0 {
		t.Errorf("unresolved: %v", r.Unresolved)
	}
	if r.ProtoFile != "payments/v1/payments_service.proto" {
		t.Errorf("ProtoFile = %q", r.ProtoFile)
	}
	if !strings.Contains(res.Summary(), "✓ 11 paths      → 16 RPCs") ||
		strings.Contains(res.Summary(), "nothing written") {
		t.Errorf("summary:\n%s", res.Summary())
	}
}

// A refused import renders the §11 block: the counts it got to, the items that
// need a decision, and "nothing written".
func TestReportOnRefusal(t *testing.T) {
	doc := head + `
  /v1/payments:
    get:
      responses:
        "204": {description: ok}
`
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:   []string{"payments.yaml"},
		Content: map[string][]byte{"payments.yaml": []byte(doc)},
	}, interchange.Options{Package: "payments.v1"})
	if err == nil {
		t.Fatal("no error")
	}
	sum := res.Summary()
	for _, want := range []string{
		"detected   OpenAPI 3.0.3",
		"1 construct needs a decision",
		"no authorization declared",
		"nothing written — resolve the 1 item above",
	} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary has no %q:\n%s", want, sum)
		}
	}
}

// The descriptors must carry the annotation values, read back the way every
// downstream consumer reads them.
func TestDescriptorExtensions(t *testing.T) {
	res, err := (&Frontend{}).Import(context.Background(), goldenSources(t), goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	svc := findService(t, res.Files, "payments/v1/payments_service.proto", "PaymentsService")

	sopts := svc.GetOptions()
	st, ok := proto.GetExtension(sopts, transportv1.E_ServiceTransports).(*transportv1.ServiceTransportOptions)
	if !ok || st == nil {
		t.Fatal("no service_transports on the service")
	}
	if got := st.GetOn(); len(got) != 2 || got[0] != transportv1.Transport_TRANSPORT_RPC || got[1] != transportv1.Transport_TRANSPORT_REST {
		t.Errorf("service transports = %v", got)
	}

	list := findMethod(t, svc, "ListPayments")
	http, _ := proto.GetExtension(list.GetOptions(), annotations.E_Http).(*annotations.HttpRule)
	if http.GetGet() != "/v1/payments" {
		t.Errorf("http = %v", http)
	}
	if list.GetOptions().GetIdempotencyLevel() != descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
		t.Error("a GET-derived RPC is not NO_SIDE_EFFECTS")
	}
	auth, _ := proto.GetExtension(list.GetOptions(), authv1.E_Auth).(*authv1.AuthOptions)
	if auth.GetPermission().GetResource() != "payments" || auth.GetPermission().GetVerb() != authv1.Verb_VERB_READ {
		t.Errorf("auth = %v", auth)
	}
	if got := auth.GetAuthTypes(); len(got) != 2 || got[0] != authv1.AuthType_AUTH_TYPE_SESSION || got[1] != authv1.AuthType_AUTH_TYPE_API_KEY {
		t.Errorf("auth_types = %v", got)
	}
	cmd, _ := proto.GetExtension(list.GetOptions(), cliv1.E_Command).(*cliv1.CommandOptions)
	if got := cmd.GetPath(); len(got) != 2 || got[0] != "payments" || got[1] != "list" {
		t.Errorf("cli path = %v", got)
	}

	// The body-carrying rule, and the transports that put this RPC on the bus.
	create := findMethod(t, svc, "CreatePayment")
	chttp, _ := proto.GetExtension(create.GetOptions(), annotations.E_Http).(*annotations.HttpRule)
	if chttp.GetPost() != "/v1/payments" || chttp.GetBody() != "payment_input" {
		t.Errorf("http = %v", chttp)
	}
	tr, _ := proto.GetExtension(create.GetOptions(), transportv1.E_Transports).(*transportv1.TransportOptions)
	if tr.GetGroup() != "payments" || len(tr.GetOn()) != 3 {
		t.Errorf("transports = %v", tr)
	}

	// internal, and an auth annotation that only the sidecar supplied.
	replay := findMethod(t, svc, "ReplayWebhook")
	if !proto.GetExtension(replay.GetOptions(), transportv1.E_Internal).(bool) {
		t.Error("ReplayWebhook is not internal")
	}
	rauth, _ := proto.GetExtension(replay.GetOptions(), authv1.E_Auth).(*authv1.AuthOptions)
	if !rauth.GetPlatform() || rauth.GetPermission().GetResource() != "webhooks" {
		t.Errorf("sidecar auth = %v", rauth)
	}

	// A path variable binds to a snake_case field, or the binding never fires.
	get := findMethod(t, svc, "GetPayment")
	ghttp, _ := proto.GetExtension(get.GetOptions(), annotations.E_Http).(*annotations.HttpRule)
	if ghttp.GetGet() != "/v1/payments/{payment_id}" {
		t.Errorf("http = %v", ghttp)
	}
}

func findService(t *testing.T, fds *descriptorpb.FileDescriptorSet, path, name string) *descriptorpb.ServiceDescriptorProto {
	t.Helper()
	for _, f := range fds.GetFile() {
		if f.GetName() != path {
			continue
		}
		for _, s := range f.GetService() {
			if s.GetName() == name {
				return s
			}
		}
	}
	t.Fatalf("no service %s in %s", name, path)
	return nil
}

func findMethod(t *testing.T, svc *descriptorpb.ServiceDescriptorProto, name string) *descriptorpb.MethodDescriptorProto {
	t.Helper()
	for _, m := range svc.GetMethod() {
		if m.GetName() == name {
			return m
		}
	}
	t.Fatalf("no method %s", name)
	return nil
}

// ProtoSources is the SourceEmitter view: the same run, the same bytes.
func TestProtoSources(t *testing.T) {
	src, opt := goldenSources(t), goldenOptions()
	files, diags, err := (&Frontend{}).ProtoSources(context.Background(), src, opt)
	if err != nil {
		t.Fatal(err)
	}
	if diags.HasErrors() {
		t.Fatal(render(diags))
	}
	res, err := (&Frontend{}).Import(context.Background(), src, opt)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range res.Proto {
		if string(files[path]) != string(want) {
			t.Errorf("%s differs between Import and ProtoSources", path)
		}
	}
}

// Parse is the interface-shaped view, and it must refuse exactly when Import
// does.
func TestParseRefusesPartial(t *testing.T) {
	doc := head + `
  /v1/payments:
    get:
      responses:
        "204": {description: ok}
`
	fds, diags, err := New().Parse(context.Background(), interchange.Sources{
		Paths:   []string{"doc.yaml"},
		Content: map[string][]byte{"doc.yaml": []byte(doc)},
	}, interchange.Options{Package: "payments.v1"})
	if err == nil {
		t.Fatal("no error")
	}
	if fds != nil {
		t.Error("descriptors returned alongside an error")
	}
	if !diags.HasErrors() {
		t.Error("no error diagnostic")
	}
}

// A warning is an item that still needs a decision, but the import happened --
// so the block lists it and does not claim nothing was written. Saying
// otherwise would be the reporter telling the lie the frontend refuses to.
func TestSummaryDoesNotLieAboutWhatWasWritten(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      responses:
        "204": {description: ok}
`
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:   []string{"doc.yaml"},
		Content: map[string][]byte{"doc.yaml": []byte(doc)},
	}, interchange.Options{
		Package: "things.v1",
		Params:  map[string]string{"on_missing_auth": "warn"},
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, render(res.Diagnostics))
	}
	if len(res.Proto) == 0 {
		t.Fatal("nothing emitted")
	}
	sum := res.Summary()
	if strings.Contains(sum, "nothing written") {
		t.Errorf("the import wrote a file and the summary says otherwise:\n%s", sum)
	}
	if !strings.Contains(sum, "1 construct needs a decision") {
		t.Errorf("the warning is not listed:\n%s", sum)
	}
	if !strings.Contains(sum, "✓ 1 paths") {
		t.Errorf("counts are not ✓:\n%s", sum)
	}
}

// Two documents that would land on the same proto file is a collision like any
// other here: it names both sides rather than dropping one of them.
func TestEmittedPathCollision(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths: []string{"a.yaml", "b.yaml"},
		Content: map[string][]byte{
			"a.yaml": []byte(doc),
			"b.yaml": []byte(doc),
		},
	}, interchange.Options{Package: "things.v1"})
	if err == nil {
		t.Fatal("two documents silently emitted one file")
	}
	var found bool
	for _, d := range res.Diagnostics {
		if strings.Contains(d.Message, "is also the file a.yaml emits") && d.Path == "b.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("no collision diagnostic naming both documents:\n%s", render(res.Diagnostics))
	}
}
