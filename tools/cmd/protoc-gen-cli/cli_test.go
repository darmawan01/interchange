package main

import (
	"slices"
	"strings"
	"testing"

	cliv1 "github.com/darmawan01/interchange/tools/gen/go/interchange/cli/v1"
	"github.com/darmawan01/interchange/tools/internal/plugintest"
	fixturev1 "github.com/darmawan01/interchange/tools/testdata/gen/interchange/fixture/v1"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

func run(t *testing.T, cfg *config) (map[string]string, error) {
	t.Helper()
	req := plugintest.Request(t, "paths=source_relative",
		fixturev1.File_interchange_fixture_v1_extra_proto,
		fixturev1.File_interchange_fixture_v1_fixture_proto)
	return plugintest.Run(req, protogen.Options{ParamFunc: cfg.set}, func(p *protogen.Plugin) error {
		return generate(p, cfg)
	})
}

func mustRun(t *testing.T, cfg *config) map[string]string {
	t.Helper()
	files, err := run(t, cfg)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return files
}

func TestGolden(t *testing.T) {
	plugintest.Golden(t, "../../testdata/gen", mustRun(t, &config{}))
}

func TestOnlyFilesToGenerate(t *testing.T) {
	var names []string
	for n := range mustRun(t, &config{}) {
		names = append(names, n)
	}
	slices.Sort(names)
	want := []string{
		"interchange/fixture/v1/fixturev1cli/extra_cli.pb.go",
		"interchange/fixture/v1/fixturev1cli/fixture_cli.pb.go",
	}
	if !slices.Equal(names, want) {
		t.Errorf("emitted %v, want %v", names, want)
	}
}

func TestDeterministic(t *testing.T) {
	first, second := mustRun(t, &config{}), mustRun(t, &config{})
	for name, a := range first {
		if b := second[name]; a != b {
			t.Errorf("%s differs between runs", name)
		}
	}
}

// TestRequireAnnotation: the option turns the coverage gap the report merely
// describes into a build failure.
func TestRequireAnnotation(t *testing.T) {
	_, err := run(t, &config{requireAnnotation: true})
	if err == nil {
		t.Fatal("require_annotation=true accepted an unannotated RPC")
	}
	if !strings.Contains(err.Error(), "PlainService.Ping") {
		t.Errorf("error must name the offending RPC, got: %v", err)
	}
	if !strings.Contains(err.Error(), "skip: true") {
		t.Errorf("error must say how to declare the omission deliberate, got: %v", err)
	}
}

func TestParamFunc(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
		fail  bool
	}{
		{value: "", want: true},
		{value: "true", want: true},
		{value: "false", want: false},
		{value: "yes", fail: true},
	} {
		cfg := &config{}
		err := cfg.set("require_annotation", tc.value)
		switch {
		case tc.fail && err == nil:
			t.Errorf("require_annotation=%q: want an error", tc.value)
		case !tc.fail && err != nil:
			t.Errorf("require_annotation=%q: %v", tc.value, err)
		case !tc.fail && cfg.requireAnnotation != tc.want:
			t.Errorf("require_annotation=%q: got %v", tc.value, cfg.requireAnnotation)
		}
	}
	if err := (&config{}).set("nonsense", "1"); err == nil {
		t.Error("unknown options must fail rather than be ignored")
	}
}

// TestCoverageReport reads the emitted report: skipped is deliberate,
// missing is the hole.
func TestCoverageReport(t *testing.T) {
	src := mustRun(t, &config{})["interchange/fixture/v1/fixturev1cli/fixture_cli.pb.go"]
	for _, want := range []string{
		`Service: "interchange.fixture.v1.FixtureService"`,
		`"/interchange.fixture.v1.FixtureService/GetItem"`,
		`"/interchange.fixture.v1.FixtureService/Reindex"`, // skip: true
		`Service: "interchange.fixture.v1.PlainService"`,
		`"/interchange.fixture.v1.PlainService/Ping"`, // unannotated
	} {
		if !strings.Contains(src, want) {
			t.Errorf("coverage report is missing %s", want)
		}
	}
	if strings.Contains(src, `Use:   "reindex"`) {
		t.Error("a skipped RPC reached the command tree")
	}
}

// TestUnrepresentableFieldsAreNotFlags: repeated and message fields have no
// flag, and --request-json is how they are still reachable.
func TestUnrepresentableFieldsAreNotFlags(t *testing.T) {
	src := mustRun(t, &config{})["interchange/fixture/v1/fixturev1cli/fixture_cli.pb.go"]
	for _, unwanted := range []string{`"tags"`, `"nested"`} {
		if strings.Contains(src, unwanted) {
			t.Errorf("emitted a flag for %s, which no single token can carry", unwanted)
		}
	}
	if !strings.Contains(src, `"request-json"`) {
		t.Error("no --request-json escape hatch: the command is a lossy subset of the RPC")
	}
	// A positional argument must not also be a flag.
	if strings.Contains(src, `"id", "id"`) || strings.Contains(src, `StringVar(&flagId`) {
		t.Error("the id field is both a positional argument and a flag")
	}
}

func TestBadAnnotationsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		ann  *cliv1.CommandOptions
		want string
	}{
		{
			name: "no path",
			ann:  &cliv1.CommandOptions{Args: []string{"missing"}},
			want: "no path",
		},
		{
			name: "unknown arg field",
			ann:  &cliv1.CommandOptions{Path: []string{"a"}, Args: []string{"nope"}},
			want: "no field called",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := badRequest(t, tc.ann)
			if _, err := plugintest.Run(req, protogen.Options{}, func(p *protogen.Plugin) error {
				return generate(p, &config{})
			}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// badRequest builds a one-method service whose (command) annotation is
// whatever the test wants to be refused.
func badRequest(t *testing.T, ann *cliv1.CommandOptions) *pluginpb.CodeGeneratorRequest {
	t.Helper()
	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, cliv1.E_Command, ann)

	file := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("bad/v1/bad.proto"),
		Package:     proto.String("bad.v1"),
		Syntax:      proto.String("proto3"),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.com/gen/bad/v1;badv1")},
		Dependency:  []string{"interchange/cli/v1/cli.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Thing")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("BadService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Do"),
				InputType:  proto.String(".bad.v1.Thing"),
				OutputType: proto.String(".bad.v1.Thing"),
				Options:    opts,
			}},
		}},
	}
	base := plugintest.Request(t, "paths=source_relative",
		fixturev1.File_interchange_fixture_v1_fixture_proto)
	return &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{file.GetName()},
		Parameter:      proto.String("paths=source_relative"),
		ProtoFile:      append(dependenciesOf(base), file),
	}
}

// dependenciesOf returns every file of a request except the ones it
// generates, which is exactly the import closure a synthetic file needs.
func dependenciesOf(req *pluginpb.CodeGeneratorRequest) []*descriptorpb.FileDescriptorProto {
	generated := map[string]bool{}
	for _, n := range req.GetFileToGenerate() {
		generated[n] = true
	}
	var out []*descriptorpb.FileDescriptorProto
	for _, f := range req.GetProtoFile() {
		if !generated[f.GetName()] {
			out = append(out, f)
		}
	}
	return out
}

// TestShortFallsBackToComment: an annotation that sets only a path still
// produces a command with help text, because the .proto already documented
// the RPC. The fixture cannot cover this -- its descriptors come from
// generated Go, which carries no comments.
func TestShortFallsBackToComment(t *testing.T) {
	req := badRequest(t, &cliv1.CommandOptions{Path: []string{"do"}})
	for _, f := range req.GetProtoFile() {
		if f.GetName() != "bad/v1/bad.proto" {
			continue
		}
		f.SourceCodeInfo = &descriptorpb.SourceCodeInfo{
			Location: []*descriptorpb.SourceCodeInfo_Location{{
				Path:            []int32{6, 0, 2, 0},
				Span:            []int32{11, 2, 40},
				LeadingComments: proto.String(" Do the thing.\n Twice.\n"),
			}},
		}
	}
	files, err := plugintest.Run(req, protogen.Options{}, func(p *protogen.Plugin) error {
		return generate(p, &config{})
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	src := files["bad/v1/badv1cli/bad_cli.pb.go"]
	if !strings.Contains(src, `Short: "Do the thing."`) {
		t.Errorf("the RPC's documentation did not become the command's help:\n%s", src)
	}
}

// TestOptionsSurviveAnUnresolvedDescriptor: a (command) annotation read as
// absent would not fail the build, it would quietly drop the command -- and
// under require_annotation=true it would fail a build that is correct. Core's
// ResolveOptions is what stops both.
func TestOptionsSurviveAnUnresolvedDescriptor(t *testing.T) {
	req := plugintest.Request(t, "paths=source_relative",
		fixturev1.File_interchange_fixture_v1_extra_proto,
		fixturev1.File_interchange_fixture_v1_fixture_proto)
	generated := map[string]bool{}
	for _, n := range req.GetFileToGenerate() {
		generated[n] = true
	}
	for _, f := range req.GetProtoFile() {
		if !generated[f.GetName()] {
			continue
		}
		for _, s := range f.GetService() {
			for _, m := range s.GetMethod() {
				opts := m.GetOptions()
				if opts == nil {
					continue
				}
				b, err := proto.Marshal(opts)
				if err != nil {
					t.Fatal(err)
				}
				fresh := &descriptorpb.MethodOptions{}
				if err := (proto.UnmarshalOptions{Resolver: &protoregistry.Types{}}).Unmarshal(b, fresh); err != nil {
					t.Fatal(err)
				}
				m.Options = fresh
			}
		}
	}
	got, err := plugintest.Run(req, protogen.Options{}, func(p *protogen.Plugin) error {
		return generate(p, &config{})
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for name, want := range mustRun(t, &config{}) {
		if got[name] != want {
			t.Errorf("%s: an unresolved descriptor produced different code; the annotation was read as absent", name)
		}
	}
}
