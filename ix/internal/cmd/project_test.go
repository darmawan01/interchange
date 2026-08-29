package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run drives the real command tree in-process with output captured, which is
// how every command test here asserts on what a user would see.
func run(t *testing.T, dir string, args ...string) (out string, code int) {
	t.Helper()
	var buf bytes.Buffer
	g := &globals{ui: &UI{Out: &buf, Err: &buf}}
	root := newRoot("ix", g)
	root.SetOut(&buf)
	root.SetErr(&buf)
	// -C rather than setting g.dir: cobra assigns a flag's default to its
	// bound variable when the flag is defined, so anything set beforehand is
	// overwritten.
	root.SetArgs(append([]string{"-C", dir}, args...))

	err := root.Execute()
	code = 0
	if err != nil {
		var ec *exitCode
		if errors.As(err, &ec) {
			code = ec.code
			if ec.err != nil && !errors.Is(ec.err, errSilent) {
				buf.WriteString(ec.err.Error() + "\n")
			}
		} else {
			code = 1
			buf.WriteString(err.Error() + "\n")
		}
	}
	return buf.String(), code
}

// The repo this ix lives in is not itself an interchange project: there is no
// interchange.yaml at its root. doctor must still exit 0 there, because a
// directory that is not a project yet is not a broken setup -- it reports on
// the toolchain and stops.
func TestDoctorOnTheRepo(t *testing.T) {
	requireBuf(t)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out, code := run(t, root, "doctor")
	if code != 0 {
		t.Fatalf("doctor exited %d:\n%s", code, out)
	}
	for _, want := range []string{"✓ buf", "✓ go", "✓ ix"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor did not report %q:\n%s", want, out)
		}
	}
}

// doctor on a real project checks everything, and exits 0 when it is sound.
func TestDoctorOnAFixture(t *testing.T) {
	requireBuf(t)
	dir := testdata(t, "badband")
	out, _ := run(t, dir, "doctor")
	for _, want := range []string{"interchange.yaml", "band", "contract"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor did not report %q:\n%s", want, out)
		}
	}
}

func TestDescribeCommandRejectsUnknownRPC(t *testing.T) {
	requireBuf(t)
	out, code := run(t, testdata(t, "fixture"), "describe", "CatalogService.Nope")
	if code == 0 {
		t.Fatalf("describe of an unknown RPC succeeded:\n%s", out)
	}
	if !strings.Contains(out, "known RPCs") {
		t.Errorf("the error does not list what is available:\n%s", out)
	}
}

// `ix dev call` with no server running starts its own loopback, so a user can
// exercise a contract with one command and no infrastructure.
func TestDevCallStandalone(t *testing.T) {
	requireBuf(t)
	out, code := run(t, testdata(t, "fixture"), "dev", "call",
		"CatalogService.ListProviders", `{"pageSize":3}`)
	if code != 0 {
		t.Fatalf("dev call exited %d:\n%s", code, out)
	}
	// The response is default-valued -- there is no handler. What it proves
	// is that the request decoded and the contract dispatched.
	for _, want := range []string{"providers", "nextPageToken"} {
		if !strings.Contains(out, want) {
			t.Errorf("the response does not carry %q:\n%s", want, out)
		}
	}
}

// An internal RPC is skipped by every public binding, and the loopback is a
// public binding for this purpose.
func TestDevRefusesInternalRPC(t *testing.T) {
	requireBuf(t)
	out, code := run(t, testdata(t, "fixture"), "dev", "call", "CatalogService.DrainProvider", "{}")
	if code == 0 {
		t.Fatalf("the loopback served an (internal) RPC:\n%s", out)
	}
}

func TestImportDetectsFormat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "payments.yaml")
	if err := os.WriteFile(p, []byte("openapi: 3.0.3\npaths: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, dir, "import", p)
	if code == 0 {
		t.Errorf("import claimed success with no frontend wired:\n%s", out)
	}
	for _, want := range []string{"OpenAPI 3.0.3", "openapi", "nothing written"} {
		if !strings.Contains(out, want) {
			t.Errorf("import output missing %q:\n%s", want, out)
		}
	}
}

// Both binary names exist: docs/08 settled on ix with interchange as an alias.
func TestBothBinariesBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	for _, pkg := range []string{"./cmd/ix", "./cmd/interchange"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "bin"), pkg)
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", pkg, err, out)
		}
	}
}
