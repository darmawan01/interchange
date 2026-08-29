package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// networkSkip turns a remote-plugin failure into a skip. Everything up to
// `ix generate` works offline by design; generate itself pulls remote
// plugins, and a sandbox without egress cannot be asked to.
func networkSkip(t *testing.T, out string, code int) {
	t.Helper()
	if code == 0 {
		return
	}
	for _, sign := range []string{
		"dial tcp", "no such host", "connection refused", "i/o timeout",
		"TLS handshake", "context deadline exceeded", "Unavailable", "connect: ",
		"failed to resolve", "network is unreachable", "proxy",
		// BSR runs remote plugins server-side, so its rate limiter is a
		// network condition like any other.
		"too many requests", "resource_exhausted",
	} {
		if strings.Contains(out, sign) {
			t.Skipf("buf could not reach the remote plugin registry; this test needs network access:\n%s", out)
		}
	}
}

// The design goal is `ix init` to a generated, typed client in under a
// minute. This is that claim as a test: scaffold, lint, generate, verify.
func TestInitRoundTrip(t *testing.T) {
	requireBuf(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := run(t, dir, "init", "--name", "todo")
	if code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	for _, f := range []string{
		"interchange.yaml", "buf.yaml", "buf.gen.yaml", "Makefile",
		".github/workflows/interchange.yml",
		"api/todo/v1/todo.proto",
		"api/interchange/transport/v1/transports.proto",
		"api/interchange/cli/v1/cli.proto",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("init did not write %s", f)
		}
	}

	// Lint must pass with no network at all: the scaffold vendors every
	// annotation proto it uses precisely so this is true.
	out, code = run(t, dir, "lint")
	if code != 0 {
		t.Fatalf("lint on a fresh scaffold exited %d:\n%s", code, out)
	}

	// The scaffold is written in buf's canonical form, so a fresh project is
	// already format-clean -- a first `ix fmt` that rewrites the file you were
	// just given reads as a bug.
	if out, code := run(t, dir, "fmt", "--check"); code != 0 {
		t.Errorf("a fresh scaffold is not buf-formatted:\n%s", out)
	}

	out, code = run(t, dir, "describe", "TodoService.ListTodos")
	if code != 0 {
		t.Fatalf("describe on a fresh scaffold exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "/todo.v1.TodoService/ListTodos") {
		t.Errorf("describe did not resolve the starter RPC:\n%s", out)
	}

	out, code = run(t, dir, "generate")
	networkSkip(t, out, code)
	if code != 0 {
		t.Fatalf("generate exited %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "gen", "go", "todo", "v1", "todo.pb.go")); err != nil {
		t.Fatalf("generate produced no Go: %v", err)
	}

	out, code = run(t, dir, "verify")
	if code != 0 {
		t.Fatalf("verify on freshly generated output exited %d:\n%s", code, out)
	}
	for _, want := range []string{"✓ frontends", "✓ annotations", "✓ generators", "✓ drift"} {
		if !strings.Contains(out, want) {
			t.Errorf("verify did not report %q:\n%s", want, out)
		}
	}
}

// The gate exists to catch exactly this: generated output that no longer
// matches the contract.
func TestVerifyDetectsDrift(t *testing.T) {
	requireBuf(t)
	dir := t.TempDir()

	if out, code := run(t, dir, "init", "--name", "todo", "--go-module", "example.com/demo"); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	out, code := run(t, dir, "generate")
	networkSkip(t, out, code)
	if code != 0 {
		t.Fatalf("generate exited %d:\n%s", code, out)
	}

	target := filepath.Join(dir, "gen", "go", "todo", "v1", "todo.pb.go")
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(b, []byte("\n// hand-edited\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code = run(t, dir, "verify")
	// verify regenerates, so it reaches the registry too -- and a rate limit
	// there is not a drift failure.
	networkSkip(t, out, code)
	if code == 0 {
		t.Fatalf("verify passed on a hand-edited generated file:\n%s", out)
	}
	if !strings.Contains(out, "✗ drift") {
		t.Errorf("verify did not report drift:\n%s", out)
	}
	if !strings.Contains(out, "gen/go/todo/v1/todo.pb.go differs") {
		t.Errorf("verify did not name the file that moved:\n%s", out)
	}
	if !strings.Contains(out, "run `ix generate`") {
		t.Errorf("verify did not say what to do about it:\n%s", out)
	}

	// A deleted file is drift too: the committed tree is missing something
	// the contract still produces.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	out, code = run(t, dir, "verify")
	if code == 0 || !strings.Contains(out, "is missing") {
		t.Errorf("verify did not notice a deleted generated file (exit %d):\n%s", code, out)
	}
}

// buf.gen.yaml exists so `buf generate` works without ix. If it drifts from
// interchange.yaml the two files disagree about what CI generates, which is
// the same failure the gate exists to stop.
func TestVerifyDetectsTemplateDrift(t *testing.T) {
	requireBuf(t)
	dir := t.TempDir()
	if out, code := run(t, dir, "init", "--name", "todo"); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	if out, code := run(t, dir, "plugin", "add", "./bin/protoc-gen-mysdk", "--out", "gen/sdk"); code != 0 {
		t.Fatalf("plugin add exited %d:\n%s", code, out)
	}
	out, code := run(t, dir, "verify")
	if code == 0 || !strings.Contains(out, "✗ template") {
		t.Errorf("verify did not notice buf.gen.yaml drift (exit %d):\n%s", code, out)
	}
	if out, code := run(t, dir, "plugin", "sync"); code != 0 {
		t.Fatalf("plugin sync exited %d:\n%s", code, out)
	}
	if out, _ := run(t, dir, "verify"); strings.Contains(out, "✗ template") {
		t.Errorf("plugin sync did not fix the template:\n%s", out)
	}
}

// `ix plugin` edits interchange.yaml in place. A tool that eats the comments
// every time it touches the file is a tool people stop letting near it.
func TestPluginEditsPreserveComments(t *testing.T) {
	requireBuf(t)
	dir := t.TempDir()
	if out, code := run(t, dir, "init", "--name", "todo"); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	if out, code := run(t, dir, "plugin", "pin", "buf.build/protocolbuffers/go", "v1.36.0"); code != 0 {
		t.Fatalf("plugin pin exited %d:\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "interchange.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "buf.build/protocolbuffers/go:v1.36.0") {
		t.Errorf("the pin was not written:\n%s", got)
	}
	if !strings.Contains(got, "# Authorization is an optional module") {
		t.Errorf("the edit dropped the file's comments:\n%s", got)
	}
}
