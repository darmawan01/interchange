package dsl

import (
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const markerKey = "interchange"

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseFile walks one DSL document into the model. It reports every problem
// it can see rather than stopping at the first, so one run of `ix import`
// tells you everything you have to fix.
func (c *collector) parseFile(path string, doc *yaml.Node) *file {
	root := documentRoot(doc)
	m := c.asMapping(root, "document")
	if m == nil {
		return nil
	}
	c.only(m, "document", markerKey, "package", "go_package", "file", "messages", "enums", "services")

	if v := c.str(m, markerKey, markerKey); v != "" && v != "v1" {
		c.errorf(m.get(markerKey), "write `interchange: v1`", "unknown DSL version %q", v)
	}

	f := &file{path: path}
	f.pkg = c.requiredStr(m, "package", "document",
		"add `package: acme.catalog.v1` -- the proto package the emitted file lands in")
	f.pkgNode = m.keyNode("package")
	if f.pkg != "" && !validPackage(f.pkg) {
		c.errorf(m.get("package"), "use dot-separated lower_snake_case segments, ending in a version: acme.catalog.v1",
			"%q is not a valid proto package", f.pkg)
	}
	f.goPackage = c.str(m, "go_package", "go_package")
	f.baseName = c.str(m, "file", "file")
	if f.baseName == "" {
		f.baseName = baseName(path)
	}
	if !identRe.MatchString(strings.ReplaceAll(f.baseName, "-", "_")) {
		c.errorf(m.keyNode("file"), "set `file: catalog` to name the emitted catalog.proto",
			"%q is not a usable proto file name", f.baseName)
	}

	if n := m.get("messages"); n != nil {
		f.messages = c.parseMessages(n, "messages")
	}
	if n := m.get("enums"); n != nil {
		f.enums = c.parseEnums(n, "enums")
	}
	if n := m.get("services"); n != nil {
		f.services = c.parseServices(n)
	}
	return f
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc != nil && doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func (c *collector) parseMessages(n *yaml.Node, what string) []*message {
	m := c.asMapping(n, what)
	if m == nil {
		return nil
	}
	var out []*message
	for _, name := range m.keys {
		p := m.kv[name]
		c.checkIdent(p.key, name, "message name")
		msg := &message{name: name, node: p.key, doc: docOf(p.key)}
		body := c.asMapping(p.val, what+"."+name)
		if body == nil {
			continue
		}
		c.only(body, what+"."+name, "fields", "messages", "enums")
		if fn := body.get("fields"); fn != nil {
			msg.fields = c.parseFields(fn, what+"."+name+".fields")
		}
		if nn := body.get("messages"); nn != nil {
			msg.messages = c.parseMessages(nn, what+"."+name+".messages")
		}
		if en := body.get("enums"); en != nil {
			msg.enums = c.parseEnums(en, what+"."+name+".enums")
		}
		out = append(out, msg)
	}
	return out
}

func (c *collector) parseFields(n *yaml.Node, what string) []*field {
	m := c.asMapping(n, what)
	if m == nil {
		return nil
	}
	var out []*field
	for _, name := range m.keys {
		p := m.kv[name]
		c.checkIdent(p.key, name, "field name")
		fld := &field{name: name, node: p.key, doc: docOf(p.key)}
		body := c.asMapping(p.val, what+"."+name)
		if body == nil {
			continue
		}
		c.only(body, what+"."+name, "type", "n", "repeated", "optional")

		fld.typ = c.requiredStr(body, "type", what+"."+name,
			"give the field a type: `type: string`")
		fld.typeNode = body.get("type")
		if fld.typeNode == nil {
			fld.typeNode = p.key
		}

		// Field numbers are the wire contract, so the DSL will not invent
		// one: a number derived from declaration order silently changes the
		// moment somebody reorders the YAML, and a renumbered field is a
		// wire-incompatible change that no reviewer would see in the diff.
		if !body.has("n") {
			c.errorf(p.key, "give the field an explicit number: `n: 1` -- the DSL will not derive one, because a derived number changes when the file is reordered",
				"%s: field %q has no number", what, name)
		} else {
			fld.numNode = body.get("n")
			if v, ok := c.intAt(fld.numNode, what+"."+name+".n"); ok {
				fld.num = v
				c.checkFieldNumber(fld.numNode, what, name, v)
			}
		}

		fld.repeated = c.boolean(body, "repeated", what+"."+name)
		fld.optional = c.boolean(body, "optional", what+"."+name)
		if fld.repeated && fld.optional {
			c.errorf(p.key, "a repeated field is already absent when empty; drop `optional: true`",
				"%s: field %q is both repeated and optional", what, name)
		}
		out = append(out, fld)
	}
	return out
}

func (c *collector) checkFieldNumber(n *yaml.Node, what, name string, v int) {
	switch {
	case v < 1 || v > 536870911:
		c.errorf(n, "field numbers run from 1 to 536870911", "%s: field %q has number %d, out of range", what, name, v)
	case v >= 19000 && v <= 19999:
		c.errorf(n, "pick a number outside 19000-19999", "%s: field %q uses number %d, which protobuf reserves", what, name, v)
	}
}

func (c *collector) parseEnums(n *yaml.Node, what string) []*enum {
	m := c.asMapping(n, what)
	if m == nil {
		return nil
	}
	var out []*enum
	for _, name := range m.keys {
		p := m.kv[name]
		c.checkIdent(p.key, name, "enum name")
		e := &enum{name: name, node: p.key, doc: docOf(p.key)}
		body := c.asMapping(p.val, what+"."+name)
		if body == nil {
			continue
		}
		c.only(body, what+"."+name, "values")
		vals := body.get("values")
		if vals == nil {
			c.errorf(p.key, "list them as `VALUE_NAME: number`", "%s: enum %q has no values", what, name)
			continue
		}
		vm := c.asMapping(vals, what+"."+name+".values")
		if vm == nil {
			continue
		}
		for _, vn := range vm.keys {
			vp := vm.kv[vn]
			c.checkIdent(vp.key, vn, "enum value name")
			ev := &enumValue{name: vn, node: vp.key}
			if v, ok := c.intAt(vp.val, what+"."+name+".values."+vn); ok {
				ev.num = v
			}
			e.values = append(e.values, ev)
		}
		out = append(out, e)
	}
	return out
}

func (c *collector) parseServices(n *yaml.Node) []*service {
	m := c.asMapping(n, "services")
	if m == nil {
		return nil
	}
	var out []*service
	for _, name := range m.keys {
		p := m.kv[name]
		c.checkIdent(p.key, name, "service name")
		svc := &service{name: name, node: p.key, doc: docOf(p.key)}
		body := c.asMapping(p.val, "services."+name)
		if body == nil {
			continue
		}
		c.only(body, "services."+name, "transports", "group", "rpcs")
		svc.annot = c.parseAnnotations(body, "services."+name, "inline", serviceAnnotationKeys)

		rpcs := body.get("rpcs")
		if rpcs == nil {
			c.errorf(p.key, "add `rpcs:` with at least one entry", "services.%s: no rpcs", name)
			continue
		}
		rm := c.asMapping(rpcs, "services."+name+".rpcs")
		if rm == nil {
			continue
		}
		for _, rn := range rm.keys {
			rp := rm.kv[rn]
			c.checkIdent(rp.key, rn, "rpc name")
			what := "services." + name + ".rpcs." + rn
			r := &rpc{name: rn, node: rp.key, doc: docOf(rp.key)}
			rbody := c.asMapping(rp.val, what)
			if rbody == nil {
				continue
			}
			c.only(rbody, what, append([]string{"request", "response"}, rpcAnnotationKeys...)...)
			r.request = c.requiredStr(rbody, "request", what, "name the request message: `request: ListProvidersRequest`")
			r.reqNode = orKey(rbody, "request", rp.key)
			r.response = c.requiredStr(rbody, "response", what, "name the response message: `response: ListProvidersResponse`")
			r.respNode = orKey(rbody, "response", rp.key)
			r.annot = c.parseAnnotations(rbody, what, "inline", rpcAnnotationKeys)
			svc.rpcs = append(svc.rpcs, r)
		}
		out = append(out, svc)
	}
	return out
}

func orKey(m *mapping, key string, fallback *yaml.Node) *yaml.Node {
	if n := m.get(key); n != nil {
		return n
	}
	return fallback
}

func (c *collector) checkIdent(n *yaml.Node, name, what string) {
	if !identRe.MatchString(name) {
		c.errorf(n, "use letters, digits and underscores, starting with a letter",
			"%q is not a valid %s", name, what)
	}
}

// baseName strips the directory and every suffix, so catalog.ix.yaml emits
// catalog.proto.
func baseName(p string) string {
	b := filepath.Base(p)
	if i := strings.Index(b, "."); i > 0 {
		b = b[:i]
	}
	return b
}

func validPackage(p string) bool {
	for _, seg := range strings.Split(p, ".") {
		if !identRe.MatchString(seg) {
			return false
		}
	}
	return true
}
