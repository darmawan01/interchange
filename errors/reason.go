package errors

import (
	"slices"
	"strings"
	"sync"
	"unicode"

	errorsv1 "github.com/darmawan01/interchange/errors/gen/go/interchange/errors/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The stock reasons, one per member of interchange.errors.v1.ErrorReason.
// The wire form of a reason is the enum value name with the enum's own
// prefix removed: ERROR_REASON_NOT_FOUND travels as NOT_FOUND, and a
// service's own CATALOG_REASON_PROVIDER_NOT_FOUND travels as
// PROVIDER_NOT_FOUND. Both spellings are accepted on the way in, so a caller
// that pastes the enum constant is not punished for it.
const (
	ReasonInvalidArgument    = "INVALID_ARGUMENT"
	ReasonUnauthenticated    = "UNAUTHENTICATED"
	ReasonPermissionDenied   = "PERMISSION_DENIED"
	ReasonNotFound           = "NOT_FOUND"
	ReasonAlreadyExists      = "ALREADY_EXISTS"
	ReasonFailedPrecondition = "FAILED_PRECONDITION"
	ReasonResourceExhausted  = "RESOURCE_EXHAUSTED"
	ReasonDeadlineExceeded   = "DEADLINE_EXCEEDED"
	ReasonUnavailable        = "UNAVAILABLE"
	ReasonInternal           = "INTERNAL"
	ReasonAborted            = "ABORTED"
	ReasonCanceled           = "CANCELED"
	ReasonUnimplemented      = "UNIMPLEMENTED"
)

// Set is a closed set of reason strings. Closed is the whole point: a client
// branches on a reason, so the set of reasons it may see has to be something
// it can enumerate from the contract rather than discover in production.
type Set interface {
	// Has reports whether reason is a member.
	Has(reason string) bool

	// Reasons lists the members in canonical form, sorted.
	Reasons() []string
}

// EnumSet builds a Set from a proto enum descriptor -- the enum in your own
// module's proto tree, which is what makes the taxonomy part of the contract
// and not part of the Go code. The zero value is skipped: an UNSPECIFIED
// reason says nothing a client can branch on.
func EnumSet(d protoreflect.EnumDescriptor) Set {
	prefix := screamingSnake(string(d.Name())) + "_"
	s := &set{m: map[string]struct{}{}}
	values := d.Values()
	for i := range values.Len() {
		v := values.Get(i)
		if v.Number() == 0 {
			continue
		}
		full := string(v.Name())
		s.add(strings.TrimPrefix(full, prefix))
		s.m[full] = struct{}{}
	}
	return s
}

// SetOf builds a Set from literal strings. It is the escape hatch for a
// taxonomy that does not live in a proto enum yet; prefer EnumSet, because a
// Go slice is not something a client in another language can read.
func SetOf(reasons ...string) Set {
	s := &set{m: map[string]struct{}{}}
	for _, r := range reasons {
		s.add(r)
	}
	return s
}

// Union accepts a reason that any of sets accepts. A service adds its own
// enum to the stock one rather than replacing it, so a generic
// PERMISSION_DENIED from an interceptor stays legal.
func Union(sets ...Set) Set { return union(slices.Clone(sets)) }

// Stock is the set built from interchange.errors.v1.ErrorReason.
func Stock() Set { return stockSet() }

var stockSet = sync.OnceValue(func() Set {
	return EnumSet(errorsv1.ErrorReason(0).Descriptor())
})

// set is immutable once built, which is why Has needs no lock.
type set struct {
	m         map[string]struct{}
	canonical []string
}

func (s *set) add(r string) {
	if r == "" {
		return
	}
	s.m[r] = struct{}{}
	s.canonical = append(s.canonical, r)
}

func (s *set) Has(reason string) bool {
	_, ok := s.m[reason]
	return ok
}

func (s *set) Reasons() []string {
	out := slices.Clone(s.canonical)
	slices.Sort(out)
	return slices.Compact(out)
}

type union []Set

func (u union) Has(reason string) bool {
	return slices.ContainsFunc(u, func(s Set) bool { return s != nil && s.Has(reason) })
}

func (u union) Reasons() []string {
	var out []string
	for _, s := range u {
		if s != nil {
			out = append(out, s.Reasons()...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// screamingSnake turns "ErrorReason" into "ERROR_REASON" so the enum's own
// prefix can be trimmed off its values without the caller naming it twice.
func screamingSnake(name string) string {
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (!unicode.IsUpper(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}
