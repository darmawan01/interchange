package authgen_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darmawan01/interchange/auth"
	"github.com/darmawan01/interchange/auth/internal/authgen"
	"github.com/darmawan01/interchange/auth/internal/fixture"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

var update = flag.Bool("update", false, "rewrite the golden file")

// run drives the plugin exactly as protoc does: a CodeGeneratorRequest in,
// a CodeGeneratorResponse out, with the parameter string parsed by the same
// ParamFunc the binary installs.
func run(t *testing.T, parameter string, files ...string) (*pluginpb.CodeGeneratorResponse, error) {
	t.Helper()
	req, err := fixture.Request(parameter, files...)
	if err != nil {
		t.Fatal(err)
	}
	opts := authgen.DefaultOptions()
	opts.Warn = &strings.Builder{}
	p, err := (protogen.Options{ParamFunc: opts.Set}).New(req)
	if err != nil {
		return nil, err
	}
	if err := authgen.Generate(p, opts); err != nil {
		return nil, err
	}
	return p.Response(), nil
}

func content(t *testing.T, resp *pluginpb.CodeGeneratorResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("plugin reported %s", resp.GetError())
	}
	if len(resp.File) != 1 {
		t.Fatalf("plugin emitted %d files, want 1", len(resp.File))
	}
	return resp.File[0].GetContent()
}

// TestGolden pins the emitted table. It is the artefact an edge gateway
// compiles against, so a change to it should be a change somebody chose.
func TestGolden(t *testing.T) {
	resp, err := run(t, "", "permsvc.proto")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got := content(t, resp)
	if name := resp.File[0].GetName(); name != "permissions.authz.go" {
		t.Fatalf("emitted %q", name)
	}

	golden := filepath.Join("testdata", "permissions.authz.go.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("generated output differs from %s:\n--- got ---\n%s", golden, got)
	}
}

// TestDeterminism is the property the drift gate rests on: same input, same
// bytes. Map iteration is random and protoc's file order is not ours to
// depend on, so this is a real risk rather than a formality.
func TestDeterminism(t *testing.T) {
	first, err := run(t, "", "permsvc.proto")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := run(t, "", "permsvc.proto")
		if err != nil {
			t.Fatal(err)
		}
		if content(t, first) != content(t, again) {
			t.Fatal("two runs over identical input produced different bytes")
		}
	}
}

// TestMissingAnnotationFailsTheBuild is the exit criterion: CI can be
// configured to fail on an RPC with no (auth) annotation. Failing is the
// point -- a warning in a build log is a warning nobody reads.
func TestMissingAnnotationFailsTheBuild(t *testing.T) {
	_, err := run(t, "", "badsvc.proto")
	if err == nil {
		t.Fatal("an unannotated RPC must fail the build under the default policy")
	}
	if !strings.Contains(err.Error(), "BadService.Forgotten") {
		t.Fatalf("the error must name the RPC, got %v", err)
	}
	if !strings.Contains(err.Error(), "public: true") {
		t.Fatalf("the error must say how to opt out, got %v", err)
	}
}

// TestMissingAnnotationPolicyIsConfigurable: the same strictness knob the
// interceptor reads, spelled as a plugin parameter, because the two halves of
// "enforce twice" have to agree about what counts as annotated.
func TestMissingAnnotationPolicyIsConfigurable(t *testing.T) {
	for _, policy := range []auth.Strictness{auth.StrictWarn, auth.StrictIgnore} {
		resp, err := run(t, "on_missing_annotation="+string(policy), "badsvc.proto")
		if err != nil {
			t.Fatalf("policy %s: %v", policy, err)
		}
		out := content(t, resp)
		if !strings.Contains(out, "/interchange.authtest.bad.v1.BadService/Annotated") {
			t.Fatalf("policy %s dropped the annotated RPC:\n%s", policy, out)
		}
		// The unannotated RPC gets no row: a gateway reading this table must
		// treat a procedure it cannot find as denied.
		if strings.Contains(out, "Forgotten") {
			t.Fatalf("policy %s emitted a row for an unannotated RPC:\n%s", policy, out)
		}
	}
	if _, err := run(t, "on_missing_annotation=lenient", "permsvc.proto"); err == nil {
		t.Fatal("an unreadable policy value must fail rather than pick a default")
	}
}

// TestUnknownPermissionFailsTheBuild: an annotation that is present and wrong
// was reviewed and believed, so it is an error under every policy.
func TestUnknownPermissionFailsTheBuild(t *testing.T) {
	for _, policy := range []string{"", "on_missing_annotation=warn", "on_missing_annotation=ignore"} {
		_, err := run(t, policy, "typosvc.proto")
		if err == nil {
			t.Fatalf("policy %q: a permission with no verb must fail the build", policy)
		}
		if !strings.Contains(err.Error(), "TypoService.Broken") {
			t.Fatalf("the error must name the RPC, got %v", err)
		}
	}
}

// TestKnownAtomsClosesTheSet: a tree that keeps a permission catalogue can
// hand it to the plugin, and an atom outside it is a typo caught at build
// time rather than a permission nobody ever grants.
func TestKnownAtomsClosesTheSet(t *testing.T) {
	all := "known_atoms=providers.read+providers.create+providers.edit"
	if _, err := run(t, all, "permsvc.proto"); err != nil {
		t.Fatalf("every declared atom is in the set: %v", err)
	}
	_, err := run(t, "known_atoms=providers.read", "permsvc.proto")
	if err == nil {
		t.Fatal("an atom outside the known set must fail the build")
	}
	if !strings.Contains(err.Error(), "providers.create") {
		t.Fatalf("the error must name the unknown atom, got %v", err)
	}
	if _, err := run(t, "known_atoms=providers.reed", "permsvc.proto"); err == nil {
		t.Fatal("a misspelt atom in the known set must fail rather than silently match nothing")
	}
}

// TestUnknownParameterFails: a mistyped plugin option is a build failure, not
// a silently ignored one.
func TestUnknownParameterFails(t *testing.T) {
	if _, err := run(t, "on_missing=error", "permsvc.proto"); err == nil {
		t.Fatal("an unknown plugin parameter must fail")
	}
}

// TestImportsAreNotGenerated: p.Files carries every transitive import, so
// without the file.Generate guard the table would carry rows for the
// annotation protos themselves.
func TestImportsAreNotGenerated(t *testing.T) {
	resp, err := run(t, "", "permsvc.proto")
	if err != nil {
		t.Fatal(err)
	}
	out := content(t, resp)
	if strings.Contains(out, "interchange/auth/v1/auth.proto") || strings.Contains(out, "descriptor.proto") {
		t.Fatalf("generated file mentions an import it was not asked to generate:\n%s", out)
	}
}
