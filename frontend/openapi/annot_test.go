package openapi

import (
	"context"
	"strings"
	"testing"

	"github.com/darmawan01/interchange"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// onlyProto is the emitted source of a one-document import.
func onlyProto(t *testing.T, res *Result) string {
	t.Helper()
	if len(res.Proto) != 1 {
		t.Fatalf("%d emitted files, want 1: %v", len(res.Proto), keysOf(res.Proto))
	}
	for _, b := range res.Proto {
		return string(b)
	}
	return ""
}

// importDoc is the one-document helper the annotation tests share.
func importDoc(t *testing.T, doc, side string, params map[string]string) *Result {
	t.Helper()
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:       []string{"doc.yaml"},
		Content:     map[string][]byte{"doc.yaml": []byte(doc)},
		Sidecar:     []byte(side),
		SidecarPath: "sidecar.yaml",
	}, interchange.Options{Package: "things.v1", Params: params})
	if err != nil {
		t.Fatalf("%v\n%s", err, render(res.Diagnostics))
	}
	return res
}

const twoOps = `openapi: 3.0.3
info: {title: Things, version: "1"}
paths:
  /v1/things:
    get:
%s      responses:
        "204": {description: ok}
  /v1/widgets:
    get:
%s      responses:
        "204": {description: ok}
`

// Annotations arrive from a vendor extension or from the sidecar, and the two
// paths must produce the same annotation.
func TestAnnotationsFromBothSources(t *testing.T) {
	doc := strings.Replace(twoOps, "%s", `      x-interchange-auth:
        auth_types: [SESSION]
        permission: {resource: things, verb: READ}
      x-interchange-transports: [RPC, REST]
`, 1)
	doc = strings.Replace(doc, "%s", "", 1)
	side := `procedures:
  /things.v1.ThingsService/ListWidgets:
    auth:
      auth_types: [SESSION]
      permission: {resource: things, verb: READ}
    transports: [RPC, REST]
    cli: {path: [things, widgets]}
`
	res := importDoc(t, doc, side, nil)
	svc := findService(t, res.Files, "things/v1/things_service.proto", "ThingsService")

	fromExt := findMethod(t, svc, "ListThings")
	fromSidecar := findMethod(t, svc, "ListWidgets")
	for _, m := range []struct {
		name string
		opts *authv1.AuthOptions
		tr   *transportv1.TransportOptions
	}{
		{"extension", extAuthOf(fromExt), extTransportsOf(fromExt)},
		{"sidecar", extAuthOf(fromSidecar), extTransportsOf(fromSidecar)},
	} {
		if m.opts.GetPermission().GetResource() != "things" || m.opts.GetPermission().GetVerb() != authv1.Verb_VERB_READ {
			t.Errorf("%s: auth = %v", m.name, m.opts)
		}
		if got := m.tr.GetOn(); len(got) != 2 {
			t.Errorf("%s: transports = %v", m.name, got)
		}
	}
}

// The annotation nearest the operation wins: the sidecar is the fallback for a
// document you cannot edit, not an override of one you can.
// An annotation set on the operation AND in the sidecar is an error, not a
// precedence rule. Silent precedence is how a security posture gets
// overwritten by a file nobody reads -- and here the two disagree about the
// resource, so whichever won quietly would be a permission nobody chose.
func TestInlineAndSidecarConflict(t *testing.T) {
	doc := strings.Replace(twoOps, "%s", `      x-interchange-auth:
        auth_types: [WORKLOAD]
        permission: {resource: things, verb: EDIT}
`, 1)
	doc = strings.Replace(doc, "%s", `      x-interchange-auth: {public: true}
`, 1)
	side := `procedures:
  /things.v1.ThingsService/ListThings:
    auth:
      auth_types: [SESSION]
      permission: {resource: overridden, verb: READ}
`
	res, err := (&Frontend{}).Import(context.Background(), interchange.Sources{
		Paths:       []string{"doc.yaml"},
		Content:     map[string][]byte{"doc.yaml": []byte(doc)},
		Sidecar:     []byte(side),
		SidecarPath: "sidecar.yaml",
	}, interchange.Options{Package: "things.v1"})
	if err == nil {
		t.Fatal("an annotation set in both places must refuse, not quietly pick one")
	}
	found := false
	for _, d := range res.Diagnostics {
		if d.Severity == interchange.SeverityError &&
			strings.Contains(d.Message, "set both on the operation and in the sidecar") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the diagnostic must name the conflict:\n%s", render(res.Diagnostics))
	}
}

// A document-level annotation is the default for operations that declare
// neither an extension nor a sidecar entry.
func TestDocumentLevelDefault(t *testing.T) {
	doc := `openapi: 3.0.3
info: {title: Things, version: "1"}
x-interchange-auth:
  auth_types: [SESSION]
  permission: {resource: things, verb: READ}
x-interchange-transports: [RPC]
paths:
  /v1/things:
    get:
      responses:
        "204": {description: ok}
`
	res := importDoc(t, doc, "", nil)
	svc := findService(t, res.Files, "things/v1/things_service.proto", "ThingsService")
	m := findMethod(t, svc, "ListThings")
	if extAuthOf(m).GetPermission().GetResource() != "things" {
		t.Errorf("auth = %v", extAuthOf(m))
	}
	if got := extTransportsOf(m).GetOn(); len(got) != 1 || got[0] != transportv1.Transport_TRANSPORT_RPC {
		t.Errorf("transports = %v", got)
	}
}

// Missing auth is a build error by default -- an RPC with no declared security
// posture is the failure the annotation exists to prevent -- but a team
// adopting an existing document can downgrade it while they work through one.
func TestMissingAuthPolicy(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      responses:
        "204": {description: ok}
`
	src := interchange.Sources{
		Paths:   []string{"doc.yaml"},
		Content: map[string][]byte{"doc.yaml": []byte(doc)},
	}
	for _, tc := range []struct {
		policy   string
		wantErr  bool
		wantSev  interchange.Severity
		wantDiag bool
	}{
		{"", true, interchange.SeverityError, true},
		{"error", true, interchange.SeverityError, true},
		{"warn", false, interchange.SeverityWarning, true},
		{"ignore", false, 0, false},
	} {
		opt := interchange.Options{Package: "things.v1"}
		if tc.policy != "" {
			opt.Params = map[string]string{"on_missing_auth": tc.policy}
		}
		res, err := (&Frontend{}).Import(context.Background(), src, opt)
		if (err != nil) != tc.wantErr {
			t.Errorf("policy %q: err = %v, want error %v", tc.policy, err, tc.wantErr)
		}
		var found bool
		for _, d := range res.Diagnostics {
			if strings.Contains(d.Message, "no authorization declared") {
				found = true
				if d.Severity != tc.wantSev {
					t.Errorf("policy %q: severity = %v", tc.policy, d.Severity)
				}
			}
		}
		if found != tc.wantDiag {
			t.Errorf("policy %q: diagnostic present = %v", tc.policy, found)
		}
	}
}

// Nullable is genuinely ambiguous, so the resolvable cases are resolved by a
// stated rule and the unresolvable one is refused. This asserts the rule.
func TestNullabilityRule(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
components:
  schemas:
    Thing:
      type: object
      required: [id, kept]
      properties:
        id: {type: string}
        optionalScalar: {type: string}
        nullableScalar: {type: string, nullable: true}
        kept: {type: string, nullable: true, x-interchange-nullable: optional}
        tags: {type: array, items: {type: string}}
`
	res := importDoc(t, doc, "", nil)
	src := onlyProto(t, res)
	for _, want := range []string{
		"  string id = 1;",
		"  optional string optional_scalar = 2;",
		"  optional string nullable_scalar = 3;",
		"  string kept = 4;",
		"  repeated string tags = 5;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("no %q in:\n%s", want, src)
		}
	}
}

// allOf of object schemas is flattened, and the import says so rather than
// leaving a reader to work out where Thing's first three fields came from.
func TestAllOfFlattenedWithANote(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
components:
  schemas:
    Base:
      type: object
      required: [id]
      properties:
        id: {type: string}
    Thing:
      allOf:
        - $ref: '#/components/schemas/Base'
        - type: object
          properties:
            name: {type: string}
`
	res := importDoc(t, doc, "", nil)
	src := onlyProto(t, res)
	if !strings.Contains(src, "message Thing {\n  string id = 1;\n  optional string name = 2;\n}") {
		t.Errorf("Thing was not flattened:\n%s", src)
	}
	var noted bool
	for _, d := range res.Diagnostics {
		if d.Severity == interchange.SeverityNote && strings.Contains(d.Message, "allOf member(s) flattened into Thing") {
			noted = true
			if d.Line == 0 {
				t.Error("the flattening note has no location")
			}
		}
	}
	if !noted {
		t.Errorf("no flattening note:\n%s", render(res.Diagnostics))
	}
}

// oneOf is refused by default; x-interchange-oneof is the opt-in, and it emits
// a real proto oneof.
func TestOneofOptIn(t *testing.T) {
	doc := head + `
  /v1/things:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
components:
  schemas:
    Card: {type: object, required: [last4], properties: {last4: {type: string}}}
    Bank: {type: object, required: [iban], properties: {iban: {type: string}}}
    Thing:
      type: object
      x-interchange-oneof: instrument
      required: [id]
      properties:
        id: {type: string}
      oneOf:
        - $ref: '#/components/schemas/Card'
        - $ref: '#/components/schemas/Bank'
`
	res := importDoc(t, doc, "", nil)
	src := onlyProto(t, res)
	want := "  oneof instrument {\n    Card card = 2;\n    Bank bank = 3;\n  }"
	if !strings.Contains(src, want) {
		t.Errorf("no oneof in:\n%s", src)
	}
}

func extAuthOf(m *descriptorpb.MethodDescriptorProto) *authv1.AuthOptions {
	a, _ := proto.GetExtension(m.GetOptions(), authv1.E_Auth).(*authv1.AuthOptions)
	return a
}

func extTransportsOf(m *descriptorpb.MethodDescriptorProto) *transportv1.TransportOptions {
	t, _ := proto.GetExtension(m.GetOptions(), transportv1.E_Transports).(*transportv1.TransportOptions)
	return t
}
