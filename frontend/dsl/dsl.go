// Package dsl is the Interchange DSL frontend: a small YAML dialect that
// becomes canonical protobuf.
//
// It exists because you should not have to learn protobuf to declare a
// service (§09). It is deliberately the smallest surface that can still say
// everything the contract needs -- messages, RPCs, and the annotations that
// give an RPC a security posture. Anything it cannot say, you say in proto:
// the emitted .proto is the artifact, it is committed, and it is a normal
// proto file that a human can take over at any time.
package dsl

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	interchange "github.com/darmawan01/interchange"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"
)

// Frontend implements interchange.Frontend for the DSL, and
// interchange.SourceEmitter: the .proto source is the artifact `ix import`
// commits, and a frontend that returned only descriptors would leave the IR
// invisible.
type Frontend struct{}

var (
	_ interchange.Frontend      = (*Frontend)(nil)
	_ interchange.SourceEmitter = (*Frontend)(nil)
)

// New returns the DSL frontend.
func New() interchange.Frontend { return &Frontend{} }

// Register makes the DSL available to the toolchain by name.
func Register() { interchange.RegisterFrontend(New()) }

func init() { Register() }

// Name is the identifier used in interchange.yaml.
func (*Frontend) Name() string { return "dsl" }

var dslExtensions = []string{".ix.yaml", ".ix.yml", ".interchange.yaml", ".interchange.yml"}

// Detect claims a file conservatively: two frontends claiming the same path
// is an error in interchange.DetectFrontend, so an over-eager claim breaks
// every other YAML frontend rather than just this one.
func (*Frontend) Detect(p string, head []byte) bool {
	h := string(head)
	// Another format's marker wins outright, whatever the file is called.
	for _, marker := range []string{"openapi:", "swagger:", "asyncapi:", "$schema"} {
		if lineStarts(h, marker) {
			return false
		}
	}
	named := false
	lower := strings.ToLower(p)
	for _, ext := range dslExtensions {
		if strings.HasSuffix(lower, ext) {
			named = true
		}
	}
	// A sidecar is annotations only -- it is an input to a frontend, not a
	// source for one, and `catalog.interchange.yaml` is exactly what §09
	// calls a sidecar.
	if lineStarts(h, "procedures:") && !lineStarts(h, "package:") {
		return false
	}
	if named {
		return true
	}
	return lineStarts(h, markerKey+":") && lineStarts(h, "package:")
}

func lineStarts(head, prefix string) bool {
	for _, l := range strings.Split(head, "\n") {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// Parse transforms DSL sources into descriptors. It never returns a partial
// result: if anything cannot be represented, every problem is reported with
// its line and column and nothing is emitted.
func (f *Frontend) Parse(ctx context.Context, src interchange.Sources, opt interchange.Options) (*descriptorpb.FileDescriptorSet, interchange.Diagnostics, error) {
	sources, diags, err := generate(src, opt)
	if err != nil {
		return nil, diags, err
	}
	deps, derr := depRegistry(opt)
	if derr != nil {
		return nil, diags, derr
	}
	set, cerr := compile(ctx, sources, deps)
	if cerr != nil {
		// Reaching here means the DSL validated a construct it then emitted
		// wrongly. That is a bug in this frontend, not in the user's file, and
		// saying so is more useful than a location in generated source.
		diags = append(diags, interchange.Diagnostic{
			Severity: interchange.SeverityError,
			Path:     src.Paths[0],
			Line:     1,
			Message:  "the generated proto did not compile: " + cerr.Error(),
			Hint:     "this is a defect in the dsl frontend -- please report it with the DSL source",
		})
		return nil, diags, diags.Err()
	}
	return set, diags, nil
}

// ProtoSources renders the .proto tree for the given sources, keyed by the
// path each file takes in the canonical tree. `ix import` writes these; they
// are committed and reviewed, and the drift gate regenerates them.
func (f *Frontend) ProtoSources(_ context.Context, src interchange.Sources, opt interchange.Options) (map[string][]byte, interchange.Diagnostics, error) {
	return generate(src, opt)
}

// depRegistry indexes the descriptors the caller supplied. A frontend must
// not read the filesystem, so this is the only way an adopter's own protos
// can be referenced -- and the only way the optional modules' annotations
// arrive without this module linking them in.
func depRegistry(opt interchange.Options) (*protoregistry.Files, error) {
	if opt.Deps == nil || len(opt.Deps.File) == 0 {
		return &protoregistry.Files{}, nil
	}
	files, err := protodesc.NewFiles(opt.Deps)
	if err != nil {
		return nil, fmt.Errorf("dsl: Options.Deps: %w", err)
	}
	return files, nil
}

func generate(src interchange.Sources, opt interchange.Options) (map[string][]byte, interchange.Diagnostics, error) {
	if len(src.Paths) == 0 {
		return nil, nil, fmt.Errorf("dsl: no sources")
	}
	var all interchange.Diagnostics

	// The sidecar is parsed first: its annotations have to be merged onto the
	// RPCs before type resolution, so the imports it implies are derived too.
	sidecarName := src.SidecarPath
	if sidecarName == "" {
		sidecarName = sidecarPath
	}
	var sidecar map[string]*annotations
	if len(src.Sidecar) > 0 {
		sc := &collector{path: sidecarName}
		var doc yaml.Node
		if err := yaml.Unmarshal(src.Sidecar, &doc); err != nil {
			sc.list = append(sc.list, yamlSyntax(sidecarName, err))
		} else {
			sidecar = sc.parseSidecar(&doc)
		}
		all = append(all, sc.list...)
	}
	deps, err := depRegistry(opt)
	if err != nil {
		return nil, all, err
	}
	used := map[string]bool{}
	declared := map[string]declSite{}
	emitted := map[string]string{}

	out := map[string][]byte{}
	for _, p := range src.Paths {
		c := &collector{path: p}
		var doc yaml.Node
		if err := yaml.Unmarshal(src.Content[p], &doc); err != nil {
			all = append(all, yamlSyntax(p, err))
			continue
		}
		f := c.parseFile(p, &doc)
		if f == nil {
			all = append(all, c.list...)
			continue
		}
		if f.pkg == "" && opt.Package != "" {
			f.pkg = opt.Package
			// The document-level "missing package" diagnostic no longer
			// applies once Options supplied one.
			c.list = dropMissingPackage(c.list)
		}
		applySidecar(c, f, sidecar, used, sidecarName)

		r := c.resolveFile(f, declared, deps)
		f.goPackage = goPackage(f, opt)
		if prev, ok := emitted[protoPath(f)]; ok {
			c.errorf(f.pkgNode, "give one of them a distinct `file:` -- otherwise the second emitted file silently replaces the first",
				"%s and %s both emit %s", prev, p, protoPath(f))
		}
		emitted[protoPath(f)] = p
		if !c.failed() {
			out[protoPath(f)] = render(f, r)
		}
		all = append(all, c.list...)
	}

	sc := &collector{path: sidecarName}
	for _, proc := range sortedKeys(sidecar) {
		if !used[proc] {
			sc.errorf(sidecar[proc].procNode, "check the package, service and method names -- a sidecar entry that matches nothing is an annotation nobody applied",
				"sidecar: %s matches no RPC", proc)
		}
	}
	all = append(all, sc.list...)

	if err := all.Err(); err != nil {
		return nil, all, err
	}
	return out, all, nil
}

// sidecarPath is the placeholder for a caller that supplied sidecar bytes
// without naming the file.
const sidecarPath = "(sidecar)"

// Diagnostics are output too: two unmatched sidecar entries must report in
// the same order every run.
func sortedKeys(m map[string]*annotations) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func applySidecar(c *collector, f *file, sidecar map[string]*annotations, used map[string]bool, sidecarName string) {
	if len(sidecar) == 0 {
		return
	}
	// Conflicts are reported against the sidecar, because that is where the
	// node the diagnostic points at actually lives.
	sc := &collector{path: sidecarName}
	defer func() { c.list = append(c.list, sc.list...) }()

	for _, s := range f.services {
		for _, m := range s.rpcs {
			proc := "/" + f.pkg + "." + s.name + "/" + m.name
			side, ok := sidecar[proc]
			if !ok {
				continue
			}
			used[proc] = true
			m.annot = sc.merge(m.annot, side, proc)
		}
	}
}

func dropMissingPackage(list interchange.Diagnostics) interchange.Diagnostics {
	out := list[:0]
	for _, d := range list {
		if strings.Contains(d.Message, `missing required key "package"`) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// protoPath places the file where its package says it belongs, which is what
// makes `import "catalog/v1/catalog.proto"` resolve for everyone else.
func protoPath(f *file) string {
	dir := strings.ReplaceAll(f.pkg, ".", "/")
	return path.Join(dir, f.baseName+".proto")
}

func goPackage(f *file, opt interchange.Options) string {
	if f.goPackage != "" {
		return f.goPackage
	}
	if opt.GoPackagePrefix == "" {
		return ""
	}
	return strings.TrimSuffix(opt.GoPackagePrefix, "/") + "/" + strings.ReplaceAll(f.pkg, ".", "/")
}

var yamlLineRe = regexp.MustCompile(`line (\d+):`)

func yamlSyntax(p string, err error) interchange.Diagnostic {
	d := interchange.Diagnostic{
		Severity: interchange.SeverityError,
		Path:     p,
		Message:  "not valid YAML: " + err.Error(),
		Hint:     "fix the YAML syntax; the DSL is read as a plain YAML document",
	}
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		d.Line, _ = strconv.Atoi(m[1])
	}
	return d
}
