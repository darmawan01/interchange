package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportRefusesAPartialContract is the rule §09 exists for: a frontend
// that silently drops what it cannot represent produces a contract that lies,
// which is worse than the three honest ones this project replaces. The
// OpenAPI fixture carries constructs that need a decision, so nothing is
// written and each is named with its location.
func TestImportRefusesAPartialContract(t *testing.T) {
	out, code := run(t, ".", "import", "../../../frontend/openapi/testdata/payments.yaml")
	if code == 0 {
		t.Fatalf("import must fail when a construct cannot be represented:\n%s", out)
	}
	if !strings.Contains(out, "nothing written") {
		t.Fatalf("output does not say nothing was written:\n%s", out)
	}
	if !strings.Contains(out, "→") {
		t.Fatalf("a refusal must carry a hint saying what to do instead:\n%s", out)
	}
}

// TestImportDetectsBothFrontends: import auto-detects the format, which is
// what makes it an on-ramp rather than a command you have to be told how to
// use.
func TestImportDetectsBothFrontends(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"dsl", "../../../frontend/dsl/testdata/catalog.ix.yaml", "frontend   dsl"},
		{"openapi", "../../../frontend/openapi/testdata/payments.yaml", "frontend   openapi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := run(t, ".", "import", tc.path, "--dry-run", "--package", "x.v1")
			if !strings.Contains(out, tc.want) {
				t.Fatalf("expected %q in:\n%s", tc.want, out)
			}
		})
	}
}

// TestImportNamesAFormatItCannotRead: "AsyncAPI, no adapter" is a useful
// answer and "unrecognised" is not.
func TestImportNamesAFormatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.yaml")
	if err := os.WriteFile(path, []byte("asyncapi: 2.6.0\ninfo:\n  title: events\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, ".", "import", path)
	if code == 0 {
		t.Fatalf("import must fail when no frontend can read the source:\n%s", out)
	}
	if !strings.Contains(out, "AsyncAPI") {
		t.Fatalf("the format should still be named:\n%s", out)
	}
}
