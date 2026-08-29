package nolink_test

import (
	"context"
	"strings"
	"testing"

	interchange "github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/frontend/dsl"
)

const src = `interchange: v1
package: catalog.v1
file: catalog

messages:
  Req:
    fields:
      a: {type: string, n: 1}

services:
  S:
    rpcs:
      M:
        request: Req
        response: Req
        auth:
          auth_types: [SESSION]
          permission: {resource: things, verb: READ}
`

// Without the optional module's descriptors -- neither linked in nor passed
// in Options.Deps -- an auth annotation is a loud failure at the RPC, not a
// silently dropped security posture and not a compiler error against source
// the author never wrote.
func TestAuthAnnotationNeedsItsDescriptors(t *testing.T) {
	in := interchange.Sources{
		Root:    ".",
		Paths:   []string{"catalog.ix.yaml"},
		Content: map[string][]byte{"catalog.ix.yaml": []byte(src)},
	}
	set, diags, err := dsl.New().Parse(context.Background(), in, interchange.Options{})
	if err == nil {
		t.Fatal("Parse succeeded without the auth descriptors")
	}
	if set != nil {
		t.Error("Parse returned descriptors alongside an error")
	}
	for _, d := range diags {
		if d.Severity != interchange.SeverityError || !strings.Contains(d.Message, "interchange/auth/v1/auth.proto") {
			continue
		}
		if d.Path != "catalog.ix.yaml" || d.Line == 0 || d.Col == 0 {
			t.Errorf("wrong location: %s", d)
		}
		if !strings.Contains(d.Hint, "Options.Deps") {
			t.Errorf("hint = %q", d.Hint)
		}
		return
	}
	t.Fatalf("no diagnostic naming the missing auth descriptors; got:\n%v", diags)
}

// Core's transports annotation is a different case: it is core's own, this
// module already depends on core, and it stays resolvable with no Deps.
func TestCoreAnnotationsNeedNoDeps(t *testing.T) {
	body := strings.Replace(src, `        auth:
          auth_types: [SESSION]
          permission: {resource: things, verb: READ}
`, "        transports: [RPC, BUS]\n", 1)

	in := interchange.Sources{
		Root:    ".",
		Paths:   []string{"catalog.ix.yaml"},
		Content: map[string][]byte{"catalog.ix.yaml": []byte(body)},
	}
	if _, diags, err := dsl.New().Parse(context.Background(), in, interchange.Options{}); err != nil {
		t.Fatalf("Parse: %v\n%v", err, diags)
	}
}
