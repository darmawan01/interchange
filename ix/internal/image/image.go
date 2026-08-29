// Package image turns a proto tree into a FileDescriptorSet and makes every
// custom option on it readable.
//
// This is the keystone. `buf build -o -` writes a binary image on stdout; ix
// unmarshals it twice. The first pass yields descriptors with unknown option
// bytes. From those descriptors ix collects every extension the tree
// *declares* and mints a dynamicpb extension type for each. The second pass
// re-parses the same bytes with that resolver, so every annotation --
// (transports), (internal), (cli.command), and (auth) -- arrives as a typed
// field.
//
// Doing it by number and field name rather than by importing generated Go is
// what lets `ix describe` print an auth annotation without ix depending on
// the auth module. It is also the same dual availability the runtime relies
// on (docs/02): one descriptor, read at build time here and by reflection
// there.
package image

import (
	"fmt"
	"sort"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/bufx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Image is a built contract: its descriptors, its extension types, and the
// raw descriptor set they came from.
type Image struct {
	FDS   *descriptorpb.FileDescriptorSet
	Files *protoregistry.Files
	Types *Resolver

	// Extensions is every extension declared anywhere in the image, sorted
	// by extendee then number. `ix lint` checks these against the band table.
	Extensions []Extension

	// Raw is the bytes buf produced, kept so a caller can hand the same
	// descriptor set to a plugin without rebuilding.
	Raw []byte
}

// Extension is one declared `extend` field.
type Extension struct {
	FullName protoreflect.FullName
	Extendee protoreflect.FullName
	Number   int32
	File     string
	Line     int // 0 when the image carries no source info
}

// Resolver finds extension and message types in the image first and in the
// process-wide registry second. Image-first matters: ix links core's
// generated transport types, and an image that declares its own copy of an
// annotation must be read as the image declares it, not as this binary
// happens to have been compiled.
type Resolver struct {
	local  *protoregistry.Types
	global *protoregistry.Types
}

// FindExtensionByName implements protoregistry.ExtensionTypeResolver.
func (r *Resolver) FindExtensionByName(f protoreflect.FullName) (protoreflect.ExtensionType, error) {
	if xt, err := r.local.FindExtensionByName(f); err == nil {
		return xt, nil
	}
	return r.global.FindExtensionByName(f)
}

// FindExtensionByNumber implements protoregistry.ExtensionTypeResolver.
func (r *Resolver) FindExtensionByNumber(m protoreflect.FullName, n protoreflect.FieldNumber) (protoreflect.ExtensionType, error) {
	if xt, err := r.local.FindExtensionByNumber(m, n); err == nil {
		return xt, nil
	}
	return r.global.FindExtensionByNumber(m, n)
}

// FindMessageByName implements protoregistry.MessageTypeResolver.
func (r *Resolver) FindMessageByName(m protoreflect.FullName) (protoreflect.MessageType, error) {
	if mt, err := r.local.FindMessageByName(m); err == nil {
		return mt, nil
	}
	return r.global.FindMessageByName(m)
}

// FindMessageByURL implements protoregistry.MessageTypeResolver.
func (r *Resolver) FindMessageByURL(url string) (protoreflect.MessageType, error) {
	if mt, err := r.local.FindMessageByURL(url); err == nil {
		return mt, nil
	}
	return r.global.FindMessageByURL(url)
}

// Build runs `buf build` and parses the result. inputs are buf inputs
// (directories, workspace paths); empty means the module in dir.
func Build(r *bufx.Runner, inputs ...string) (*Image, error) {
	args := []string{"build", "-o", "-", "--as-file-descriptor-set"}
	if len(inputs) == 1 {
		args = append(args, inputs[0])
	} else if len(inputs) > 1 {
		// buf builds one input per invocation; merging N images by hand
		// would silently drop conflicting file entries, so make the user's
		// workspace express the union instead of guessing here.
		return nil, fmt.Errorf("image: buf builds one input at a time, got %d (%s) -- put them in one buf.yaml workspace", len(inputs), strings.Join(inputs, ", "))
	}
	raw, err := r.Output(args...)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse does the two-pass unmarshal on a binary FileDescriptorSet.
func Parse(raw []byte) (*Image, error) {
	var first descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &first); err != nil {
		return nil, fmt.Errorf("image: parsing descriptor set: %w", err)
	}
	files, err := protodesc.NewFiles(&first)
	if err != nil {
		return nil, fmt.Errorf("image: linking descriptors: %w", err)
	}

	local := new(protoregistry.Types)
	var exts []Extension
	var regErr error
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		collect(fd, fd.Extensions(), local, &exts, &regErr)
		var walk func(protoreflect.MessageDescriptors)
		walk = func(ms protoreflect.MessageDescriptors) {
			for i := 0; i < ms.Len(); i++ {
				m := ms.Get(i)
				collect(fd, m.Extensions(), local, &exts, &regErr)
				walk(m.Messages())
			}
		}
		walk(fd.Messages())
		return regErr == nil
	})
	if regErr != nil {
		return nil, regErr
	}
	res := &Resolver{local: local, global: protoregistry.GlobalTypes}

	var second descriptorpb.FileDescriptorSet
	if err := (proto.UnmarshalOptions{Resolver: res}).Unmarshal(raw, &second); err != nil {
		return nil, fmt.Errorf("image: re-parsing descriptor set with extensions: %w", err)
	}
	resolved, err := protodesc.NewFiles(&second)
	if err != nil {
		return nil, fmt.Errorf("image: linking descriptors: %w", err)
	}

	sort.Slice(exts, func(i, j int) bool {
		if exts[i].Extendee != exts[j].Extendee {
			return exts[i].Extendee < exts[j].Extendee
		}
		if exts[i].Number != exts[j].Number {
			return exts[i].Number < exts[j].Number
		}
		return exts[i].FullName < exts[j].FullName
	})

	return &Image{FDS: &second, Files: resolved, Types: res, Extensions: exts, Raw: raw}, nil
}

func collect(fd protoreflect.FileDescriptor, xs protoreflect.ExtensionDescriptors, into *protoregistry.Types, out *[]Extension, errp *error) {
	for i := 0; i < xs.Len(); i++ {
		xd := xs.Get(i)
		xt := dynamicpb.NewExtensionType(xd)
		if err := into.RegisterExtension(xt); err != nil {
			// A duplicate registration means the same extension arrived
			// twice; the descriptors already linked, so this is not fatal.
			continue
		}
		*out = append(*out, Extension{
			FullName: xd.FullName(),
			Extendee: xd.ContainingMessage().FullName(),
			Number:   int32(xd.Number()),
			File:     fd.Path(),
			Line:     Line(xd),
		})
	}
}

// Line is a descriptor's 1-based source line, or 0 when the image was built
// without source info.
func Line(d protoreflect.Descriptor) int {
	fd := d.ParentFile()
	if fd == nil {
		return 0
	}
	locs := fd.SourceLocations()
	if locs.Len() == 0 {
		return 0
	}
	loc := locs.ByDescriptor(d)
	if loc.Path == nil {
		return 0
	}
	return loc.StartLine + 1
}

// Pos is "file:line" for a descriptor -- the form every ix diagnostic uses,
// because a diagnostic without a location is a diagnostic nobody can act on.
func Pos(d protoreflect.Descriptor) string {
	fd := d.ParentFile()
	if fd == nil {
		return string(d.FullName())
	}
	if l := Line(d); l > 0 {
		return fmt.Sprintf("%s:%d", fd.Path(), l)
	}
	return fd.Path()
}

// Services lists every service in files the image was asked to build,
// sorted by full name. Imported dependencies are excluded: a lint or a
// describe that walked googleapis would be reporting on somebody else's
// contract.
func (im *Image) Services(local func(path string) bool) []protoreflect.ServiceDescriptor {
	var out []protoreflect.ServiceDescriptor
	im.Files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if local != nil && !local(fd.Path()) {
			return true
		}
		ss := fd.Services()
		for i := 0; i < ss.Len(); i++ {
			out = append(out, ss.Get(i))
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].FullName() < out[j].FullName() })
	return out
}

// LocalFiles reports the files buf was asked to build, as opposed to the
// dependencies it pulled in. buf's --as-file-descriptor-set output does not
// mark them, so ix uses the same rule buf's own plugins do: a file under one
// of the configured source directories is local.
func LocalFiles(im *Image, roots []string) func(string) bool {
	if len(roots) == 0 {
		return func(string) bool { return true }
	}
	return func(path string) bool {
		for _, r := range roots {
			if r == "." || r == "" {
				return !isWellKnown(path)
			}
			if strings.HasPrefix(path, strings.TrimSuffix(r, "/")+"/") {
				return true
			}
		}
		return false
	}
}

func isWellKnown(path string) bool {
	for _, p := range []string{"google/", "buf/", "grpc/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Methods lists every method of every local service, sorted.
func (im *Image) Methods(local func(string) bool) []protoreflect.MethodDescriptor {
	var out []protoreflect.MethodDescriptor
	for _, sd := range im.Services(local) {
		ms := sd.Methods()
		for i := 0; i < ms.Len(); i++ {
			out = append(out, ms.Get(i))
		}
	}
	return out
}

// FindMethod resolves an RPC named any of the three ways a user will type it:
// "Service.Method", "pkg.Service.Method" or "/pkg.Service/Method".
func (im *Image) FindMethod(ref string, local func(string) bool) (protoreflect.MethodDescriptor, error) {
	want, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	var hits []protoreflect.MethodDescriptor
	for _, m := range im.Methods(local) {
		if want.matches(m) {
			hits = append(hits, m)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no RPC matches %q\n%s", ref, suggest(im.Methods(local)))
	default:
		var names []string
		for _, h := range hits {
			names = append(names, string(h.FullName()))
		}
		return nil, fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
}

type ref struct {
	service string // may be bare ("CatalogService") or qualified
	method  string
	exact   bool // "/pkg.Svc/Method" form: match the full name only
}

func (r ref) matches(m protoreflect.MethodDescriptor) bool {
	if string(m.Name()) != r.method {
		return false
	}
	svc := string(m.Parent().FullName())
	if r.exact || strings.Contains(r.service, ".") {
		return svc == r.service
	}
	return svc == r.service || strings.HasSuffix(svc, "."+r.service)
}

func parseRef(s string) (ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ref{}, fmt.Errorf("empty RPC reference")
	}
	if strings.HasPrefix(s, "/") {
		parts := strings.Split(strings.TrimPrefix(s, "/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return ref{}, fmt.Errorf("%q: want /pkg.Service/Method", s)
		}
		return ref{service: parts[0], method: parts[1], exact: true}, nil
	}
	i := strings.LastIndex(s, ".")
	if i <= 0 || i == len(s)-1 {
		return ref{}, fmt.Errorf("%q: want Service.Method, pkg.Service.Method or /pkg.Service/Method", s)
	}
	return ref{service: s[:i], method: s[i+1:]}, nil
}

func suggest(ms []protoreflect.MethodDescriptor) string {
	if len(ms) == 0 {
		return "  (the contract declares no RPCs)"
	}
	var b strings.Builder
	b.WriteString("known RPCs:\n")
	n := 0
	for _, m := range ms {
		if n == 20 {
			fmt.Fprintf(&b, "  ... and %d more\n", len(ms)-n)
			break
		}
		fmt.Fprintf(&b, "  %s.%s\n", m.Parent().FullName(), m.Name())
		n++
	}
	return strings.TrimRight(b.String(), "\n")
}
