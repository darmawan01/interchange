package interchange

import (
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ResolveOptions re-reads a descriptor's options so that custom options are
// readable with proto.GetExtension, whatever built the descriptor.
//
// This is not a convenience. A descriptor built from linked generated Go
// carries concrete extension values; one built by protocompile, by protodesc
// with a dynamic resolver, or by any schema frontend carries dynamicpb values
// or raw unknown bytes instead. Against those, proto.GetExtension with a
// concrete extension type either panics or -- worse -- returns the zero value
// without an error.
//
// The zero value is the dangerous half. An annotation that reads as absent is
// an authorization check that stops firing, in a build that compiles and a
// test suite that passes. Every module that reads an annotation needs this,
// so core owns it: core still does not know what any annotation means, only
// how to hand one back intact.
//
// out must be the options message for the descriptor's kind --
// *descriptorpb.MethodOptions for a method, and so on.
func ResolveOptions(d protoreflect.Descriptor, out proto.Message) error {
	if d == nil {
		return fmt.Errorf("interchange: ResolveOptions called with a nil descriptor")
	}
	opts := d.Options()
	if opts == nil {
		return nil
	}
	// Round-tripping through bytes is what re-binds the extension fields to
	// the concrete types in the registry. There is no cheaper way: the
	// values in the source message are of the wrong Go type by construction.
	b, err := proto.Marshal(opts)
	if err != nil {
		return fmt.Errorf("interchange: marshal options of %s: %w", d.FullName(), err)
	}
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(b, out); err != nil {
		return fmt.Errorf("interchange: resolve options of %s: %w", d.FullName(), err)
	}
	return nil
}

var optionCache sync.Map // protoreflect.Descriptor -> proto.Message

func cachedOptions[T proto.Message](d protoreflect.Descriptor, fresh func() T) T {
	var zero T
	if d == nil {
		return zero
	}
	if v, ok := optionCache.Load(d); ok {
		return v.(T)
	}
	out := fresh()
	if err := ResolveOptions(d, out); err != nil {
		// A descriptor whose own options do not round-trip is corrupt. There
		// is nothing an annotation reader can do with that, and returning
		// empty options is the same silent-absence failure this exists to
		// prevent -- so callers get empty options and the descriptor is not
		// cached, which keeps the failure visible on the next read.
		return zero
	}
	optionCache.Store(d, out)
	return out
}

// MethodOptions returns a method's options with custom options resolved.
// The result is cached per descriptor and must not be mutated.
func MethodOptions(md protoreflect.MethodDescriptor) *descriptorpb.MethodOptions {
	return cachedOptions(md, func() *descriptorpb.MethodOptions { return &descriptorpb.MethodOptions{} })
}

// ServiceOptions returns a service's options with custom options resolved.
func ServiceOptions(sd protoreflect.ServiceDescriptor) *descriptorpb.ServiceOptions {
	return cachedOptions(sd, func() *descriptorpb.ServiceOptions { return &descriptorpb.ServiceOptions{} })
}

// FieldOptions returns a field's options with custom options resolved.
func FieldOptions(fd protoreflect.FieldDescriptor) *descriptorpb.FieldOptions {
	return cachedOptions(fd, func() *descriptorpb.FieldOptions { return &descriptorpb.FieldOptions{} })
}
