package dsl_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	interchange "github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/frontend/dsl"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

var update = flag.Bool("update", false, "rewrite the golden .proto files")

// sources builds Sources the way `ix import` would, but with short paths so
// the header comment in the emitted proto does not depend on the working
// directory.
func sources(t *testing.T, name string, sidecar string) interchange.Sources {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	src := interchange.Sources{
		Root:    "testdata",
		Paths:   []string{name},
		Content: map[string][]byte{name: b},
	}
	if sidecar != "" {
		sb, err := os.ReadFile(filepath.Join("testdata", sidecar))
		if err != nil {
			t.Fatal(err)
		}
		src.Sidecar = sb
		src.SidecarPath = sidecar
	}
	return src
}

var opts = interchange.Options{GoPackagePrefix: "github.com/darmawan01/interchange/examples/catalog/gen/go"}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test -run %s -update)", err, t.Name())
	}
	if !bytes.Equal(want, got) {
		t.Errorf("emitted proto differs from %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// The whole point of the frontend in one test: a YAML file a team can write
// without knowing protobuf becomes a .proto file with every annotation on it.
func TestGoldenCatalog(t *testing.T) {
	f := emitter(t)
	out, diags, err := f.ProtoSources(context.Background(), sources(t, "catalog.ix.yaml", ""), opts)
	if err != nil {
		t.Fatalf("ProtoSources: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("diagnostics: %v", diags)
	}
	src, ok := out["catalog/v1/catalog.proto"]
	if !ok {
		t.Fatalf("emitted %v, want catalog/v1/catalog.proto", keys(out))
	}
	golden(t, "catalog.golden.proto", src)
}

// The sidecar is the universal fallback (§09): the same contract, with the
// annotations in a file of their own.
func TestGoldenSidecar(t *testing.T) {
	f := emitter(t)
	out, diags, err := f.ProtoSources(context.Background(), sources(t, "bare.ix.yaml", "bare.annotations.yaml"), opts)
	if err != nil {
		t.Fatalf("ProtoSources: %v", err)
	}
	if diags.HasErrors() {
		t.Fatalf("diagnostics: %v", diags)
	}
	golden(t, "bare.golden.proto", out["catalog/v1/bare.proto"])
}

// The emitted proto is committed and sits under the drift gate, so identical
// input must produce identical bytes -- descriptors included.
func TestDeterministic(t *testing.T) {
	f := emitter(t)
	ctx := context.Background()

	var lastSrc []byte
	var lastSet []byte
	for i := range 5 {
		out, _, err := f.ProtoSources(ctx, sources(t, "catalog.ix.yaml", ""), opts)
		if err != nil {
			t.Fatal(err)
		}
		set, _, err := dsl.New().Parse(ctx, sources(t, "catalog.ix.yaml", ""), opts)
		if err != nil {
			t.Fatal(err)
		}
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if !bytes.Equal(lastSrc, out["catalog/v1/catalog.proto"]) {
				t.Fatalf("run %d emitted different .proto bytes", i)
			}
			if !bytes.Equal(lastSet, b) {
				t.Fatalf("run %d emitted different descriptor bytes", i)
			}
		}
		lastSrc, lastSet = out["catalog/v1/catalog.proto"], b
	}
}

func TestDetect(t *testing.T) {
	f := dsl.New()
	cases := []struct {
		path string
		head string
		want bool
	}{
		{"api/catalog.ix.yaml", "package: catalog.v1\n", true},
		{"api/catalog.interchange.yaml", "package: catalog.v1\n", true},
		{"api/catalog.yaml", "interchange: v1\npackage: catalog.v1\n", true},
		{"api/catalog.yaml", "package: catalog.v1\n", false},
		{"api/payments.yaml", "openapi: 3.0.3\n", false},
		// A .interchange.yaml holding only annotations is a sidecar: an
		// input to a frontend, not a source for one.
		{"api/catalog.interchange.yaml", "procedures:\n  /a.v1.S/M:\n    transports: [RPC]\n", false},
		{"api/catalog.ix.yaml", "openapi: 3.0.3\n", false},
		{"api/schema.json", `{"$schema": "..."}`, false},
	}
	for _, c := range cases {
		if got := f.Detect(c.path, []byte(c.head)); got != c.want {
			t.Errorf("Detect(%q, %q) = %v, want %v", c.path, c.head, got, c.want)
		}
	}
}

func TestName(t *testing.T) {
	if n := dsl.New().Name(); n != "dsl" {
		t.Fatalf("Name() = %q", n)
	}
	if _, ok := interchange.FrontendFor("dsl"); !ok {
		t.Fatal("dsl did not register itself")
	}
}

// The .proto source is reached through core's optional interface, not a
// concrete type: that is what `ix import` asserts on before it will write a
// tree.
func emitter(t *testing.T) interchange.SourceEmitter {
	t.Helper()
	e, ok := dsl.New().(interchange.SourceEmitter)
	if !ok {
		t.Fatal("the dsl frontend does not implement interchange.SourceEmitter")
	}
	return e
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Options.Deps is the only way an adopter's own protos can be referenced: a
// frontend must not read the filesystem, so without it the resolvable types
// are whatever happens to be linked into the importing binary.
func TestDepsResolveExternalTypes(t *testing.T) {
	deps := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    proto.String("acme/legacy/v1/legacy.proto"),
		Package: proto.String("acme.legacy.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("LegacyId"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("value"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("value"),
			}},
		}},
	}}}

	const src = `interchange: v1
package: catalog.v1
file: catalog

messages:
  Thing:
    fields:
      id: {type: acme.legacy.v1.LegacyId, n: 1}
`
	in := interchange.Sources{
		Root:    ".",
		Paths:   []string{"catalog.ix.yaml"},
		Content: map[string][]byte{"catalog.ix.yaml": []byte(src)},
	}

	// Nothing in this binary knows acme.legacy.v1, so without Deps the type
	// is unknown -- and says so at the field.
	if _, diags, err := dsl.New().Parse(context.Background(), in, interchange.Options{}); err == nil {
		t.Fatal("Parse resolved a type nobody supplied")
	} else if !strings.Contains(diags.Err().Error(), `unknown type "acme.legacy.v1.LegacyId"`) {
		t.Errorf("diagnostics = %v", diags)
	}

	opt := interchange.Options{Deps: deps}
	out, _, err := emitter(t).ProtoSources(context.Background(), in, opt)
	if err != nil {
		t.Fatalf("ProtoSources with Deps: %v", err)
	}
	if got := string(out["catalog/v1/catalog.proto"]); !strings.Contains(got, `import "acme/legacy/v1/legacy.proto";`) {
		t.Errorf("the import Deps implies was not emitted:\n%s", got)
	}
	set, _, err := dsl.New().Parse(context.Background(), in, opt)
	if err != nil {
		t.Fatalf("Parse with Deps: %v", err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		t.Fatal(err)
	}
	fd := find[protoreflect.MessageDescriptor](t, files, "catalog.v1.Thing").Fields().ByName("id")
	if got := fd.Message().FullName(); got != "acme.legacy.v1.LegacyId" {
		t.Errorf("field id resolved to %s", got)
	}
}

// A named sidecar reports against its own name; an unnamed one falls back to
// a placeholder rather than blaming the DSL file.
func TestSidecarPathInDiagnostics(t *testing.T) {
	in := interchange.Sources{
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
		Sidecar: []byte("procedures:\n  /catalog.v1.S/Gone:\n    transports: [RPC]\n"),
	}
	for _, want := range []string{"(sidecar)", "api/catalog.annotations.yaml"} {
		in.SidecarPath = ""
		if want != "(sidecar)" {
			in.SidecarPath = want
		}
		_, diags, err := dsl.New().Parse(context.Background(), in, interchange.Options{})
		if err == nil {
			t.Fatal("Parse accepted a sidecar entry matching no RPC")
		}
		found := false
		for _, d := range diags {
			if strings.Contains(d.Message, "matches no RPC") {
				found = true
				if d.Path != want {
					t.Errorf("path = %q, want %q", d.Path, want)
				}
				if d.Line != 2 {
					t.Errorf("line = %d, want 2", d.Line)
				}
			}
		}
		if !found {
			t.Fatalf("no diagnostic for the unmatched procedure: %v", diags)
		}
	}
}
