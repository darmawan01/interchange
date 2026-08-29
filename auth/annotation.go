package auth

import (
	"sync"

	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// AnnotationOf decodes the (auth) option off a method descriptor. This is the
// whole of the module's privileged access to core: a protoreflect descriptor
// that core carries but never reads.
//
// A nil descriptor decodes to an absent annotation rather than to a panic --
// hand-written or partially generated service code has no descriptor, and
// under the default policy the absent annotation denies it.
func AnnotationOf(m protoreflect.MethodDescriptor) Annotation {
	if m == nil {
		return Annotation{}
	}
	opts, ok := m.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return Annotation{}
	}
	return annotationFromOptions(opts)
}

func annotationFromOptions(opts *descriptorpb.MethodOptions) Annotation {
	// Re-parse rather than read the extension in place. A descriptor built by
	// a compiler or a schema frontend resolves the option into a dynamicpb
	// value or leaves it in unknown fields; a typed GetExtension on the first
	// panics and on the second silently returns nothing -- which would be an
	// authorization check that stops firing. Marshalling and re-parsing
	// against the global type registry normalises both.
	if proto.Size(opts) == 0 {
		return Annotation{}
	}
	raw, err := proto.Marshal(opts)
	if err != nil {
		return Annotation{}
	}
	var norm descriptorpb.MethodOptions
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, &norm); err != nil {
		return Annotation{}
	}
	if !proto.HasExtension(&norm, authv1.E_Auth) {
		return Annotation{}
	}
	ext, _ := proto.GetExtension(&norm, authv1.E_Auth).(*authv1.AuthOptions)
	if ext == nil {
		return Annotation{}
	}
	ann := Annotation{
		Present:   true,
		AuthTypes: append([]AuthType(nil), ext.GetAuthTypes()...),
		Public:    ext.GetPublic(),
		Platform:  ext.GetPlatform(),
	}
	if p := ext.GetPermission(); p != nil {
		ann.Permission = Permission{Resource: p.GetResource(), Verb: p.GetVerb()}
	}
	return ann
}

// annotationCache decodes each method once. The decode is a marshal and a
// re-parse, which is cheap but not free, and a procedure's annotation cannot
// change while the process is running.
//
// It hangs off an interceptor instance rather than off the package so two
// registries in one process -- a test suite, a multi-tenant host -- cannot see
// each other's descriptors.
type annotationCache struct {
	mu sync.RWMutex
	m  map[string]Annotation
}

func newAnnotationCache() *annotationCache { return &annotationCache{m: map[string]Annotation{}} }

func (c *annotationCache) get(procedure string, m protoreflect.MethodDescriptor) Annotation {
	c.mu.RLock()
	ann, ok := c.m[procedure]
	c.mu.RUnlock()
	if ok {
		return ann
	}
	ann = AnnotationOf(m)
	c.mu.Lock()
	c.m[procedure] = ann
	c.mu.Unlock()
	return ann
}
