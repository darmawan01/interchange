package openapi

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/reporter"
	"github.com/darmawan01/interchange"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// Options.Deps is where a caller hands over the descriptors its document
	// references. These blank imports are the fallback for a caller that
	// passes none -- core's own protos and upstream's google.api, nothing
	// from an optional module: a frontend that linked /auth in to emit an
	// auth annotation would make the optional module mandatory.
	_ "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	_ "google.golang.org/genproto/googleapis/api/annotations"
)

// compile turns emitted .proto source into descriptors. The artifact and the
// descriptors come from one code path on purpose: a hand-built descriptor set
// can disagree with the file that was committed, and then the reviewed text
// and the generated code describe different contracts.
func compile(ctx context.Context, files map[string][]byte, deps *protoregistry.Files) (*descriptorpb.FileDescriptorSet, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []string
	c := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(&memResolver{files: files, deps: deps}),
		SourceInfoMode: protocompile.SourceInfoStandard,
		Reporter: reporter.NewReporter(
			func(err reporter.ErrorWithPos) error { errs = append(errs, err.Error()); return err },
			nil,
		),
	}
	compiled, err := c.Compile(ctx, names...)
	if err != nil {
		if len(errs) > 0 {
			return nil, fmt.Errorf("emitted proto did not compile:\n  %s", strings.Join(errs, "\n  "))
		}
		return nil, fmt.Errorf("emitted proto did not compile: %w", err)
	}
	return fileSet(compiled)
}

// fileSet flattens the compiled files and every transitive dependency into one
// set, dependencies first. The order is a depth-first walk of a name-sorted
// input, so the same document always produces the same set.
func fileSet(files linker.Files) (*descriptorpb.FileDescriptorSet, error) {
	out := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor) error
	add = func(fd protoreflect.FileDescriptor) error {
		if seen[fd.Path()] {
			return nil
		}
		seen[fd.Path()] = true
		imports := fd.Imports()
		deps := make([]protoreflect.FileDescriptor, 0, imports.Len())
		for i := 0; i < imports.Len(); i++ {
			deps = append(deps, imports.Get(i).FileDescriptor)
		}
		sort.Slice(deps, func(i, j int) bool { return deps[i].Path() < deps[j].Path() })
		for _, d := range deps {
			if err := add(d); err != nil {
				return err
			}
		}
		fdp, err := normalize(fd)
		if err != nil {
			return err
		}
		out.File = append(out.File, fdp)
		return nil
	}
	sorted := append(linker.Files(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path() < sorted[j].Path() })
	for _, fd := range sorted {
		if err := add(fd); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// normalize re-parses a descriptor against the global type registry. A
// compiler resolves an extension it was not linked against into a dynamicpb
// value, and proto.GetExtension on one of those does not return the annotation
// -- so a caller reading (interchange.auth.v1.auth) off the result would get
// nothing without this step.
func normalize(fd protoreflect.FileDescriptor) (*descriptorpb.FileDescriptorProto, error) {
	raw, err := proto.Marshal(protodesc.ToFileDescriptorProto(fd))
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", fd.Path(), err)
	}
	var out descriptorpb.FileDescriptorProto
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("reparse %s: %w", fd.Path(), err)
	}
	return &out, nil
}

// memResolver serves the emitted files from memory, the annotation imports out
// of the global registry, and anything else from Options.Deps -- which is how
// an adopter's own tree becomes resolvable without this package reading the
// filesystem.
type memResolver struct {
	files map[string][]byte
	deps  *protoregistry.Files
}

func (r *memResolver) FindFileByPath(path string) (protocompile.SearchResult, error) {
	if src, ok := r.files[path]; ok {
		return protocompile.SearchResult{Source: bytes.NewReader(src)}, nil
	}
	// The well-known types are the compiler's own. Serving a second copy out
	// of Deps -- which any FileDescriptorSet built with --include_imports
	// carries -- would put two descriptor.proto in one graph, and every
	// symbol in it collides with itself.
	if !strings.HasPrefix(path, "google/protobuf/") {
		if fd, err := r.deps.FindFileByPath(path); err == nil {
			return protocompile.SearchResult{Desc: fd}, nil
		}
	}
	if fd, err := protoregistry.GlobalFiles.FindFileByPath(path); err == nil {
		return protocompile.SearchResult{Desc: fd}, nil
	}
	return protocompile.SearchResult{}, fmt.Errorf("import %q is not available to the openapi frontend", path)
}

// resolvable reports whether an import the emitted file needs can be found.
// Checked while converting, so a missing annotation descriptor is reported at
// the annotation rather than as a compiler error against source the author
// never wrote.
func resolvable(deps *protoregistry.Files, path string) bool {
	if strings.HasPrefix(path, "google/protobuf/") {
		return true // protocompile carries the well-known types itself
	}
	if _, err := deps.FindFileByPath(path); err == nil {
		return true
	}
	_, err := protoregistry.GlobalFiles.FindFileByPath(path)
	return err == nil
}

// depRegistry indexes the descriptors the caller supplied. The linked-file and
// ordering hazards live in core, because this is the third frontend that would
// otherwise have written them itself -- and the second to get them wrong.
func depRegistry(opt interchange.Options) (*protoregistry.Files, error) {
	files, err := interchange.DepFiles(opt.Deps)
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	return files, nil
}
