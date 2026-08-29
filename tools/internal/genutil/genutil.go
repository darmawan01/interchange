// Package genutil is the plumbing both interchange plugins share: import
// management with readable aliases, and source locations for the errors a
// plugin raises when a contract says something it cannot represent.
package genutil

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Importer collects the imports one generated file needs and writes the
// import block itself.
//
// protogen's own QualifiedGoIdent would do this, but it names an import after
// the last path element: every `.../v1` package becomes `v1`, `v11`, `v12`.
// Generated code is read by humans during incidents, so the alias is the
// package's real Go name.
type Importer struct {
	names map[protogen.GoImportPath]protogen.GoPackageName
	alias map[protogen.GoImportPath]string
	taken map[string]bool
	order []protogen.GoImportPath
}

// NewImporter returns an importer that knows the Go package name of every
// file in the request.
func NewImporter(p *protogen.Plugin) *Importer {
	im := &Importer{
		names: map[protogen.GoImportPath]protogen.GoPackageName{},
		alias: map[protogen.GoImportPath]string{},
		taken: map[string]bool{},
	}
	for _, f := range p.Files {
		im.names[f.GoImportPath] = f.GoPackageName
	}
	return im
}

// Pkg returns the alias an import path is referred to by, adding it to the
// import block on first use.
func (im *Importer) Pkg(p protogen.GoImportPath) string {
	if a, ok := im.alias[p]; ok {
		return a
	}
	base := im.base(p)
	a := base
	for n := 2; im.taken[a]; n++ {
		a = fmt.Sprintf("%s%d", base, n)
	}
	im.alias[p] = a
	im.taken[a] = true
	im.order = append(im.order, p)
	return a
}

// Ref renders a qualified reference to an identifier.
func (im *Importer) Ref(id protogen.GoIdent) string {
	return im.Pkg(id.GoImportPath) + "." + id.GoName
}

func (im *Importer) base(p protogen.GoImportPath) string {
	if n, ok := im.names[p]; ok {
		return string(n)
	}
	s := string(p)
	base := path.Base(s)
	// A `.../catalog/v1` path is package catalogv1, which is what
	// protoc-gen-go named it too.
	if isVersion(base) {
		base = path.Base(path.Dir(s)) + base
	}
	return sanitize(base)
}

func isVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

// Write emits the import block: standard library first, then the rest, each
// group sorted. Sorting is not cosmetic -- an import block in map order is a
// generated file that differs from itself between runs.
func (im *Importer) Write(g *protogen.GeneratedFile) {
	if len(im.order) == 0 {
		return
	}
	paths := slices.Clone(im.order)
	slices.SortFunc(paths, func(a, b protogen.GoImportPath) int {
		return strings.Compare(string(a), string(b))
	})
	var std, ext []protogen.GoImportPath
	for _, p := range paths {
		if strings.Contains(strings.SplitN(string(p), "/", 2)[0], ".") {
			ext = append(ext, p)
		} else {
			std = append(std, p)
		}
	}
	g.P("import (")
	for i, group := range [][]protogen.GoImportPath{std, ext} {
		if i > 0 && len(group) > 0 && len(std) > 0 {
			g.P()
		}
		for _, p := range group {
			if im.alias[p] == path.Base(string(p)) {
				g.P("\t", strconv.Quote(string(p)))
				continue
			}
			g.P("\t", im.alias[p], " ", strconv.Quote(string(p)))
		}
	}
	g.P(")")
	g.P()
}

// SourceLoc renders "file.proto:12:3" for the element at loc, so a plugin's
// refusal points at the line someone has to edit. Descriptors reconstructed
// from generated Go carry no SourceCodeInfo, so it degrades to the file name
// rather than lying about a position.
func SourceLoc(f *protogen.File, loc protogen.Location) string {
	info := f.Proto.GetSourceCodeInfo()
	if info != nil {
		for _, l := range info.GetLocation() {
			if !samePath(l.GetPath(), loc.Path) {
				continue
			}
			if span := l.GetSpan(); len(span) >= 3 {
				return fmt.Sprintf("%s:%d:%d", loc.SourceFile, span[0]+1, span[1]+1)
			}
		}
	}
	return loc.SourceFile
}

func samePath(a []int32, b protoreflect.SourcePath) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
