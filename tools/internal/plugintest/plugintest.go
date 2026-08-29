// Package plugintest drives a protoc plugin the way protoc does, from
// descriptors that are already linked into the test binary.
//
// The fixture's generated Go carries its own descriptor, so the golden tests
// need no compiler and no committed .binpb: the input to the generator is the
// same artefact the fixture package registers at init, which means the golden
// files cannot drift from the .proto without the fixture failing to build.
package plugintest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// Request builds a CodeGeneratorRequest that generates the given files and
// carries their transitive imports, dependencies first -- the order protoc
// guarantees and protogen relies on.
func Request(t testing.TB, param string, generate ...protoreflect.FileDescriptor) *pluginpb.CodeGeneratorRequest {
	t.Helper()

	var (
		seen  = map[string]bool{}
		files []*descriptorpb.FileDescriptorProto
	)
	var walk func(fd protoreflect.FileDescriptor)
	walk = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			imp := imports.Get(i)
			if imp.IsPlaceholder() {
				t.Fatalf("plugintest: %s imports %s, which is not linked into the test binary", fd.Path(), imp.Path())
			}
			walk(imp.FileDescriptor)
		}
		files = append(files, protodesc.ToFileDescriptorProto(fd))
	}

	var names []string
	for _, fd := range generate {
		walk(fd)
		names = append(names, fd.Path())
	}

	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: names,
		Parameter:      proto.String(param),
		ProtoFile:      files,
	}
	// The round trip is not ceremony: it is where option extensions stop
	// being unknown bytes and become the typed annotations the plugin reads,
	// which is exactly what happens at the real protoc boundary.
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("plugintest: marshal request: %v", err)
	}
	out := &pluginpb.CodeGeneratorRequest{}
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatalf("plugintest: unmarshal request: %v", err)
	}
	return out
}

// Run invokes a plugin and returns its output files by name.
func Run(req *pluginpb.CodeGeneratorRequest, opts protogen.Options, run func(*protogen.Plugin) error) (map[string]string, error) {
	p, err := opts.New(req)
	if err != nil {
		return nil, err
	}
	p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
	if err := run(p); err != nil {
		return nil, err
	}
	resp := p.Response()
	if resp.Error != nil {
		return nil, errors.New(resp.GetError())
	}
	out := map[string]string{}
	for _, f := range resp.GetFile() {
		out[f.GetName()] = f.GetContent()
	}
	return out, nil
}

// Golden compares generated output against the committed tree under
// tools/testdata/gen. The goldens are real .go files in a real package, so
// `go build ./...` proves the generator emits code that compiles against core
// -- something a .golden blob can never prove.
//
// Rewrite them with UPDATE_GOLDEN=1 go test ./...
func Golden(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if len(files) == 0 {
		t.Fatal("plugintest: generator produced no files")
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("golden %s: %v (run UPDATE_GOLDEN=1 go test ./...)", p, err)
		}
		if string(want) != content {
			t.Errorf("golden %s differs from generated output; run UPDATE_GOLDEN=1 go test ./...", p)
		}
	}
}
