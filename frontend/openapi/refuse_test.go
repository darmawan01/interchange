package openapi

import (
	"context"
	"strings"
	"testing"

	"github.com/darmawan01/interchange"
)

// Every construct this frontend refuses is a row here. The assertion is the
// one §09 makes: an error, the exact source location, and a hint -- and
// nothing written.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		side string
		opt  interchange.Options
		// at is a substring of the source line the diagnostic must point at.
		at   string
		want string
		hint string
	}{{
		name: "oneOf without the opt-in",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Card: {type: object, properties: {last4: {type: string}}}
    Payment:
      type: object
      properties: {id: {type: string}}
      oneOf:
        - $ref: '#/components/schemas/Card'
`,
		at:   "      oneOf:",
		want: "components/schemas/Payment: 'oneOf' has no canonical proto form",
		hint: "use a proto oneof, or flatten the variants and set x-interchange-oneof",
	}, {
		name: "anyOf without the opt-in",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Card: {type: object, properties: {last4: {type: string}}}
    Payment:
      type: object
      properties: {id: {type: string}}
      anyOf:
        - $ref: '#/components/schemas/Card'
`,
		at:   "      anyOf:",
		want: "components/schemas/Payment: 'anyOf' has no canonical proto form",
		hint: "use a proto oneof",
	}, {
		name: "required and nullable",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Payment:
      type: object
      required: [id]
      properties:
        id: {type: string, nullable: true}
`,
		at:   "        id: {type: string, nullable: true}",
		want: "proto3 cannot distinguish present-but-null from absent",
		hint: "x-interchange-nullable: optional",
	}, {
		name: "nullable repeated",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Payment:
      type: object
      properties:
        tags: {type: array, nullable: true, items: {type: string}}
`,
		at:   "        tags: ",
		want: "is nullable and repeated",
		hint: "no presence in proto3",
	}, {
		name: "derived name collision",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
  /v1/payments/:
    get: {x-interchange-auth: {public: true}, responses: {"204": {description: ok}}}
`,
		at:   "    get: {x-interchange-auth",
		want: "ListPayments collides with the name derived from paths./v1/payments.get",
		hint: "set operationId, or x-interchange-name, on one of them",
	}, {
		name: "$ref that does not resolve",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Nope'}}}}
`,
		at:   `        "200": {description: ok`,
		want: "$ref #/components/schemas/Nope does not resolve",
		hint: "check the component name",
	}, {
		name: "external $ref",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: 'common.yaml#/Payment'}}}}
`,
		at:   `        "200": {description: ok`,
		want: "is external",
		hint: "bundle the document into one file",
	}, {
		name: "untyped schema",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Payment:
      type: object
      properties:
        anything: {}
`,
		at:   "        anything: {}",
		want: "schema has no type: proto has no 'any'",
		hint: "give it a type",
	}, {
		name: "array of arrays",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Payment:
      type: object
      properties:
        grid: {type: array, items: {type: array, items: {type: string}}}
`,
		at:   "        grid: ",
		want: "an array of arrays has no proto form",
		hint: "wrap the inner array in an object schema",
	}, {
		name: "integer enum",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Payment:
      type: object
      properties:
        priority: {type: integer, enum: [1, 2, 3]}
`,
		at:   "        priority: ",
		want: "a integer enum has no proto form",
		hint: "drop the enum and use the plain type",
	}, {
		name: "no authorization declared",
		doc: head + `
  /v1/payments:
    get:
      responses:
        "204": {description: ok}
`,
		at:   "    get:",
		want: "no authorization declared",
		hint: "add x-interchange-auth, or a sidecar entry",
	}, {
		name: "header parameter",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      parameters:
        - {name: X-Tenant, in: header, schema: {type: string}}
      responses:
        "204": {description: ok}
`,
		at:   "        - {name: X-Tenant",
		want: `parameter "X-Tenant" is in: header`,
		hint: "x-interchange-skip: true",
	}, {
		name: "object query parameter",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      parameters:
        - {name: filter, in: query, schema: {type: object, properties: {q: {type: string}}}}
      responses:
        "204": {description: ok}
`,
		at:   "        - {name: filter",
		want: `query parameter "filter" is not a scalar`,
		hint: "move it into the body",
	}, {
		name: "GET with a request body",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {q: {type: string}}}
      responses:
        "204": {description: ok}
`,
		at:   "      requestBody:",
		want: "a GET with a requestBody cannot be transcoded",
		hint: "use POST",
	}, {
		name: "HEAD has no RPC form",
		doc: head + `
  /v1/payments:
    head:
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`,
		at:   "    head:",
		want: "HEAD has no RPC form",
		hint: "remove the operation",
	}, {
		name: "POST onto an item path",
		doc: head + `
  /v1/payments/{paymentId}:
    post:
      x-interchange-auth: {public: true}
      parameters:
        - {name: paymentId, in: path, required: true, schema: {type: string}}
      responses:
        "204": {description: ok}
`,
		at:   "    post:",
		want: "has no derivable name",
		hint: "set operationId",
	}, {
		name: "two success responses with a body",
		doc: head + `
  /v1/payments:
    post:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {type: object, properties: {a: {type: string}}}}}}
        "202": {description: queued, content: {application/json: {schema: {type: object, properties: {b: {type: string}}}}}}
`,
		at:   "      responses:",
		want: "2 success responses carry a body (200, 202)",
		hint: "an RPC returns one message",
	}, {
		name: "no JSON media type",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {text/csv: {schema: {type: string}}}}
`,
		at:   `        "200": {description: ok`,
		want: "no JSON media type (found text/csv)",
		hint: "add an application/json media type",
	}, {
		name: "misspelled extension",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-authz: {public: true}
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`,
		at:   "      x-interchange-authz:",
		want: "x-interchange-authz is not an interchange extension",
		hint: "see README.md",
	}, {
		name: "duplicate pinned field number",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Payment'}}}}
components:
  schemas:
    Payment:
      type: object
      properties:
        a: {type: string, x-interchange-field: 7}
        b: {type: string, x-interchange-field: 7}
`,
		at:   "        b: {type: string",
		want: "field number 7 is already used by a",
		hint: "give each x-interchange-field a distinct number",
	}, {
		name: "path variable with no parameter",
		doc: head + `
  /v1/payments/{paymentId}:
    get:
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`,
		at:   "    get:",
		want: "path variable {paymentId} has no parameter declaration",
		hint: "declare it in 'parameters' with in: path",
	}, {
		name: "sidecar entry that matches nothing",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`,
		side: "procedures:\n  /payments.v1.PaymentsService/Nope:\n    transports: [RPC]\n",
		at:   "  /payments.v1.PaymentsService/Nope:",
		want: "matches no procedure in this document",
		hint: "procedure strings are",
	}, {
		name: "unknown transport",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      x-interchange-transports: [RPC, CARRIER_PIGEON]
      responses:
        "204": {description: ok}
`,
		at:   "      x-interchange-transports:",
		want: `"CARRIER_PIGEON" is not a transport`,
		hint: "one of RPC, REST, BUS, MQTT, WS",
	}, {
		name: "no proto package",
		doc: head + `
  /v1/payments:
    get:
      x-interchange-auth: {public: true}
      responses:
        "204": {description: ok}
`,
		opt:  interchange.Options{},
		want: "no proto package for this document",
		hint: "x-interchange-package at the document root",
	}, {
		name: "not a 3.x document",
		doc: `swagger: "2.0"
info: {title: Payments, version: "1"}
paths: {}
`,
		at:   `swagger: "2.0"`,
		want: "is not a 3.x document",
		hint: "convert a 2.0 document first",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := tc.opt
			if opt.Package == "" && tc.name != "no proto package" {
				opt = interchange.Options{Package: "payments.v1"}
			}
			src := interchange.Sources{
				Paths:       []string{"doc.yaml"},
				Content:     map[string][]byte{"doc.yaml": []byte(tc.doc)},
				Sidecar:     []byte(tc.side),
				SidecarPath: "sidecar.yaml",
			}
			res, err := (&Frontend{}).Import(context.Background(), src, opt)
			if err == nil {
				t.Fatalf("no error; got proto for %v", keysOf(res.Proto))
			}
			if len(res.Proto) != 0 {
				t.Errorf("refused import still emitted %v", keysOf(res.Proto))
			}
			if res.Files != nil {
				t.Error("refused import still returned descriptors")
			}

			var found *interchange.Diagnostic
			for i, d := range res.Diagnostics {
				if d.Severity == interchange.SeverityError && strings.Contains(d.Message, tc.want) {
					found = &res.Diagnostics[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("no error diagnostic containing %q; got:\n%s", tc.want, render(res.Diagnostics))
			}
			if !strings.Contains(found.Hint, tc.hint) {
				t.Errorf("hint = %q, want it to contain %q", found.Hint, tc.hint)
			}
			if tc.at != "" {
				want := lineOf(t, tc.doc, tc.side, tc.at)
				if found.Line != want {
					t.Errorf("line = %d, want %d (%q)\n%s", found.Line, want, tc.at, found.String())
				}
				if found.Col == 0 {
					t.Error("no column")
				}
			}
		})
	}
}

// head is the preamble every refusal fixture shares. Keeping it one line long
// keeps the reported line numbers easy to check by eye.
const head = `openapi: 3.0.3
info: {title: Payments, version: "1"}
paths:`

// lineOf finds the 1-based line a marker appears on, in whichever of the two
// files contains it, so the table asserts a location without hard-coding a
// number that shifts every time a fixture is edited.
func lineOf(t *testing.T, doc, side, marker string) int {
	t.Helper()
	for _, text := range []string{doc, side} {
		for i, line := range strings.Split(text, "\n") {
			if strings.Contains(line, marker) {
				return i + 1
			}
		}
	}
	t.Fatalf("marker %q is in neither fixture", marker)
	return 0
}

func render(d interchange.Diagnostics) string {
	var b strings.Builder
	for _, x := range d {
		b.WriteString(x.String())
		b.WriteString("\n")
	}
	return b.String()
}
