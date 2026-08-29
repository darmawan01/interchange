package dsl

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"gopkg.in/yaml.v3"
)

var scalars = map[string]bool{
	"double": true, "float": true,
	"int32": true, "int64": true, "uint32": true, "uint64": true,
	"sint32": true, "sint64": true, "fixed32": true, "fixed64": true,
	"sfixed32": true, "sfixed64": true,
	"bool": true, "string": true, "bytes": true,
}

// Map keys are the integral, bool and string scalars -- protobuf's rule, not
// ours.
var mapKeyScalars = map[string]bool{
	"int32": true, "int64": true, "uint32": true, "uint64": true,
	"sint32": true, "sint64": true, "fixed32": true, "fixed64": true,
	"sfixed32": true, "sfixed64": true,
	"bool": true, "string": true,
}

type symbol struct {
	isEnum bool
}

// declSite is where a fully-qualified name was declared. It is kept across
// every source in one run, because two DSL files in the same package can
// collide and each file's own resolver would see nothing wrong.
type declSite struct {
	path string
	node *yaml.Node
}

// resolver holds the file's own type names plus the imports it turned out to
// need. Imports are derived from the types and annotations actually used, so
// a DSL author never maintains an import list.
type resolver struct {
	c       *collector
	pkg     string
	symbols map[string]symbol
	imports map[string]bool

	// declared is shared across every source in the run.
	declared map[string]declSite
}

func (c *collector) resolveFile(f *file, declared map[string]declSite) *resolver {
	r := &resolver{c: c, pkg: f.pkg, symbols: map[string]symbol{}, imports: map[string]bool{}, declared: declared}
	r.collect(f.messages, f.enums, f.pkg)
	r.checkScopeDuplicates(f.messages, f.enums, "document")

	r.walkMessages(f.messages, nil)
	r.checkEnums(f.enums)
	r.resolveServices(f)
	return r
}

func (r *resolver) collect(msgs []*message, enums []*enum, prefix string) {
	for _, m := range msgs {
		full := prefix + "." + m.name
		r.symbols[full] = symbol{}
		r.claim(full, m.node)
		r.collect(m.messages, m.enums, full)
	}
	for _, e := range enums {
		full := prefix + "." + e.name
		r.symbols[full] = symbol{isEnum: true}
		r.claim(full, e.node)
	}
}

func (r *resolver) claim(full string, n *yaml.Node) {
	if r.declared == nil {
		return
	}
	if prev, ok := r.declared[full]; ok {
		r.c.errorf(n, "rename it, or move the two declarations into one file -- one proto package cannot hold the name twice",
			"%s is already declared in %s at line %d", full, prev.path, prev.node.Line)
		return
	}
	r.declared[full] = declSite{path: r.c.path, node: n}
}

func (r *resolver) checkScopeDuplicates(msgs []*message, enums []*enum, what string) {
	seen := map[string]bool{}
	for _, m := range msgs {
		seen[m.name] = true
	}
	for _, e := range enums {
		if seen[e.name] {
			r.c.errorf(e.node, "rename one of them", "%s: %q is declared as both a message and an enum", what, e.name)
		}
	}
}

func (r *resolver) walkMessages(msgs []*message, scope []string) {
	for _, m := range msgs {
		inner := append(append([]string{}, scope...), m.name)
		r.checkScopeDuplicates(m.messages, m.enums, "messages."+strings.Join(inner, "."))
		r.checkFields(m, inner)
		r.checkEnums(m.enums)
		r.walkMessages(m.messages, inner)
	}
}

func (r *resolver) checkFields(m *message, scope []string) {
	byNum := map[int]*field{}
	for _, f := range m.fields {
		if f.numNode != nil {
			if prev, ok := byNum[f.num]; ok {
				r.c.errorf(f.numNode, "give one of them a different number -- a field number is the wire contract and cannot be shared",
					"message %s: fields %q and %q both use number %d", strings.Join(scope, "."), prev.name, f.name, f.num)
			} else {
				byNum[f.num] = f
			}
		}
		r.resolveFieldType(f, scope)
	}
}

func (r *resolver) resolveFieldType(f *field, scope []string) {
	t := strings.TrimSpace(f.typ)
	if t == "" {
		return
	}
	if strings.HasPrefix(t, "map<") {
		r.resolveMap(f, t, scope)
		return
	}
	if scalars[t] {
		f.rendered = t
		return
	}
	if _, ok := r.lookup(t, scope); !ok {
		r.unknownType(f.typeNode, t)
		return
	}
	f.rendered = t
}

func (r *resolver) resolveMap(f *field, t string, scope []string) {
	body := strings.TrimSuffix(strings.TrimPrefix(t, "map<"), ">")
	if !strings.HasSuffix(t, ">") || !strings.Contains(body, ",") {
		r.c.errorf(f.typeNode, "write it as `map<string, string>`", "field %q: %q is not a map type", f.name, t)
		return
	}
	k, v, _ := strings.Cut(body, ",")
	k, v = strings.TrimSpace(k), strings.TrimSpace(v)
	if !mapKeyScalars[k] {
		r.c.errorf(f.typeNode, "a map key is an integral, bool or string scalar", "field %q: %q is not a valid map key", f.name, k)
	}
	switch {
	case scalars[v]:
	default:
		if _, ok := r.lookup(v, scope); !ok {
			r.unknownType(f.typeNode, v)
		}
	}
	if f.repeated {
		r.c.errorf(f.node, "a map is already a collection; drop `repeated: true`", "field %q: a map cannot be repeated", f.name)
	}
	if f.optional {
		r.c.errorf(f.node, "a map is already absent when empty; drop `optional: true`", "field %q: a map cannot be optional", f.name)
	}
	f.isMap, f.mapKey, f.mapVal = true, k, v
	f.rendered = "map<" + k + ", " + v + ">"
}

// lookup mirrors protobuf's own innermost-scope-outward resolution for names
// declared in this file, then falls back to the linked descriptor registry
// for a fully-qualified name from another proto file -- recording the import
// that reference implies.
func (r *resolver) lookup(name string, scope []string) (symbol, bool) {
	for i := len(scope); i >= 0; i-- {
		cand := r.pkg
		if i > 0 {
			cand += "." + strings.Join(scope[:i], ".")
		}
		cand += "." + name
		if s, ok := r.symbols[cand]; ok {
			return s, true
		}
	}
	if s, ok := r.symbols[r.pkg+"."+name]; ok {
		return s, true
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return symbol{}, false
	}
	switch d.(type) {
	case protoreflect.MessageDescriptor:
		r.imports[d.ParentFile().Path()] = true
		return symbol{}, true
	case protoreflect.EnumDescriptor:
		r.imports[d.ParentFile().Path()] = true
		return symbol{isEnum: true}, true
	}
	return symbol{}, false
}

func (r *resolver) unknownType(n *yaml.Node, t string) {
	r.c.errorf(n, "declare it under `messages:` or `enums:`, use a scalar (string, int32, bool, bytes, ...), or name a type from a known proto file in full (google.protobuf.Timestamp)",
		"unknown type %q", t)
}

func (r *resolver) checkEnums(enums []*enum) {
	for _, e := range enums {
		byNum := map[int]string{}
		zero := ""
		for _, v := range e.values {
			if prev, ok := byNum[v.num]; ok {
				r.c.errorf(v.node, "give one of them a different number; the DSL does not support allow_alias",
					"enum %s: %q and %q both use %d", e.name, prev, v.name, v.num)
				continue
			}
			byNum[v.num] = v.name
			if v.num == 0 {
				zero = v.name
			}
		}
		switch {
		case zero == "":
			r.c.errorf(e.node, "add `"+screaming(e.name)+"_UNSPECIFIED: 0` as the first value",
				"enum %s has no zero value", e.name)
		case !strings.HasSuffix(zero, "_UNSPECIFIED"):
			r.c.errorf(e.node, "rename it to `"+screaming(e.name)+"_UNSPECIFIED` -- proto3 has no field presence for enums, so the zero value is what an unset field reads as",
				"enum %s: zero value %q is not the unspecified value", e.name, zero)
		}
	}
}

func (r *resolver) resolveServices(f *file) {
	for _, s := range f.services {
		if s.annot != nil && len(s.annot.transports) > 0 {
			r.imports[importTransports] = true
		}
		for _, m := range s.rpcs {
			r.checkRPCType(m.request, m.reqNode, "request")
			r.checkRPCType(m.response, m.respNode, "response")
			r.annotationImports(m.annot)
			if m.annot == nil || m.annot.auth == nil {
				r.c.list = append(r.c.list, warning(r.c.path, m.node,
					"rpc "+s.name+"."+m.name+": no authorization declared",
					"add an `auth:` block, or a sidecar entry -- an RPC with no declared posture is one nobody reviewed"))
			}
		}
	}
}

func (r *resolver) checkRPCType(name string, n *yaml.Node, what string) {
	if name == "" {
		return
	}
	if scalars[name] {
		r.c.errorf(n, "an RPC takes and returns a message, not a scalar", "%s type %q is a scalar", what, name)
		return
	}
	s, ok := r.lookup(name, nil)
	if !ok {
		r.c.errorf(n, "declare it under `messages:` -- an RPC's "+what+" must be a message in this file or a fully-qualified message from a known proto file",
			"unknown %s message %q", what, name)
		return
	}
	if s.isEnum {
		r.c.errorf(n, "an RPC takes and returns a message, not an enum", "%s type %q is an enum", what, name)
	}
}

func (r *resolver) annotationImports(a *annotations) {
	if a == nil {
		return
	}
	if len(a.transports) > 0 || a.internalSet {
		r.imports[importTransports] = true
	}
	if a.http != nil {
		r.imports[importHTTP] = true
	}
	if a.auth != nil {
		r.imports[importAuth] = true
	}
	if a.cli != nil {
		r.imports[importCLI] = true
	}
}

func (r *resolver) sortedImports() []string {
	out := make([]string, 0, len(r.imports))
	for p := range r.imports {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// screaming turns CamelCase into SCREAMING_SNAKE, for the enum hint.
func screaming(s string) string {
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && ch >= 'A' && ch <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(ch)
	}
	return strings.ToUpper(b.String())
}
