package dsl

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The annotation vocabulary. It is deliberately the same set of names the
// sidecar in §09 uses, so an annotation reads identically whether it lives
// next to its RPC or in a separate file.
var (
	serviceAnnotationKeys = []string{"transports", "group"}
	rpcAnnotationKeys     = []string{"transports", "group", "http", "auth", "cli", "internal", "idempotency"}

	transportNames = []string{"RPC", "REST", "BUS", "MQTT", "WS"}
	authTypeNames  = []string{"API_KEY", "SESSION", "WORKLOAD"}
	verbNames      = []string{"READ", "CREATE", "EDIT", "DELETE"}
	httpMethods    = []string{"get", "post", "put", "patch", "delete"}
	idempotencies  = []string{"no_side_effects", "idempotent", "unknown"}
)

func (c *collector) parseAnnotations(m *mapping, what, origin string, _ []string) *annotations {
	a := &annotations{origin: origin}

	if ts, nodes := c.strList(m, "transports", what); ts != nil {
		a.transports = ts
		a.transportsNode = m.keyNode("transports")
		for i, t := range ts {
			if !oneOf(t, transportNames) {
				c.errorf(nodes[i], "the roads are "+strings.Join(transportNames, ", "),
					"%s: unknown transport %q", what, t)
			}
		}
	}
	a.group = c.str(m, "group", what+".group")

	if n := m.get("http"); n != nil {
		a.httpNode = m.keyNode("http")
		a.http = c.parseHTTP(n, what+".http")
	}
	if n := m.get("auth"); n != nil {
		a.authNode = m.keyNode("auth")
		a.auth = c.parseAuth(n, what+".auth")
	}
	if n := m.get("cli"); n != nil {
		a.cliNode = m.keyNode("cli")
		a.cli = c.parseCLI(n, what+".cli")
	}
	if m.has("internal") {
		a.internalSet = true
		a.internalNode = m.keyNode("internal")
		a.internal = c.boolean(m, "internal", what)
	}
	if m.has("idempotency") {
		a.idempotencyNode = m.keyNode("idempotency")
		a.idempotency = c.str(m, "idempotency", what+".idempotency")
		if !oneOf(a.idempotency, idempotencies) {
			c.errorf(m.get("idempotency"), "one of "+strings.Join(idempotencies, ", "),
				"%s: unknown idempotency %q", what, a.idempotency)
		}
	}
	return a
}

func (c *collector) parseHTTP(n *yaml.Node, what string) *httpRule {
	m := c.asMapping(n, what)
	if m == nil {
		return nil
	}
	c.only(m, what, append(append([]string{}, httpMethods...), "body")...)

	var found []string
	r := &httpRule{}
	for _, meth := range httpMethods {
		if m.has(meth) {
			found = append(found, meth)
			r.method = meth
			r.path = c.str(m, meth, what+"."+meth)
		}
	}
	switch len(found) {
	case 0:
		c.errorf(m.node, "write one of "+strings.Join(httpMethods, ", ")+": `http: {get: /v1/things}`",
			"%s: no HTTP method", what)
		return nil
	case 1:
	default:
		c.errorf(m.node, "split them into two RPCs -- one REST route per RPC",
			"%s: %d HTTP methods (%s); an RPC maps to exactly one route", what, len(found), strings.Join(found, ", "))
		return nil
	}
	if !strings.HasPrefix(r.path, "/") {
		c.errorf(m.get(r.method), "start the path with /", "%s: %q is not an absolute path", what, r.path)
	}
	r.body = c.str(m, "body", what+".body")
	if r.body == "" && (r.method == "post" || r.method == "put" || r.method == "patch") {
		r.body = "*"
	}
	return r
}

func (c *collector) parseAuth(n *yaml.Node, what string) *authRule {
	m := c.asMapping(n, what)
	if m == nil {
		return nil
	}
	c.only(m, what, "auth_types", "permission", "public", "platform")

	r := &authRule{}
	types, nodes := c.strList(m, "auth_types", what)
	r.authTypes = types
	for i, t := range types {
		if !oneOf(t, authTypeNames) {
			c.errorf(nodes[i], "the credential kinds are "+strings.Join(authTypeNames, ", "),
				"%s: unknown auth type %q", what, t)
		}
	}
	r.public = c.boolean(m, "public", what)
	r.platform = c.boolean(m, "platform", what)

	if pn := m.get("permission"); pn != nil {
		pm := c.asMapping(pn, what+".permission")
		if pm == nil {
			return r
		}
		c.only(pm, what+".permission", "resource", "verb")
		r.hasPerm = true
		r.resource = c.requiredStr(pm, "resource", what+".permission", "name the resource: `resource: providers`")
		r.verb = c.requiredStr(pm, "verb", what+".permission", "one of "+strings.Join(verbNames, ", "))
		if r.verb != "" && !oneOf(r.verb, verbNames) {
			c.errorf(pm.get("verb"), "the verbs are "+strings.Join(verbNames, ", "),
				"%s.permission: unknown verb %q", what, r.verb)
		}
	}
	if !r.public && !r.hasPerm && len(r.authTypes) == 0 {
		c.errorf(m.node, "name the credential kinds and the permission, or say `public: true`",
			"%s: an auth block that grants nothing and denies nothing", what)
	}
	return r
}

func (c *collector) parseCLI(n *yaml.Node, what string) *cliRule {
	m := c.asMapping(n, what)
	if m == nil {
		return nil
	}
	c.only(m, what, "path", "args", "short", "long", "skip")

	r := &cliRule{}
	r.path, _ = c.strList(m, "path", what)
	r.args, _ = c.strList(m, "args", what)
	r.short = c.str(m, "short", what+".short")
	r.long = c.str(m, "long", what+".long")
	r.skip = c.boolean(m, "skip", what)
	if !r.skip && len(r.path) == 0 {
		c.errorf(m.node, "give it a command path -- `cli: {path: [catalog, providers]}` -- or `cli: {skip: true}`",
			"%s: no command path", what)
	}
	return r
}

// parseSidecar reads the universal fallback: annotations keyed by the full
// procedure string, in a file of their own.
func (c *collector) parseSidecar(doc *yaml.Node) map[string]*annotations {
	root := documentRoot(doc)
	m := c.asMapping(root, "sidecar")
	if m == nil {
		return nil
	}
	c.only(m, "sidecar", "procedures")
	pn := m.get("procedures")
	if pn == nil {
		c.errorf(m.node, "a sidecar is `procedures:` keyed by /pkg.Service/Method", "sidecar: no procedures")
		return nil
	}
	pm := c.asMapping(pn, "sidecar.procedures")
	if pm == nil {
		return nil
	}
	out := map[string]*annotations{}
	for _, proc := range pm.keys {
		p := pm.kv[proc]
		what := "procedures." + proc
		if !strings.HasPrefix(proc, "/") || strings.Count(proc, "/") != 2 {
			c.errorf(p.key, "write the full procedure string: /pkg.v1.Service/Method",
				"sidecar: %q is not a procedure", proc)
			continue
		}
		body := c.asMapping(p.val, what)
		if body == nil {
			continue
		}
		c.only(body, what, rpcAnnotationKeys...)
		a := c.parseAnnotations(body, what, "sidecar", rpcAnnotationKeys)
		a.procNode = p.key
		out[proc] = a
	}
	return out
}

// merge applies a sidecar entry on top of the inline annotations. An
// annotation set in both places is an error rather than a precedence rule:
// silent precedence is how a security posture gets overwritten by a file
// nobody was reading.
func (c *collector) merge(inline, side *annotations, proc string) *annotations {
	if side == nil {
		return inline
	}
	if inline == nil {
		return side
	}
	out := *inline
	conflict := func(node *yaml.Node, key string) {
		c.errorf(node, fmt.Sprintf("remove one of them -- keep `%s` inline, or in the sidecar, not both", key),
			"%s: %q is annotated both inline and in the sidecar", proc, key)
	}
	if len(side.transports) > 0 {
		if len(inline.transports) > 0 {
			conflict(side.transportsNode, "transports")
		}
		out.transports, out.transportsNode = side.transports, side.transportsNode
	}
	if side.group != "" {
		if inline.group != "" {
			conflict(side.procNode, "group")
		}
		out.group = side.group
	}
	if side.http != nil {
		if inline.http != nil {
			conflict(side.httpNode, "http")
		}
		out.http, out.httpNode = side.http, side.httpNode
	}
	if side.auth != nil {
		if inline.auth != nil {
			conflict(side.authNode, "auth")
		}
		out.auth, out.authNode = side.auth, side.authNode
	}
	if side.cli != nil {
		if inline.cli != nil {
			conflict(side.cliNode, "cli")
		}
		out.cli, out.cliNode = side.cli, side.cliNode
	}
	if side.internalSet {
		if inline.internalSet {
			conflict(side.internalNode, "internal")
		}
		out.internal, out.internalSet = side.internal, true
	}
	if side.idempotency != "" {
		if inline.idempotency != "" {
			conflict(side.idempotencyNode, "idempotency")
		}
		out.idempotency = side.idempotency
	}
	return &out
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
