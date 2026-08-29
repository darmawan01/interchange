package openapi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/darmawan01/interchange"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	yaml "go.yaml.in/yaml/v4"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// yamlNode is the node type libopenapi hands back for a vendor extension.
type yamlNode = yaml.Node

// converter turns one OpenAPI document into one proto file.
//
// Two rules shape every decision here. Total or loud: a construct with no
// canonical proto form produces a diagnostic with its source location and the
// import writes nothing. Deterministic: field numbers come from document
// order, never from a map walk, because a renumbered field is a silently
// broken wire contract.
type converter struct {
	src   *source
	doc   *v3.Document
	opt   interchange.Options
	sc    *sidecar
	deps  *protoregistry.Files
	file  *protoFile
	diags interchange.Diagnostics

	// owner records which JSON pointer claimed a top-level proto name, so a
	// collision can name both sides.
	owner map[string]string
	// schemaKind maps a component schema name to what it became.
	schemaKind map[string]schemaKind

	pkg     string
	service string

	nPaths, nRPCs, nSchemas, nMessages int
}

type schemaKind struct {
	kind string // "message", "enum", "alias"
	name string // the proto name, for message and enum
	ptr  string
}

func (c *converter) errf(pointer, hint, format string, args ...any) {
	c.diags = append(c.diags, c.src.at(interchange.SeverityError, pointer, fmt.Sprintf(format, args...), hint))
}

// errAt reports at one location but names another: the oneOf diagnostic points
// at the 'oneOf' key while naming the schema, which is the shape §09 prints.
func (c *converter) errAt(loc, name, hint, format string, args ...any) {
	d := c.src.at(interchange.SeverityError, loc, fmt.Sprintf(format, args...), hint)
	d.Message = where(name) + ": " + fmt.Sprintf(format, args...)
	c.diags = append(c.diags, d)
}

func (c *converter) notef(pointer, format string, args ...any) {
	c.diags = append(c.diags, c.src.at(interchange.SeverityNote, pointer, fmt.Sprintf(format, args...), ""))
}

// claim records a top-level proto name, reporting a collision with the pointer
// that took it first. A derived name that collides is an error rather than a
// silent suffix: PaymentRequest2 is a contract nobody meant to publish.
func (c *converter) claim(name, pointer string) bool {
	if prev, ok := c.owner[name]; ok {
		prevLoc := ""
		if n, _ := c.src.find(prev); n != nil {
			prevLoc = fmt.Sprintf(" (%s:%d:%d)", c.src.path, n.Line, n.Column)
		}
		c.errf(pointer,
			"set operationId, or x-interchange-name, on one of them",
			"%s collides with the name derived from %s%s", name, where(prev), prevLoc)
		return false
	}
	c.owner[name] = pointer
	return true
}

func (c *converter) convert() {
	c.owner = map[string]string{}
	c.schemaKind = map[string]schemaKind{}

	c.file = &protoFile{Package: c.pkg, source: c.src.path}
	if c.opt.GoPackagePrefix != "" {
		c.file.GoPackage = strings.TrimSuffix(c.opt.GoPackagePrefix, "/") + "/" + strings.ReplaceAll(c.pkg, ".", "/")
	}
	c.file.Path = strings.ReplaceAll(c.pkg, ".", "/") + "/" + snake(c.service) + ".proto"

	c.checkExtensionSpelling()
	c.convertComponents()
	c.convertPaths()
}

// checkExtensionSpelling refuses an unknown x-interchange-* key anywhere in the
// document. A misspelled annotation is worse than a missing one: the author
// believes the RPC is annotated and nothing says otherwise.
func (c *converter) checkExtensionSpelling() {
	var walk func(pointer string)
	walk = func(pointer string) {
		n, ok := c.src.value(pointer)
		if !ok || n == nil {
			return
		}
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				if strings.HasPrefix(k.Value, "x-interchange-") && !knownExtensions[k.Value] {
					c.errf(ptr(pointer, k.Value), "see README.md for the x-interchange-* extensions",
						"%s is not an interchange extension", k.Value)
				}
				walk(ptr(pointer, k.Value))
			}
		case yaml.SequenceNode:
			for i := range n.Content {
				walk(ptr(pointer, strconv.Itoa(i)))
			}
		}
	}
	walk("")
}

// requireImport records an import the emitted file needs, and refuses at the
// annotation when nobody supplied its descriptors. The optional modules'
// annotation protos arrive in Options.Deps precisely so that emitting an auth
// annotation does not make /auth a dependency of this frontend -- and a
// missing one has to be a located diagnostic, not a compiler error against
// source the author never wrote.
func (c *converter) requireImport(path string, at node) {
	c.file.importPath(path)
	if resolvable(c.deps, path) {
		return
	}
	c.diags = append(c.diags, at.diag(
		fmt.Sprintf("this annotation needs %s, which is not among the descriptors provided", path),
		"pass it in Options.Deps -- `ix` supplies the annotation protos for every module you have installed"))
}

// ---------------------------------------------------------------- components

func (c *converter) convertComponents() {
	if c.doc.Components == nil || c.doc.Components.Schemas == nil {
		return
	}
	// Two passes: classify every component first, so a reference to a schema
	// declared later in the document resolves to the same thing as one to a
	// schema declared earlier.
	for name, proxy := range c.doc.Components.Schemas.FromOldest() {
		c.nSchemas++
		p := ptr("/components/schemas", name)
		s := schemaOf(proxy)
		if s == nil {
			c.errf(p, "check the $ref target", "schema could not be resolved")
			continue
		}
		switch {
		case isEnum(s):
			c.schemaKind[name] = schemaKind{kind: "enum", name: camel(name), ptr: p}
		case isObject(s) || hasComposition(s):
			c.schemaKind[name] = schemaKind{kind: "message", name: camel(name), ptr: p}
		default:
			// A named scalar or array is a type alias, and proto has no
			// aliases: it is inlined at every use instead, which is lossless
			// on the wire.
			c.schemaKind[name] = schemaKind{kind: "alias", ptr: p}
		}
	}

	names := make([]string, 0, c.doc.Components.Schemas.Len())
	for name := range c.doc.Components.Schemas.FromOldest() {
		names = append(names, name)
	}
	for _, name := range names {
		k := c.schemaKind[name]
		proxy, _ := c.doc.Components.Schemas.Get(name)
		s := schemaOf(proxy)
		if s == nil {
			continue
		}
		switch k.kind {
		case "enum":
			if !c.claim(k.name, k.ptr) {
				continue
			}
			if e := c.buildEnum(k.ptr, k.name, s); e != nil {
				c.file.Enums = append(c.file.Enums, e)
				c.nMessages++
			}
		case "message":
			if !c.claim(k.name, k.ptr) {
				continue
			}
			m := &protoMessage{Name: k.name, Comment: comment(s.Description)}
			c.fillMessage(k.ptr, m, s)
			c.file.Messages = append(c.file.Messages, m)
			c.nMessages++
		case "alias":
			c.notef(k.ptr, "named %s is a type alias; proto has none, so it is inlined at every use", typeWord(s))
		}
	}
}

func infoDescription(i *base.Info) string {
	if i == nil {
		return ""
	}
	return i.Description
}

func typeWord(s *base.Schema) string {
	if len(s.Type) > 0 {
		return s.Type[0]
	}
	return "schema"
}

// fillMessage populates a message from an object schema: allOf members first
// (flattened, in document order), then the schema's own properties, then any
// oneOf/anyOf variants the author opted into.
func (c *converter) fillMessage(pointer string, m *protoMessage, s *base.Schema) {
	c.number(m, c.collect(pointer, m, s, map[string]bool{}))
}

// placed is a field plus where it came from, so a numbering conflict can be
// reported at the right line.
type placed struct {
	field   *protoField
	pointer string
	Number  int
}

// collect walks an object schema into fields. seen guards against an allOf
// cycle and records property names so a conflicting redefinition is an error
// rather than a last-one-wins merge.
func (c *converter) collect(pointer string, m *protoMessage, s *base.Schema, seen map[string]bool) []placed {
	var out []placed

	if s.Not != nil {
		c.errf(ptr(pointer, "not"), "express the constraint in the handler, or drop it",
			"'not' has no canonical proto form")
	}

	for i, member := range s.AllOf {
		mp := ptr(ptr(pointer, "allOf"), strconv.Itoa(i))
		ms, _, mptr := c.deref(mp, member)
		if ms == nil {
			continue
		}
		if !isObject(ms) && len(ms.AllOf) == 0 {
			c.errf(mp, "allOf can only be flattened when every member is an object schema",
				"'allOf' member is %s, not an object", typeWord(ms))
			continue
		}
		if len(ms.OneOf) > 0 || len(ms.AnyOf) > 0 {
			c.errf(mp, "flatten the member, or hoist it out of the allOf",
				"'allOf' member uses 'oneOf'/'anyOf', which cannot be flattened")
			continue
		}
		if seen[mptr] {
			c.errf(mp, "break the cycle", "'allOf' member %s is cyclic", where(mptr))
			continue
		}
		seen[mptr] = true
		out = append(out, c.collect(mptr, m, ms, seen)...)
		delete(seen, mptr)
	}
	if len(s.AllOf) > 0 {
		c.notef(ptr(pointer, "allOf"), "%d allOf member(s) flattened into %s", len(s.AllOf), m.Name)
	}

	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	if s.Properties != nil {
		for name, proxy := range s.Properties.FromOldest() {
			pp := ptr(ptr(pointer, "properties"), name)
			f := c.buildField(pp, m, name, proxy, required[name])
			if f == nil {
				continue
			}
			out = append(out, *f)
		}
	}
	out = append(out, c.buildOneof(pointer, m, s)...)
	return out
}

// buildOneof handles oneOf/anyOf. Neither has a canonical proto form, so both
// are refused unless x-interchange-oneof names the proto oneof to emit -- the
// opt-in is the author stating that the variants really are exclusive fields
// of one message.
func (c *converter) buildOneof(pointer string, m *protoMessage, s *base.Schema) []placed {
	variants, key := s.OneOf, "oneOf"
	if len(variants) == 0 {
		variants, key = s.AnyOf, "anyOf"
	}
	if len(variants) == 0 {
		return nil
	}
	name := extString(s.Extensions, extOneof)
	if name == "" {
		c.errAt(ptr(pointer, key), pointer, "use a proto oneof, or flatten the variants and set x-interchange-oneof",
			"'%s' has no canonical proto form", key)
		return nil
	}
	if key == "anyOf" {
		c.notef(ptr(pointer, key), "'anyOf' emitted as oneof %s; proto cannot express more than one variant at a time", snake(name))
	}
	if !protoIdent(snake(name)) {
		c.errf(ptr(pointer, extOneof), "use a lower_snake_case identifier",
			"x-interchange-oneof: %q is not a proto identifier", name)
		return nil
	}
	m.Oneofs = append(m.Oneofs, snake(name))

	var out []placed
	for i, v := range variants {
		vp := ptr(ptr(pointer, key), strconv.Itoa(i))
		if !v.IsReference() {
			c.errf(vp, "move the variant into components/schemas and $ref it",
				"'%s' variant %d is inline, so it has no name to give the oneof field", key, i)
			continue
		}
		ref := refName(v.GetReference())
		k, ok := c.schemaKind[ref]
		if !ok || k.kind == "alias" {
			c.errf(vp, "each variant must $ref an object or enum schema",
				"'%s' variant %s is not a message", key, ref)
			continue
		}
		out = append(out, placed{
			field:   &protoField{Name: snake(ref), Type: k.name, Oneof: snake(name)},
			pointer: vp,
		})
	}
	return out
}

// buildField maps one property to one field.
func (c *converter) buildField(pointer string, m *protoMessage, name string, proxy *base.SchemaProxy, required bool) *placed {
	// Property-site extensions are read from the raw document rather than the
	// model: a $ref property has its sibling keys resolved away, and a pin
	// belongs to the property, not to the schema it points at.
	if n, ok := extensionNode(c.src, pointer, extSkip); ok && n.boolean() {
		return nil
	}
	fname := snake(name)
	if !protoIdent(fname) {
		c.errf(pointer, "rename the property, or set x-interchange-name",
			"property %q does not map to a proto field name", name)
		return nil
	}
	s, _, sptr := c.deref(pointer, proxy)
	if s == nil {
		return nil
	}
	t := c.typeFor(pointer, proxy, m, camel(name))
	if t == nil {
		return nil
	}
	_ = sptr
	// The comment is the property's own description, read from the document.
	// A $ref property has no description of its own -- OpenAPI ignores keys
	// beside a $ref -- and copying the target's onto every use just repeats
	// the message's own comment at each field.
	desc, _ := c.src.scalar(ptr(pointer, "description"))
	f := &protoField{Name: fname, Type: t.name, Repeated: t.repeated, Comment: comment(desc)}

	nullable, ok := c.nullability(pointer, s)
	if !ok {
		return nil
	}
	switch {
	case nullable && (t.repeated || t.isMap):
		c.errf(pointer, "drop nullable: a repeated field and a map have no presence in proto3",
			"%q is nullable and repeated, which proto3 cannot express", name)
		return nil
	case required && nullable:
		c.errf(pointer, "drop nullable, drop it from required, or set x-interchange-nullable: optional",
			"%q is required and nullable: proto3 cannot distinguish present-but-null from absent", name)
		return nil
	case !required && !t.repeated && !t.isMap && !t.isMessage:
		// Absent and null collapse to the same thing on the wire; optional at
		// least keeps "the client did not send it" distinguishable from zero.
		f.Optional = true
	}

	pin := placed{field: f, pointer: pointer}
	if fn, ok := extensionNode(c.src, pointer, extField); ok {
		n := fn.str()
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 {
			c.errf(ptr(pointer, extField), "x-interchange-field must be a positive integer",
				"x-interchange-field: %q is not a field number", n)
			return nil
		}
		f.Number = v
		pin.Number = v
	}
	return &pin
}

// nullability reads 3.0 `nullable: true` and 3.1 `type: [T, "null"]`, and
// applies the x-interchange-nullable opt-out.
func (c *converter) nullability(pointer string, s *base.Schema) (bool, bool) {
	nullable := s.Nullable != nil && *s.Nullable
	for _, t := range s.Type {
		if t == "null" {
			nullable = true
		}
	}
	nn, _ := extensionNode(c.src, pointer, extNullable)
	switch v := nn.str(); v {
	case "":
	case "optional":
		return false, true
	default:
		c.errf(ptr(pointer, extNullable), "the only accepted value is 'optional'",
			"x-interchange-nullable: %q is not understood", v)
		return false, false
	}
	return nullable, true
}

type protoType struct {
	name      string
	repeated  bool
	isMessage bool
	isMap     bool
}

// typeOf maps a schema to a proto type, generating nested messages and enums
// under host as it goes. hint names anything it has to generate.
func (c *converter) typeOf(sp *base.SchemaProxy, pointer string, host *protoMessage, hint string, s *base.Schema) *protoType {
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 || (isObject(s) && s.Properties != nil && s.Properties.Len() > 0) || len(s.AllOf) > 0 {
		// An inline composite becomes a nested message named after the
		// property that holds it.
		if isEnum(s) {
			return c.nestedEnum(pointer, host, hint, s)
		}
		nested := &protoMessage{Name: hint, Comment: comment(s.Description)}
		host.Nested = append(host.Nested, nested)
		c.fillMessage(pointer, nested, s)
		return &protoType{name: hint, isMessage: true}
	}
	if isEnum(s) {
		return c.nestedEnum(pointer, host, hint, s)
	}

	base := baseType(s)
	switch base {
	case "array":
		items := itemsOf(s)
		if items == nil {
			c.errf(pointer, "give the array an 'items' schema", "array has no 'items'")
			return nil
		}
		ip := ptr(pointer, "items")
		is, _, _ := c.deref(ip, items)
		if is == nil {
			return nil
		}
		if !items.IsReference() && baseType(is) == "array" {
			c.errf(ip, "wrap the inner array in an object schema",
				"an array of arrays has no proto form: repeated cannot nest")
			return nil
		}
		it := c.typeFor(ip, items, host, singular(hint))
		if it == nil {
			return nil
		}
		if it.isMap {
			c.errf(ip, "wrap the map in an object schema", "an array of maps has no proto form")
			return nil
		}
		return &protoType{name: it.name, repeated: true, isMessage: it.isMessage}

	case "object", "":
		if ap := additionalOf(s); ap != nil {
			vp := ptr(pointer, "additionalProperties")
			vt := c.typeFor(vp, ap, host, hint)
			if vt == nil {
				return nil
			}
			if vt.repeated || vt.isMap {
				c.errf(vp, "wrap the value in an object schema",
					"a map of arrays or maps has no proto form")
				return nil
			}
			return &protoType{name: fmt.Sprintf("map<string, %s>", vt.name), isMap: true}
		}
		if base == "object" && additionalIsFree(s) {
			c.file.importPath("google/protobuf/struct.proto")
			c.notef(pointer, "free-form object mapped to google.protobuf.Struct; its shape is not part of the contract")
			return &protoType{name: "google.protobuf.Struct", isMessage: true}
		}
		c.errf(pointer, "give it a type, or properties, or additionalProperties",
			"schema has no type: proto has no 'any'")
		return nil

	case "string":
		switch s.Format {
		case "date-time":
			c.file.importPath("google/protobuf/timestamp.proto")
			return &protoType{name: "google.protobuf.Timestamp", isMessage: true}
		case "byte", "binary":
			return &protoType{name: "bytes"}
		}
		return &protoType{name: "string"}

	case "integer":
		switch s.Format {
		case "int32":
			return &protoType{name: "int32"}
		case "int64", "":
			return &protoType{name: "int64"}
		default:
			c.notef(pointer, "integer format %q is not a proto width; using int64", s.Format)
			return &protoType{name: "int64"}
		}

	case "number":
		switch s.Format {
		case "float":
			return &protoType{name: "float"}
		case "double", "":
			return &protoType{name: "double"}
		default:
			c.notef(pointer, "number format %q is not a proto width; using double", s.Format)
			return &protoType{name: "double"}
		}

	case "boolean":
		return &protoType{name: "bool"}

	case "null":
		c.errf(pointer, "use a nullable typed schema instead",
			"a 'null'-only schema has no proto form")
		return nil
	}
	c.errf(pointer, "use string, integer, number, boolean, array or object",
		"type %q has no proto form", base)
	return nil
}

func (c *converter) nestedEnum(pointer string, host *protoMessage, hint string, s *base.Schema) *protoType {
	e := c.buildEnum(pointer, hint, s)
	if e == nil {
		return nil
	}
	host.Enums = append(host.Enums, e)
	return &protoType{name: hint}
}

// buildEnum turns a string enum into a proto enum. The zero value is always a
// synthesised _UNSPECIFIED, because proto3 has no way to say "unset" otherwise
// and the first declared value would silently become the default.
func (c *converter) buildEnum(pointer, name string, s *base.Schema) *protoEnum {
	if bt := baseType(s); bt != "string" && bt != "" {
		c.errf(ptr(pointer, "enum"), "drop the enum and use the plain type, or make the values strings",
			"a %s enum has no proto form: proto enum values are named constants", bt)
		return nil
	}
	e := &protoEnum{Name: name, Comment: comment(s.Description)}
	prefix := screaming(name) + "_"
	e.Values = append(e.Values, protoEnumValue{Name: prefix + "UNSPECIFIED", Number: 0})
	seen := map[string]bool{prefix + "UNSPECIFIED": true}
	for i, v := range s.Enum {
		if v == nil || v.Value == "" {
			c.errf(ptr(ptr(pointer, "enum"), strconv.Itoa(i)),
				"remove the empty value; the synthesised _UNSPECIFIED is the zero value",
				"enum value %d is empty", i)
			continue
		}
		vn := prefix + screaming(v.Value)
		if !protoIdent(vn) {
			c.errf(ptr(ptr(pointer, "enum"), strconv.Itoa(i)),
				"rename the value to something that maps to an identifier",
				"enum value %q does not map to a proto enum value name", v.Value)
			continue
		}
		if seen[vn] {
			c.errf(ptr(ptr(pointer, "enum"), strconv.Itoa(i)),
				"two values differing only in punctuation or case collide once uppercased",
				"enum value %q collides with an earlier value", v.Value)
			continue
		}
		seen[vn] = true
		e.Values = append(e.Values, protoEnumValue{Name: vn, Number: i + 1})
	}
	return e
}

// deref resolves a schema proxy, following an internal $ref to the component
// it names and returning the pointer that locates it -- so a diagnostic about
// a shared schema points at the schema, not at the reference.
func (c *converter) deref(pointer string, proxy *base.SchemaProxy) (*base.Schema, *base.SchemaProxy, string) {
	if proxy == nil {
		c.errf(pointer, "give it a schema", "no schema")
		return nil, nil, pointer
	}
	if !proxy.IsReference() {
		s := proxy.Schema()
		if s == nil {
			c.errf(pointer, "check the schema", "schema could not be built")
			return nil, nil, pointer
		}
		return s, proxy, pointer
	}
	name := refName(proxy.GetReference())
	target, ok := c.doc.Components.Schemas.Get(name)
	if !ok {
		c.errf(pointer, "check the $ref target", "$ref %s does not resolve", proxy.GetReference())
		return nil, nil, pointer
	}
	tp := ptr("/components/schemas", name)
	ts := schemaOf(target)
	if ts == nil {
		c.errf(pointer, "check the $ref target", "$ref %s could not be built", proxy.GetReference())
		return nil, nil, tp
	}
	return ts, target, tp
}

// typeFor maps a property, item or parameter schema to a proto type. A $ref to
// a component that became a message or an enum is a reference to that type; a
// $ref to a scalar alias, and anything inline, is expanded here.
func (c *converter) typeFor(pointer string, proxy *base.SchemaProxy, host *protoMessage, hint string) *protoType {
	if proxy != nil && proxy.IsReference() {
		if name, ok := c.refType(proxy); ok {
			k := c.schemaKind[refName(proxy.GetReference())]
			return &protoType{name: name, isMessage: k.kind == "message"}
		}
	}
	s, sp, sptr := c.deref(pointer, proxy)
	if s == nil {
		return nil
	}
	return c.typeOf(sp, sptr, host, hint, s)
}

// refType returns the proto type a $ref resolves to, or "" when the reference
// names an alias that has to be inlined at the use site instead.
func (c *converter) refType(proxy *base.SchemaProxy) (string, bool) {
	if proxy == nil || !proxy.IsReference() {
		return "", false
	}
	k, ok := c.schemaKind[refName(proxy.GetReference())]
	if !ok || k.kind == "alias" {
		return "", false
	}
	return k.name, true
}

func refName(ref string) string {
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return ref
	}
	return unescapePtr(ref[i+1:])
}

// -------------------------------------------------------------------- paths

// The proto files an emitted annotation imports.
const (
	httpAnnotationsProto = "google/api/annotations.proto"
	transportsProto      = "interchange/transport/v1/transports.proto"
	authProto            = "interchange/auth/v1/auth.proto"
	permissionsProto     = "interchange/auth/v1/permissions.proto"
	cliProto             = "interchange/cli/v1/cli.proto"
)

// httpMethods fixes the order operations are visited in, so the report and any
// collision diagnostic do not depend on map iteration.
var httpMethods = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

type operation struct {
	method  string
	path    string
	pointer string
	op      *v3.Operation
	name    string
}

func (c *converter) convertPaths() {
	if c.doc.Paths == nil {
		return
	}
	svc := &protoService{Name: c.service, Comment: comment(infoDescription(c.doc.Info))}
	if n, ok := extensionNode(c.src, "", extServiceTransports); ok {
		t, diags := decodeTransports(n)
		c.diags = append(c.diags, diags...)
		svc.Transports = t
		if t != nil {
			c.requireImport(transportsProto, n)
		}
	}

	c.file.Service = svc

	var ops []operation
	for path, item := range c.doc.Paths.PathItems.FromOldest() {
		c.nPaths++
		pp := ptr("/paths", path)
		found := item.GetOperations()
		for _, method := range httpMethods {
			op, ok := found.Get(method)
			if !ok || op == nil {
				continue
			}
			op2 := operation{method: method, path: path, pointer: ptr(pp, method), op: op}
			switch method {
			case "head", "options", "trace":
				c.errf(op2.pointer, "remove the operation; these are transport concerns, not contract methods",
					"%s has no RPC form", strings.ToUpper(method))
				continue
			}
			name, err := c.operationName(op2)
			if err != nil {
				c.errf(op2.pointer, "set operationId, or x-interchange-name, on this operation", "%s", err.Error())
				continue
			}
			op2.name = name
			ops = append(ops, op2)
		}
	}

	for i := range ops {
		if !c.claim(ops[i].name, ops[i].pointer) {
			ops[i].name = ""
		}
	}
	for _, op := range ops {
		if op.name == "" {
			continue
		}
		if m := c.buildMethod(op); m != nil {
			svc.Methods = append(svc.Methods, m)
			c.nRPCs++
		}
	}
	sort.Slice(svc.Methods, func(i, j int) bool { return svc.Methods[i].Name < svc.Methods[j].Name })
}

func (c *converter) operationName(op operation) (string, error) {
	if n := extString(op.op.Extensions, extName); n != "" {
		if !protoIdent(camel(n)) {
			return "", fmt.Errorf("x-interchange-name: %q is not a proto identifier", n)
		}
		return camel(n), nil
	}
	if id := op.op.OperationId; id != "" {
		n := camel(id)
		if !protoIdent(n) {
			return "", fmt.Errorf("operationId %q does not map to a proto identifier", id)
		}
		return n, nil
	}
	return rpcName(op.method, op.path)
}

func (c *converter) buildMethod(op operation) *protoMethod {
	req := &protoMessage{Name: op.name + "Request"}
	resp := &protoMessage{Name: op.name + "Response"}
	if !c.claim(req.Name, op.pointer) || !c.claim(resp.Name, op.pointer) {
		return nil
	}

	m := &protoMethod{
		Name:    op.name,
		Input:   req.Name,
		Output:  resp.Name,
		Comment: comment(firstNonEmpty(op.op.Summary, op.op.Description)),
	}

	body := c.buildRequest(op, req)
	c.buildResponse(op, resp)
	c.file.Messages = append(c.file.Messages, req, resp)

	c.file.importPath(httpAnnotationsProto)
	m.HTTP = &httpRule{Verb: op.method, Path: httpPath(op.path), Body: body}
	m.Idempotent = op.method == "get"

	c.annotate(op, m)
	return m
}

// buildRequest assembles the request message: path parameters in path order,
// then query parameters in document order, then the body. That order is the
// field-number rule for a request message and it is stable against everything
// except editing the path or reordering the parameter list.
func (c *converter) buildRequest(op operation, req *protoMessage) string {
	params := c.parameters(op)
	var fields []placed

	// Path parameters, in the order the path declares them.
	byName := map[string]paramSite{}
	for _, p := range params {
		if p.param.In == "path" {
			byName[p.param.Name] = p
		}
	}
	for _, name := range pathParams(op.path) {
		p, ok := byName[name]
		if !ok {
			c.errf(op.pointer, "declare it in 'parameters' with in: path",
				"path variable {%s} has no parameter declaration", name)
			continue
		}
		if f := c.paramField(p, req, true); f != nil {
			fields = append(fields, *f)
		}
		delete(byName, name)
	}
	for name, p := range byName {
		c.errf(p.pointer, "add it to the path template, or change its 'in'",
			"path parameter %q does not appear in %s", name, op.path)
	}

	for _, p := range params {
		if skipped(p) {
			continue
		}
		switch p.param.In {
		case "path":
			continue
		case "query":
			if f := c.paramField(p, req, p.param.Required != nil && *p.param.Required); f != nil {
				fields = append(fields, *f)
			}
		default:
			c.errf(p.pointer, "headers and cookies are transport metadata, not contract fields; remove it or set x-interchange-skip: true",
				"parameter %q is in: %s, which has no request-message form", p.param.Name, p.param.In)
		}
	}

	body := c.bodyFields(op, req, &fields)
	c.number(req, fields)
	return body
}

type paramSite struct {
	param   *v3.Parameter
	pointer string
}

// parameters merges path-item and operation parameters, operation last, so an
// operation-level parameter overrides the path-item default of the same name.
func (c *converter) parameters(op operation) []paramSite {
	var out []paramSite
	item, _ := c.doc.Paths.PathItems.Get(op.path)
	base := ptr("/paths", op.path)
	if item != nil {
		for i, p := range item.Parameters {
			out = append(out, paramSite{param: p, pointer: ptr(ptr(base, "parameters"), strconv.Itoa(i))})
		}
	}
	for i, p := range op.op.Parameters {
		out = append(out, paramSite{param: p, pointer: ptr(ptr(op.pointer, "parameters"), strconv.Itoa(i))})
	}
	seen := map[string]int{}
	var merged []paramSite
	for _, p := range out {
		key := p.param.In + " " + p.param.Name
		if i, ok := seen[key]; ok {
			merged[i] = p
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, p)
	}
	return merged
}

// skipped reports an x-interchange-skip on a parameter: the deliberate opt-out
// for a header or a query field that is transport plumbing rather than part of
// the contract.
func skipped(p paramSite) bool {
	return extBool(p.param.Extensions, extSkip)
}

func (c *converter) paramField(p paramSite, req *protoMessage, required bool) *placed {
	if skipped(p) {
		return nil
	}
	sp := ptr(p.pointer, "schema")
	name := snake(p.param.Name)
	if !protoIdent(name) {
		c.errf(p.pointer, "rename the parameter", "parameter %q does not map to a proto field name", p.param.Name)
		return nil
	}
	t := c.typeFor(sp, p.param.Schema, req, camel(p.param.Name))
	if t == nil {
		return nil
	}
	if p.param.In == "path" && (t.repeated || t.isMap || t.isMessage) {
		c.errf(sp, "path variables bind to scalar fields only",
			"path parameter %q is not a scalar", p.param.Name)
		return nil
	}
	if t.isMap || t.isMessage {
		c.errf(sp, "flatten it into named parameters, or move it into the body",
			"query parameter %q is not a scalar, so it cannot be bound", p.param.Name)
		return nil
	}
	f := &protoField{Name: name, Type: t.name, Repeated: t.repeated, Comment: comment(p.param.Description)}
	if !required && !t.repeated && !t.isMessage {
		f.Optional = true
	}
	return &placed{field: f, pointer: p.pointer}
}

// bodyFields adds the request body. A $ref body becomes one typed field and
// google.api.http names it; an inline object is flattened and the rule says
// body: "*". Both keep the path variables bindable, which a body of "*" over a
// nested message would not.
func (c *converter) bodyFields(op operation, req *protoMessage, fields *[]placed) string {
	rb := op.op.RequestBody
	if rb == nil {
		return ""
	}
	bp := ptr(op.pointer, "requestBody")
	if op.method == "get" || op.method == "delete" {
		c.errf(bp, "move the fields into query parameters, or use POST",
			"a %s with a requestBody cannot be transcoded", strings.ToUpper(op.method))
		return ""
	}
	proxy, cp := c.jsonContent(bp, rb.Content)
	if proxy == nil {
		return ""
	}
	s, _, _ := c.deref(cp, proxy)
	if s == nil {
		return ""
	}
	if name, ok := c.refType(proxy); ok {
		fname := snake(refName(proxy.GetReference()))
		*fields = append(*fields, placed{
			field:   &protoField{Name: fname, Type: name},
			pointer: cp,
		})
		return fname
	}
	if isObject(s) || len(s.AllOf) > 0 {
		if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
			*fields = append(*fields, c.buildOneof(cp, req, s)...)
		}
		*fields = append(*fields, c.collectBody(cp, req, s)...)
		return "*"
	}
	t := c.typeFor(cp, proxy, req, "Body")
	if t == nil {
		return ""
	}
	*fields = append(*fields, placed{field: &protoField{Name: "body", Type: t.name, Repeated: t.repeated}, pointer: cp})
	return "body"
}

func (c *converter) collectBody(pointer string, m *protoMessage, s *base.Schema) []placed {
	return c.collect(pointer, m, s, map[string]bool{})
}

// buildResponse maps the success response. Exactly one 2xx may carry a body:
// an RPC returns one message, and picking one of several by rule would be a
// guess about which shape the client should expect.
func (c *converter) buildResponse(op operation, resp *protoMessage) {
	if op.op.Responses == nil {
		return
	}
	var codes []string
	for code := range op.op.Responses.Codes.FromOldest() {
		if strings.HasPrefix(code, "2") {
			codes = append(codes, code)
		}
	}
	var withBody []string
	for _, code := range codes {
		r, _ := op.op.Responses.Codes.Get(code)
		if r != nil && r.Content != nil && r.Content.Len() > 0 {
			withBody = append(withBody, code)
		}
	}
	rp := ptr(op.pointer, "responses")
	switch len(withBody) {
	case 0:
		return
	case 1:
	default:
		c.errf(rp, "an RPC returns one message; give the alternatives their own operation",
			"%d success responses carry a body (%s)", len(withBody), strings.Join(withBody, ", "))
		return
	}
	code := withBody[0]
	r, _ := op.op.Responses.Codes.Get(code)
	cp := ptr(rp, code)
	proxy, mp := c.jsonContent(cp, r.Content)
	if proxy == nil {
		return
	}
	s, _, _ := c.deref(mp, proxy)
	if s == nil {
		return
	}
	var fields []placed
	if name, ok := c.refType(proxy); ok {
		fields = append(fields, placed{
			field:   &protoField{Name: snake(refName(proxy.GetReference())), Type: name},
			pointer: mp,
		})
	} else if isObject(s) || len(s.AllOf) > 0 {
		if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
			fields = append(fields, c.buildOneof(mp, resp, s)...)
		}
		fields = append(fields, c.collectBody(mp, resp, s)...)
	} else {
		t := c.typeFor(mp, proxy, resp, "Body")
		if t == nil {
			return
		}
		name := "body"
		if t.repeated {
			name = "items"
		}
		fields = append(fields, placed{field: &protoField{Name: name, Type: t.name, Repeated: t.repeated}, pointer: mp})
	}
	c.number(resp, fields)
}

// jsonContent picks the media type to model. JSON is the only content a
// descriptor can describe; anything else would need a field whose bytes have
// no schema, which is a contract that says nothing.
func (c *converter) jsonContent(pointer string, content *orderedmap.Map[string, *v3.MediaType]) (*base.SchemaProxy, string) {
	if content == nil || content.Len() == 0 {
		return nil, ""
	}
	var types []string
	for ct := range content.FromOldest() {
		types = append(types, ct)
	}
	pick := ""
	for _, ct := range types {
		base := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
		if base == "application/json" || strings.HasSuffix(base, "+json") {
			if pick == "" || base == "application/json" {
				pick = ct
			}
		}
	}
	if pick == "" {
		sort.Strings(types)
		c.errf(ptr(pointer, "content"), "add an application/json media type",
			"no JSON media type (found %s)", strings.Join(types, ", "))
		return nil, ""
	}
	mt, _ := content.Get(pick)
	if mt == nil || mt.Schema == nil {
		c.errf(ptr(ptr(pointer, "content"), pick), "give the media type a schema", "media type %s has no schema", pick)
		return nil, ""
	}
	return mt.Schema, ptr(ptr(ptr(pointer, "content"), pick), "schema")
}

// number assigns field numbers: document order, honouring pins, skipping any
// number a pin already took.
func (c *converter) number(m *protoMessage, fields []placed) {
	taken := map[int]string{}
	for _, f := range fields {
		if f.Number == 0 {
			continue
		}
		if prev, dup := taken[f.Number]; dup {
			c.errf(f.pointer, "give each x-interchange-field a distinct number",
				"field number %d is already used by %s", f.Number, prev)
			continue
		}
		taken[f.Number] = f.field.Name
	}
	next := 1
	for _, f := range fields {
		if f.field.Number == 0 {
			for taken[next] != "" {
				next++
			}
			f.field.Number = next
			taken[next] = f.field.Name
			next++
		}
		m.Fields = append(m.Fields, f.field)
	}
}

// ------------------------------------------------------------- annotations

// annotate resolves the four annotations for one method. An annotation on the
// operation and the same annotation in the sidecar is an ERROR, not a
// precedence rule: silent precedence is how a security posture gets
// overwritten by a file nobody reads (ADR-0044). A document-level
// x-interchange-* remains the default for operations that declare neither,
// which is a default rather than a conflict.
func (c *converter) annotate(op operation, m *protoMethod) {
	procedure := "/" + c.pkg + "." + c.service + "/" + m.Name
	side, hasSide := c.sc.lookup(procedure)

	get := func(key string) (node, bool) {
		inline, hasInline := extensionNode(c.src, op.pointer, key)
		var fromSide node
		hasFromSide := false
		if hasSide {
			fromSide, hasFromSide = side.entry(strings.TrimPrefix(key, "x-interchange-"))
		}
		switch {
		case hasInline && hasFromSide:
			c.errf(op.pointer,
				"set it in one place: remove "+key+" from the operation, or remove the sidecar entry",
				"%s is set both on the operation and in the sidecar for %s", key, procedure)
			return inline, true
		case hasInline:
			return inline, true
		case hasFromSide:
			return fromSide, true
		}
		if n, ok := extensionNode(c.src, "", key); ok {
			return n, true
		}
		return node{}, false
	}

	if n, ok := get(extTransports); ok {
		t, diags := decodeTransports(n)
		c.diags = append(c.diags, diags...)
		m.Transports = t
		if t != nil {
			c.requireImport(transportsProto, n)
		}
	}
	if n, ok := get(extInternal); ok {
		m.Internal = n.boolean()
		if m.Internal {
			c.requireImport(transportsProto, n)
		}
	}
	if n, ok := get(extCLI); ok {
		cli, diags := decodeCLI(n)
		c.diags = append(c.diags, diags...)
		m.CLI = cli
		if cli != nil {
			c.requireImport(cliProto, n)
		}
	}
	if n, ok := get(extAuth); ok {
		a, diags := decodeAuth(n)
		c.diags = append(c.diags, diags...)
		m.Auth = a
		if a != nil {
			c.requireImport(authProto, n)
			if a.Permission != nil {
				c.requireImport(permissionsProto, n)
			}
		}
	} else {
		switch c.opt.Params["on_missing_auth"] {
		case "ignore":
		case "warn":
			c.diags = append(c.diags, c.src.at(interchange.SeverityWarning, op.pointer,
				"no authorization declared", "add x-interchange-auth, or a sidecar entry"))
		default:
			c.errf(op.pointer, "add x-interchange-auth, or a sidecar entry", "no authorization declared")
		}
	}

}

// ------------------------------------------------------------------ helpers

func schemaOf(p *base.SchemaProxy) *base.Schema {
	if p == nil {
		return nil
	}
	return p.Schema()
}

func isObject(s *base.Schema) bool {
	if s == nil {
		return false
	}
	for _, t := range s.Type {
		if t == "object" {
			return true
		}
	}
	return s.Properties != nil && s.Properties.Len() > 0
}

func hasComposition(s *base.Schema) bool {
	return s != nil && (len(s.AllOf) > 0 || len(s.OneOf) > 0 || len(s.AnyOf) > 0)
}

func isEnum(s *base.Schema) bool {
	return s != nil && len(s.Enum) > 0
}

// baseType is the schema's type with 3.1's null member removed, since null is
// nullability rather than a type of its own.
func baseType(s *base.Schema) string {
	if s == nil {
		return ""
	}
	for _, t := range s.Type {
		if t != "null" {
			return t
		}
	}
	if len(s.Type) > 0 {
		return "null"
	}
	if s.Properties != nil && s.Properties.Len() > 0 {
		return "object"
	}
	return ""
}

func itemsOf(s *base.Schema) *base.SchemaProxy {
	if s == nil || s.Items == nil || !s.Items.IsA() {
		return nil
	}
	return s.Items.A
}

func additionalOf(s *base.Schema) *base.SchemaProxy {
	if s == nil || s.AdditionalProperties == nil || !s.AdditionalProperties.IsA() {
		return nil
	}
	return s.AdditionalProperties.A
}

// additionalIsFree reports the free-form object case: no properties, and
// additionalProperties either absent or literally true.
func additionalIsFree(s *base.Schema) bool {
	if s == nil || (s.Properties != nil && s.Properties.Len() > 0) {
		return false
	}
	if s.AdditionalProperties == nil {
		return true
	}
	return s.AdditionalProperties.IsB() && s.AdditionalProperties.B
}

func comment(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\n")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func extString(m *orderedmap.Map[string, *yamlNode], key string) string {
	if m == nil {
		return ""
	}
	n, ok := m.Get(key)
	if !ok || n == nil {
		return ""
	}
	return n.Value
}

func extBool(m *orderedmap.Map[string, *yamlNode], key string) bool {
	return extString(m, key) == "true"
}

// extensionNode reads an x-interchange-* key out of the raw document rather
// than the model, because the raw node is what carries the position a
// diagnostic about a bad annotation has to point at.
func extensionNode(src *source, pointer, key string) (node, bool) {
	n, ok := src.value(ptr(pointer, key))
	if !ok || n == nil {
		return node{}, false
	}
	return node{file: src.path, n: n}, true
}
