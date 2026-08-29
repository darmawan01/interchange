package auth

import (
	"sync"

	"github.com/darmawan01/interchange"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
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
	// Never m.Options() directly. A descriptor built by a compiler or a
	// schema frontend carries the option as a dynamicpb value or as unknown
	// bytes; a typed GetExtension on the first panics and on the second
	// silently returns nothing -- which would be an authorization check that
	// stops firing. interchange.MethodOptions normalises both, and owning
	// that in core rather than here is what stops three modules carrying
	// three copies of it (ADR-0035).
	norm := interchange.MethodOptions(m)
	if norm == nil {
		return Annotation{}
	}
	return annotationFromOptions(norm)
}

func annotationFromOptions(norm *descriptorpb.MethodOptions) Annotation {
	if !proto.HasExtension(norm, authv1.E_Auth) {
		return Annotation{}
	}
	ext, _ := proto.GetExtension(norm, authv1.E_Auth).(*authv1.AuthOptions)
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
