package dsl_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	interchange "github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/frontend/dsl"
	"google.golang.org/protobuf/proto"
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
	f := dsl.New().(*dsl.Frontend)
	out, diags, err := f.ProtoSources(sources(t, "catalog.ix.yaml", ""), opts)
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
	f := dsl.New().(*dsl.Frontend)
	out, diags, err := f.ProtoSources(sources(t, "bare.ix.yaml", "bare.annotations.yaml"), opts)
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
	f := dsl.New().(*dsl.Frontend)
	ctx := context.Background()

	var lastSrc []byte
	var lastSet []byte
	for i := range 5 {
		out, _, err := f.ProtoSources(sources(t, "catalog.ix.yaml", ""), opts)
		if err != nil {
			t.Fatal(err)
		}
		set, _, err := f.Parse(ctx, sources(t, "catalog.ix.yaml", ""), opts)
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

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
