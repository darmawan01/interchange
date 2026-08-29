package openapi

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/darmawan01/interchange"
)

var update = flag.Bool("update", false, "rewrite the golden .proto")

// goldenSources is the payments fixture and its sidecar.
func goldenSources(t *testing.T) interchange.Sources {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("testdata", "payments.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	side, err := os.ReadFile(filepath.Join("testdata", "payments.interchange.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return interchange.Sources{
		Root:        "testdata",
		Paths:       []string{"payments.yaml"},
		Content:     map[string][]byte{"payments.yaml": doc},
		Sidecar:     side,
		SidecarPath: "payments.interchange.yaml",
	}
}

func goldenOptions() interchange.Options {
	return interchange.Options{
		Package:         "payments.v1",
		GoPackagePrefix: "github.com/example/payments/gen/go",
	}
}

func TestGolden(t *testing.T) {
	res, err := (&Frontend{}).Import(context.Background(), goldenSources(t), goldenOptions())
	if err != nil {
		for _, d := range res.Diagnostics {
			t.Log(d.String())
		}
		t.Fatalf("import: %v", err)
	}
	const path = "payments/v1/payments_service.proto"
	got, ok := res.Proto[path]
	if !ok {
		t.Fatalf("no %s in %v", path, keysOf(res.Proto))
	}
	gp := filepath.Join("testdata", "payments.golden.proto")
	if *update {
		if err := os.WriteFile(gp, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(gp)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("emitted proto differs from the golden; re-run with -update after reviewing\n--- got ---\n%s", got)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
