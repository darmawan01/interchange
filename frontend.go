package interchange

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Sources are the files a frontend was pointed at. Content is populated for
// every path; a frontend must not read the filesystem itself, so that `ix`
// can drive it from a git object, an archive, or a test fixture.
type Sources struct {
	// Root is the directory paths are relative to, for diagnostics only.
	Root string

	// Paths is the ordered list of source files.
	Paths []string

	// Content maps each path to its bytes.
	Content map[string][]byte

	// Sidecar is the optional annotations file (§09) -- the universal
	// fallback for formats with nowhere to put an annotation.
	Sidecar []byte

	// SidecarPath names the sidecar for diagnostics. A diagnostic without a
	// file and a line is barely a diagnostic, and a sidecar's mistakes are
	// exactly the ones a reader needs pointing at.
	SidecarPath string
}

// Options configures a frontend run.
type Options struct {
	// Package is the proto package emitted descriptors land in, when the
	// source format has no equivalent notion.
	Package string

	// GoPackagePrefix is the go_package option prefix for emitted files.
	GoPackagePrefix string

	// Deps are descriptors the sources may reference by fully-qualified name:
	// the annotation protos, and an adopter's own existing tree.
	//
	// A frontend must not read the filesystem, so without this the only
	// resolvable external types are the ones linked into whatever binary is
	// doing the parsing -- which works for the annotation set and for nothing
	// an adopter wrote.
	Deps *descriptorpb.FileDescriptorSet

	// Params are frontend-specific settings from interchange.yaml.
	Params map[string]string
}

// Severity of a diagnostic.
type Severity int

// Diagnostic severities.
const (
	SeverityError Severity = iota
	SeverityWarning
	SeverityNote
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "note"
	}
}

// Diagnostic is one message with the exact source location. A frontend that
// cannot represent a construct must produce one of these rather than a
// partial contract: "total, or loud" (§09).
type Diagnostic struct {
	Severity Severity
	Path     string
	Line     int
	Col      int
	Message  string

	// Hint is the "→ do this instead" line.
	Hint string
}

func (d Diagnostic) String() string {
	loc := d.Path
	if d.Line > 0 {
		loc = fmt.Sprintf("%s:%d", d.Path, d.Line)
		if d.Col > 0 {
			loc = fmt.Sprintf("%s:%d", loc, d.Col)
		}
	}
	s := fmt.Sprintf("%s: %s: %s", d.Severity, loc, d.Message)
	if d.Hint != "" {
		s += "\n  → " + d.Hint
	}
	return s
}

// Diagnostics is an ordered set of diagnostics.
type Diagnostics []Diagnostic

// HasErrors reports whether any diagnostic is fatal.
func (d Diagnostics) HasErrors() bool {
	return slices.ContainsFunc(d, func(x Diagnostic) bool { return x.Severity == SeverityError })
}

// Err returns a single error summarising the fatal diagnostics, or nil.
func (d Diagnostics) Err() error {
	var msg string
	n := 0
	for _, x := range d {
		if x.Severity != SeverityError {
			continue
		}
		n++
		msg += "\n" + x.String()
	}
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%d construct(s) could not be represented:%s", n, msg)
}

// Frontend turns one source format into canonical descriptors. It is the ONLY
// place a source format is understood; everything downstream of the IR is
// format-blind.
type Frontend interface {
	// Name is the identifier used in interchange.yaml (e.g. "openapi").
	Name() string

	// Detect reports whether this frontend claims the file, so `ix import`
	// can work without being told the format. head is the first few KiB.
	Detect(path string, head []byte) bool

	// Parse transforms sources into descriptors. It MUST return an error --
	// never a partial result -- for anything it cannot represent.
	Parse(ctx context.Context, src Sources, opt Options) (*descriptorpb.FileDescriptorSet, Diagnostics, error)
}

// SourceEmitter is an optional capability: a frontend that can render the
// canonical .proto source for its input, not just the descriptors.
//
// The emitted proto is the artifact -- committed, reviewed, and under the
// same drift gate as generated code (§09). A frontend that returns only
// descriptors leaves the IR invisible, which is the thing that rule exists to
// prevent, so `ix import` type-asserts for this and refuses to write a tree
// without it.
type SourceEmitter interface {
	// ProtoSources renders the sources as formatted .proto files, keyed by
	// the path each should be written to, relative to the api root.
	ProtoSources(ctx context.Context, src Sources, opt Options) (map[string][]byte, Diagnostics, error)
}

// DepFiles indexes the descriptors a caller supplied in Options.Deps so a
// frontend can resolve types it did not link.
//
// Two things make this less obvious than it looks, and both have already bitten
// a frontend that wrote it itself:
//
// A file already linked into this binary is used as linked, and the copy in
// Deps is dropped. Every FileDescriptorSet built with --include_imports carries
// descriptor.proto; building a second object for a path the compiler already
// has puts two of that file in one link, and then every symbol in it collides
// with itself.
//
// And a supplied file that imports another is registered after it, whatever
// order the set arrived in, so it shares the one object rather than minting a
// second copy.
func DepFiles(deps *descriptorpb.FileDescriptorSet) (*protoregistry.Files, error) {
	out := &protoregistry.Files{}
	if deps == nil || len(deps.File) == 0 {
		return out, nil
	}
	res := &fallbackResolver{local: out}

	pending := make([]*descriptorpb.FileDescriptorProto, 0, len(deps.File))
	for _, fdp := range deps.File {
		if _, err := protoregistry.GlobalFiles.FindFileByPath(fdp.GetName()); err == nil {
			continue
		}
		pending = append(pending, fdp)
	}

	// Register whatever resolves, repeatedly, until a pass makes no progress.
	// A set is conventionally ordered deps-first, but conventionally is not
	// always, and the failure when it is not is an unresolved import rather
	// than anything a reader would connect to file ordering.
	var lastErr error
	for len(pending) > 0 {
		var stuck []*descriptorpb.FileDescriptorProto
		progress := false
		for _, fdp := range pending {
			fd, err := protodesc.NewFile(fdp, res)
			if err != nil {
				lastErr = fmt.Errorf("%s: %w", fdp.GetName(), err)
				stuck = append(stuck, fdp)
				continue
			}
			if err := out.RegisterFile(fd); err != nil {
				return nil, fmt.Errorf("interchange: Options.Deps: %s: %w", fdp.GetName(), err)
			}
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("interchange: Options.Deps: %w", lastErr)
		}
		pending = stuck
	}
	return out, nil
}

// fallbackResolver resolves a dependency against the files registered so far,
// then against what is linked in.
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

var frontends = struct {
	sync.RWMutex
	m map[string]Frontend
}{m: map[string]Frontend{}}

// RegisterFrontend makes a frontend available to the whole toolchain.
func RegisterFrontend(f Frontend) {
	frontends.Lock()
	defer frontends.Unlock()
	frontends.m[f.Name()] = f
}

// FrontendFor resolves a frontend by name.
func FrontendFor(name string) (Frontend, bool) {
	frontends.RLock()
	defer frontends.RUnlock()
	f, ok := frontends.m[name]
	return f, ok
}

// DetectFrontend asks every registered frontend whether it claims a file.
// Ambiguity is an error rather than a coin flip.
func DetectFrontend(path string, head []byte) (Frontend, error) {
	frontends.RLock()
	defer frontends.RUnlock()
	var matches []Frontend
	for _, f := range frontends.m {
		if f.Detect(path, head) {
			matches = append(matches, f)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no frontend claims %s (registered: %v)", path, frontendNamesLocked())
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, f := range matches {
			names[i] = f.Name()
		}
		slices.Sort(names)
		return nil, fmt.Errorf("%s is claimed by more than one frontend %v; name one explicitly", path, names)
	}
}

// Frontends lists the registered frontend names, sorted.
func Frontends() []string {
	frontends.RLock()
	defer frontends.RUnlock()
	return frontendNamesLocked()
}

func frontendNamesLocked() []string {
	out := make([]string, 0, len(frontends.m))
	for n := range frontends.m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
