package dsl_test

import (
	"context"
	"strings"
	"testing"

	interchange "github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/frontend/dsl"
)

// "Total, or loud" (§09) is the property this table exists to defend: every
// construct the DSL cannot represent produces an error diagnostic at the
// exact line and column, with a hint saying what to do instead -- and nothing
// is emitted.
//
// The offending line in each fixture is marked `#!`; the helper finds it, so
// the expected line number cannot drift out of date.
const marker = "#!"

type diagCase struct {
	name    string
	src     string
	sidecar string
	// inSidecar says the diagnostic is expected against the sidecar, and the
	// `#!` marker is in the sidecar text.
	inSidecar bool
	want      string // substring of the message
	hint      string // substring of the hint
}

const preamble = `interchange: v1
package: catalog.v1
file: catalog
`

func TestTotalOrLoud(t *testing.T) {
	cases := []diagCase{{
		name: "unknown type",
		src: preamble + `
messages:
  Thing:
    fields:
      when: {type: Instant, n: 1} #!
`,
		want: `unknown type "Instant"`,
		hint: "declare it under `messages:`",
	}, {
		name: "duplicate field number",
		src: preamble + `
messages:
  Thing:
    fields:
      a: {type: string, n: 1}
      b: {type: string, n: 1} #!
`,
		want: `both use number 1`,
		hint: "wire contract",
	}, {
		name: "field with no number",
		src: preamble + `
messages:
  Thing:
    fields:
      a: {type: string} #!
`,
		want: `field "a" has no number`,
		hint: "`n: 1`",
	}, {
		name: "enum without a zero value",
		src: preamble + `
enums:
  Status: #!
    values:
      STATUS_ACTIVE: 1
`,
		want: "enum Status has no zero value",
		hint: "STATUS_UNSPECIFIED: 0",
	}, {
		name: "enum zero value is not UNSPECIFIED",
		src: preamble + `
enums:
  Status: #!
    values:
      STATUS_ACTIVE: 0
`,
		want: `zero value "STATUS_ACTIVE" is not the unspecified value`,
		hint: "STATUS_UNSPECIFIED",
	}, {
		name: "unknown annotation key",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        authz: {resource: things} #!
`,
		want: `unknown key "authz"`,
		hint: "transports, group, http, auth, cli, internal, idempotency",
	}, {
		name: "unknown key inside an annotation",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        auth:
          auth_types: [SESSION]
          scope: admin #!
`,
		want: `unknown key "scope"`,
		hint: "auth_types, permission, public, platform",
	}, {
		name: "rpc references an undefined message",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: MissingResponse #!
`,
		want: `unknown response message "MissingResponse"`,
		hint: "declare it under `messages:`",
	}, {
		name: "unknown transport",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        transports: [RPC, CARRIER_PIGEON] #!
`,
		want: `unknown transport "CARRIER_PIGEON"`,
		hint: "RPC, REST, BUS, MQTT, WS",
	}, {
		name: "unknown auth verb",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        auth:
          auth_types: [SESSION]
          permission: {resource: things, verb: FROB} #!
`,
		want: `unknown verb "FROB"`,
		hint: "READ, CREATE, EDIT, DELETE",
	}, {
		name: "two HTTP methods on one rpc",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        http: {get: /v1/a, post: /v1/a} #!
`,
		want: "an RPC maps to exactly one route",
		hint: "split them into two RPCs",
	}, {
		name: "duplicate key",
		src: preamble + `
messages:
  Thing:
    fields:
      a: {type: string, n: 1}
      a: {type: int32, n: 2} #!
`,
		want: `duplicate key "a"`,
		hint: "remove one",
	}, {
		name: "map cannot be repeated",
		src: preamble + `
messages:
  Thing:
    fields:
      labels: {type: "map<string, string>", n: 1, repeated: true} #!
`,
		want: "a map cannot be repeated",
		hint: "drop `repeated: true`",
	}, {
		name: "invalid map key",
		src: preamble + `
messages:
  Thing:
    fields:
      labels: {type: "map<double, string>", n: 1} #!
`,
		want: `"double" is not a valid map key`,
		hint: "integral, bool or string scalar",
	}, {
		name: "reserved field number",
		src: preamble + `
messages:
  Thing:
    fields:
      a: {type: string, n: 19001} #!
`,
		want: "which protobuf reserves",
		hint: "outside 19000-19999",
	}, {
		name: "sidecar entry matches no rpc",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
`,
		sidecar: `procedures:
  /catalog.v1.S/Missing: #!
    transports: [RPC]
`,
		inSidecar: true,
		want:      "matches no RPC",
		hint:      "matches nothing is an annotation nobody applied",
	}, {
		name: "annotated both inline and in the sidecar",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        transports: [RPC]
`,
		sidecar: `procedures:
  /catalog.v1.S/M:
    transports: [RPC, BUS] #!
`,
		inSidecar: true,
		want:      "annotated both inline and in the sidecar",
		hint:      "not both",
	}, {
		name: "not a procedure string",
		src: preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M: {request: Req, response: Req}
`,
		sidecar: `procedures:
  ListProviders: #!
    transports: [RPC]
`,
		inSidecar: true,
		want:      "is not a procedure",
		hint:      "/pkg.v1.Service/Method",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := c.src
			path := "catalog.ix.yaml"
			if c.inSidecar {
				text = c.sidecar
				path = "(sidecar)"
			}
			wantLine := markedLine(t, text)

			src := interchange.Sources{
				Root:    ".",
				Paths:   []string{"catalog.ix.yaml"},
				Content: map[string][]byte{"catalog.ix.yaml": []byte(c.src)},
			}
			if c.sidecar != "" {
				src.Sidecar = []byte(c.sidecar)
			}
			set, diags, err := dsl.New().Parse(context.Background(), src, interchange.Options{})
			if err == nil {
				t.Fatalf("Parse succeeded; want an error\n%v", diags)
			}
			if set != nil {
				t.Error("Parse returned descriptors alongside an error: a partial contract is worse than none")
			}
			found := false
			for _, d := range diags {
				if d.Severity != interchange.SeverityError || !strings.Contains(d.Message, c.want) {
					continue
				}
				found = true
				if d.Path != path {
					t.Errorf("path = %q, want %q", d.Path, path)
				}
				if d.Line != wantLine {
					t.Errorf("line = %d, want %d\n%s", d.Line, wantLine, d)
				}
				if d.Col <= 0 {
					t.Errorf("no column:\n%s", d)
				}
				if !strings.Contains(d.Hint, c.hint) {
					t.Errorf("hint = %q, want it to contain %q", d.Hint, c.hint)
				}
			}
			if !found {
				t.Fatalf("no error diagnostic containing %q; got:\n%v", c.want, diags)
			}
		})
	}
}

func markedLine(t *testing.T, text string) int {
	t.Helper()
	for i, l := range strings.Split(text, "\n") {
		if strings.Contains(l, marker) {
			return i + 1
		}
	}
	t.Fatalf("fixture has no %s marker", marker)
	return 0
}

// A missing auth block is a warning, not an error: authorization is an
// optional module, and its policy is the module's to set. The frontend's job
// is to make the omission visible.
func TestMissingAuthIsAWarning(t *testing.T) {
	src := interchange.Sources{
		Root:  ".",
		Paths: []string{"catalog.ix.yaml"},
		Content: map[string][]byte{"catalog.ix.yaml": []byte(preamble + `
messages:
  Req: {fields: {a: {type: string, n: 1}}}
services:
  S:
    rpcs:
      M: {request: Req, response: Req}
`)},
	}
	_, diags, err := dsl.New().Parse(context.Background(), src, interchange.Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, d := range diags {
		if d.Severity == interchange.SeverityWarning && strings.Contains(d.Message, "no authorization declared") {
			if d.Line == 0 || d.Col == 0 {
				t.Errorf("warning has no location: %s", d)
			}
			return
		}
	}
	t.Fatalf("no warning for an unannotated RPC; got:\n%v", diags)
}

// Two sources in one run is where a per-file check stops being enough: each
// file is individually valid, and the contract is still wrong.
func TestMultiFileCollisions(t *testing.T) {
	const body = `
messages:
  Thing:
    fields:
      a: {type: string, n: 1}
`
	cases := []struct {
		name       string
		a, b       string
		want, hint string
	}{{
		name: "two sources emit the same proto file",
		a:    "interchange: v1\npackage: catalog.v1\nfile: catalog\n" + body,
		b:    "interchange: v1\npackage: catalog.v1\nfile: catalog\nmessages:\n  Other:\n    fields:\n      a: {type: string, n: 1}\n",
		want: "both emit catalog/v1/catalog.proto",
		hint: "distinct `file:`",
	}, {
		name: "the same type declared in two sources",
		a:    "interchange: v1\npackage: catalog.v1\nfile: one\n" + body,
		b:    "interchange: v1\npackage: catalog.v1\nfile: two\n" + body,
		want: "catalog.v1.Thing is already declared in a.ix.yaml",
		hint: "one proto package cannot hold the name twice",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := interchange.Sources{
				Root:  ".",
				Paths: []string{"a.ix.yaml", "b.ix.yaml"},
				Content: map[string][]byte{
					"a.ix.yaml": []byte(c.a),
					"b.ix.yaml": []byte(c.b),
				},
			}
			set, diags, err := dsl.New().Parse(context.Background(), src, interchange.Options{})
			if err == nil {
				t.Fatalf("Parse succeeded; want an error")
			}
			if set != nil {
				t.Error("Parse returned descriptors alongside an error")
			}
			for _, d := range diags {
				if d.Severity == interchange.SeverityError && strings.Contains(d.Message, c.want) {
					if d.Path != "b.ix.yaml" || d.Line == 0 || d.Col == 0 {
						t.Errorf("wrong location: %s", d)
					}
					if !strings.Contains(d.Hint, c.hint) {
						t.Errorf("hint = %q, want it to contain %q", d.Hint, c.hint)
					}
					return
				}
			}
			t.Fatalf("no error containing %q; got:\n%v", c.want, diags)
		})
	}
}
