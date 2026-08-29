// Package fixture compiles this module's test contract at test time.
//
// The tests need a REAL protoreflect.MethodDescriptor carrying a real (auth)
// annotation: a hand-built descriptor would test the test rather than the
// annotation path. Compiling with protocompile keeps the fixture out of the
// module's public proto tree -- auth/api ships the annotation, not a test
// service -- while still exercising the descriptor exactly as generated code
// hands it to core.
package fixture

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bufbuild/protocompile"
	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// Dir is the directory holding the fixture .proto files. It is resolved from
// this file's own path so a test in another package can compile them without
// caring where it was run from.
var Dir = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata")
}()

// Compile compiles fixture files named relative to Dir and returns their
// descriptors with every custom option resolved to this module's generated
// types.
//
// The normalisation matters: a compiler resolves an extension it was not
// linked against into a dynamicpb value, and proto.GetExtension on one of
// those panics rather than returning the annotation. Re-parsing the descriptor
// against the global type registry is what turns it back into an
// *authv1.AuthOptions.
func Compile(names ...string) ([]protoreflect.FileDescriptor, error) {
	c := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(&resolver{}),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	compiled, err := c.Compile(context.Background(), names...)
	if err != nil {
		return nil, fmt.Errorf("fixture: compile %v: %w", names, err)
	}

	files := new(protoregistry.Files)
	out := make([]protoreflect.FileDescriptor, 0, len(compiled))
	for _, fd := range compiled {
		norm, err := normalize(fd, &fallbackResolver{local: files})
		if err != nil {
			return nil, err
		}
		if err := files.RegisterFile(norm); err != nil {
			return nil, fmt.Errorf("fixture: register %s: %w", fd.Path(), err)
		}
		out = append(out, norm)
	}
	return out, nil
}

// MustCompile is Compile for test setup that would only fail on the next line
// anyway.
func MustCompile(names ...string) []protoreflect.FileDescriptor {
	fds, err := Compile(names...)
	if err != nil {
		panic(err)
	}
	return fds
}

func normalize(fd protoreflect.FileDescriptor, res protodesc.Resolver) (protoreflect.FileDescriptor, error) {
	raw, err := proto.Marshal(protodesc.ToFileDescriptorProto(fd))
	if err != nil {
		return nil, fmt.Errorf("fixture: marshal %s: %w", fd.Path(), err)
	}
	var fdp descriptorpb.FileDescriptorProto
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, &fdp); err != nil {
		return nil, fmt.Errorf("fixture: reparse %s: %w", fd.Path(), err)
	}
	out, err := protodesc.NewFile(&fdp, res)
	if err != nil {
		return nil, fmt.Errorf("fixture: rebuild %s: %w", fd.Path(), err)
	}
	return out, nil
}

// Request builds the CodeGeneratorRequest a protoc plugin would receive for
// these files: every transitive dependency in the proto_file list, dependencies
// first, and only the named files marked for generation.
func Request(parameter string, names ...string) (*pluginpb.CodeGeneratorRequest, error) {
	fds, err := Compile(names...)
	if err != nil {
		return nil, err
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: append([]string(nil), names...),
		Parameter:      proto.String(parameter),
	}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			add(imports.Get(i).FileDescriptor)
		}
		req.ProtoFile = append(req.ProtoFile, protodesc.ToFileDescriptorProto(fd))
	}
	for _, fd := range fds {
		add(fd)
	}
	return req, nil
}

// Handler is the fixture service implementation: one func for every method, so
// a test can decide what "the handler ran" means without a generated stub.
type Handler func(ctx context.Context, procedure string, req proto.Message) (proto.Message, error)

// ServiceDesc builds the descriptor generated code would emit for a service,
// with dynamicpb standing in for generated message types. MethodDesc.Desc is
// the point of the exercise -- it is where the auth module reads its own
// annotation from.
func ServiceDesc(sd protoreflect.ServiceDescriptor, transports []transportv1.Transport) *interchange.ServiceDesc {
	out := &interchange.ServiceDesc{Name: string(sd.FullName()), Desc: sd}
	methods := sd.Methods()
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		in, outMsg := m.Input(), m.Output()
		procedure := interchange.Procedure(string(sd.FullName()), string(m.Name()))
		out.Methods = append(out.Methods, interchange.MethodDesc{
			Name:        string(m.Name()),
			Service:     string(sd.FullName()),
			Procedure:   procedure,
			NewRequest:  func() proto.Message { return dynamicpb.NewMessage(in) },
			NewResponse: func() proto.Message { return dynamicpb.NewMessage(outMsg) },
			Transports:  transports,
			Desc:        m,
			Handler: func(ctx context.Context, impl any, req proto.Message) (proto.Message, error) {
				return impl.(Handler)(ctx, procedure, req)
			},
		})
	}
	return out
}

// Service finds a service by name across compiled files.
func Service(fds []protoreflect.FileDescriptor, name protoreflect.FullName) (protoreflect.ServiceDescriptor, error) {
	for _, fd := range fds {
		if sd := fd.Services().ByName(name.Name()); sd != nil && sd.FullName() == name {
			return sd, nil
		}
	}
	return nil, fmt.Errorf("fixture: no service %s in the compiled files", name)
}

// resolver serves fixture sources off disk and every import out of the global
// registry, which is where interchange/auth/v1/auth.proto already lives
// because this module links its own generated code.
type resolver struct{}

func (r *resolver) FindFileByPath(path string) (protocompile.SearchResult, error) {
	if fd, err := protoregistry.GlobalFiles.FindFileByPath(path); err == nil {
		return protocompile.SearchResult{Desc: fd}, nil
	}
	src, err := os.ReadFile(filepath.Join(Dir, path))
	if err != nil {
		return protocompile.SearchResult{}, err
	}
	return protocompile.SearchResult{Source: bytes.NewReader(src)}, nil
}

// fallbackResolver rebuilds a fixture file against the files already rebuilt in
// this pass, falling back to the global registry for the annotation protos.
type fallbackResolver struct{ local *protoregistry.Files }

func (r *fallbackResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if fd, err := r.local.FindFileByPath(path); err == nil {
		return fd, nil
	}
	return protoregistry.GlobalFiles.FindFileByPath(path)
}

func (r *fallbackResolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d, err := r.local.FindDescriptorByName(name); err == nil {
		return d, nil
	}
	return protoregistry.GlobalFiles.FindDescriptorByName(name)
}
