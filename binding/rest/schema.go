package rest

import (
	"fmt"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rest/internal/protoann"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// restSchema is the transport annotation made structural. Vanguard routes
// every method of the schema it is given, so a method that carries an HTTP
// rule but does not declare this road has to be absent from the schema --
// filtering after the fact would mean the route existed and merely answered
// badly.
//
// It returns the filtered service descriptor and how many methods survived.
func restSchema(sd *interchange.ServiceDesc, on transportv1.Transport) (protoreflect.ServiceDescriptor, int, error) {
	keep := map[string]bool{}
	for i := range sd.Methods {
		md := &sd.Methods[i]
		if !md.ExposedOn(on) || md.Internal {
			continue
		}
		if md.Desc == nil {
			return nil, 0, fmt.Errorf("rest: %s carries no method descriptor", md.Procedure)
		}
		if _, ok := protoann.HTTPRule(md.Desc); !ok {
			return nil, 0, fmt.Errorf("rest: %s declares %s but carries no google.api.http rule, "+
				"so it has no URI to be reached at", md.Procedure, on)
		}
		keep[md.Name] = true
	}
	if len(keep) == 0 {
		return nil, 0, nil
	}

	file := sd.Desc.ParentFile()
	fdp := protodesc.ToFileDescriptorProto(file)
	var found bool
	for _, svc := range fdp.GetService() {
		if svc.GetName() != string(sd.Desc.Name()) {
			continue
		}
		found = true
		methods := svc.GetMethod()[:0:0]
		for _, m := range svc.GetMethod() {
			if keep[m.GetName()] {
				methods = append(methods, m)
			}
		}
		svc.Method = methods
	}
	if !found {
		return nil, 0, fmt.Errorf("rest: %s is not declared in %s", sd.Name, file.Path())
	}

	deps, err := dependencies(file)
	if err != nil {
		return nil, 0, err
	}
	rebuilt, err := protodesc.NewFile(fdp, deps)
	if err != nil {
		return nil, 0, fmt.Errorf("rest: rebuilding %s without its off-road methods: %w", file.Path(), err)
	}
	svc := rebuilt.Services().ByName(sd.Desc.Name())
	if svc == nil {
		return nil, 0, fmt.Errorf("rest: %s vanished from the rebuilt descriptor", sd.Name)
	}
	return svc, len(keep), nil
}

// dependencies collects the transitive imports of a file so protodesc can
// rebuild it. The global registry is not enough: a descriptor set loaded from
// a file at runtime is deliberately not registered globally, since its
// well-known imports would collide with the linked-in ones.
func dependencies(file protoreflect.FileDescriptor) (*protoregistry.Files, error) {
	files := new(protoregistry.Files)
	var add func(protoreflect.FileDescriptor) error
	add = func(f protoreflect.FileDescriptor) error {
		if f.IsPlaceholder() {
			return fmt.Errorf("rest: %s imports %s, which is not in the descriptor set", file.Path(), f.Path())
		}
		if _, err := files.FindFileByPath(f.Path()); err == nil {
			return nil
		}
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			if err := add(imports.Get(i).FileDescriptor); err != nil {
				return err
			}
		}
		return files.RegisterFile(f)
	}
	imports := file.Imports()
	for i := 0; i < imports.Len(); i++ {
		if err := add(imports.Get(i).FileDescriptor); err != nil {
			return nil, err
		}
	}
	return files, nil
}
