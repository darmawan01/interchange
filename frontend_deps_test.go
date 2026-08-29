package interchange_test

import (
	"testing"

	"github.com/darmawan01/interchange"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestDepFilesDropsAlreadyLinkedFiles: every FileDescriptorSet built with
// --include_imports carries descriptor.proto, and building a second object for
// a path the compiler already has puts two of that file in one link -- at
// which point every symbol in it collides with itself.
func TestDepFilesDropsAlreadyLinkedFiles(t *testing.T) {
	linked := protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{linked}}

	files, err := interchange.DepFiles(set)
	if err != nil {
		t.Fatalf("a Deps set carrying a linked file must not fail: %v", err)
	}
	if n := files.NumFiles(); n != 0 {
		t.Fatalf("registered %d files; a linked file must be used as linked, not duplicated", n)
	}
}

// TestDepFilesResolvesOutOfOrder: a set is conventionally ordered deps-first,
// and conventionally is not always.
func TestDepFilesResolvesOutOfOrder(t *testing.T) {
	base := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("acme/base.proto"),
		Package: proto.String("acme"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Id"),
		}},
	}
	dependent := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("acme/user.proto"),
		Package:    proto.String("acme"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"acme/base.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("User"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("id"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				TypeName: proto.String(".acme.Id"),
				JsonName: proto.String("id"),
			}},
		}},
	}

	// Dependent first, which is the order that fails a single pass.
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{dependent, base}}
	files, err := interchange.DepFiles(set)
	if err != nil {
		t.Fatalf("out-of-order Deps must still resolve: %v", err)
	}
	if _, err := files.FindDescriptorByName("acme.User"); err != nil {
		t.Fatalf("acme.User did not register: %v", err)
	}
}

// TestDepFilesReportsAGenuinelyMissingImport: making no progress is the only
// honest place to stop, and the error has to name the file.
func TestDepFilesReportsAGenuinelyMissingImport(t *testing.T) {
	orphan := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("acme/orphan.proto"),
		Package:    proto.String("acme"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"acme/nowhere.proto"},
	}
	_, err := interchange.DepFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{orphan},
	})
	if err == nil {
		t.Fatal("a Deps set with an unresolvable import must fail")
	}
}
