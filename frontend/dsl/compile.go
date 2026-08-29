package dsl

import (
	"bytes"
	"context"
	"sort"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	// The annotation descriptors have to be linked in: a frontend is handed
	// its sources, never a filesystem, so the only place the definition of
	// (interchange.auth.v1.auth) can come from is the descriptor registry of
	// the process doing the import.
	_ "google.golang.org/genproto/googleapis/api/annotations"

	_ "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	_ "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	_ "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	_ "github.com/darmawan01/interchange/tools/gen/go/interchange/cli/v1"
)

// compile turns the emitted .proto source into descriptors. Going through a
// real compiler rather than assembling FileDescriptorProtos by hand is what
// makes the output *canonical*: option values are resolved by the same code
// protoc uses, so a DSL user's descriptors are indistinguishable from a proto
// user's.
func compile(ctx context.Context, sources map[string][]byte) (*descriptorpb.FileDescriptorSet, error) {
	paths := make([]string, 0, len(sources))
	for p := range sources {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	res := protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
		if b, ok := sources[path]; ok {
			return protocompile.SearchResult{Source: bytes.NewReader(b)}, nil
		}
		fd, err := protoregistry.GlobalFiles.FindFileByPath(path)
		if err != nil {
			return protocompile.SearchResult{}, protoregistry.NotFound
		}
		return protocompile.SearchResult{Desc: fd}, nil
	})

	c := protocompile.Compiler{
		Resolver:       protocompile.WithStandardImports(res),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := c.Compile(ctx, paths...)
	if err != nil {
		return nil, err
	}

	// Dependencies first, then the files themselves: the order protoc emits
	// with --include_imports, and the order that lets a consumer build a
	// registry in one pass.
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imps := fd.Imports()
		deps := make([]protoreflect.FileDescriptor, 0, imps.Len())
		for i := range imps.Len() {
			deps = append(deps, imps.Get(i).FileDescriptor)
		}
		sort.Slice(deps, func(i, j int) bool { return deps[i].Path() < deps[j].Path() })
		for _, d := range deps {
			add(d)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
	}
	for _, p := range paths {
		add(files.FindFileByPath(p))
	}
	return set, nil
}
