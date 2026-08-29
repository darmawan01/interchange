package openapi

import (
	"fmt"
	"strings"

	"github.com/darmawan01/interchange"
	yaml "go.yaml.in/yaml/v4"
)

// Annotations arrive two ways -- x-interchange-* vendor extensions in the
// document, and a sidecar keyed by procedure (§09 rule 3). Both decode through
// this file, so the two paths cannot drift apart in what they accept.
//
// Precedence: a vendor extension wins. The sidecar is the fallback for a
// document you cannot edit, so the annotation nearest the operation is the one
// a reviewer reads.

type transportOptions struct {
	On    []string
	Group string
}

type permission struct {
	Resource string
	Verb     string
}

type authOptions struct {
	AuthTypes  []string
	Permission *permission
	Public     bool
	Platform   bool
}

type cliOptions struct {
	Path  []string
	Args  []string
	Short string
	Long  string
	Skip  bool
}

// Vendor extension keys. Anything else beginning x-interchange- is rejected:
// a misspelled annotation that is silently ignored is an authorization check
// that never fires.
const (
	extTransports        = "x-interchange-transports"
	extServiceTransports = "x-interchange-service-transports"
	extAuth              = "x-interchange-auth"
	extCLI               = "x-interchange-cli"
	extInternal          = "x-interchange-internal"
	extOneof             = "x-interchange-oneof"
	extName              = "x-interchange-name"
	extService           = "x-interchange-service"
	extPackage           = "x-interchange-package"
	extField             = "x-interchange-field"
	extNullable          = "x-interchange-nullable"
	extSkip              = "x-interchange-skip"
)

var knownExtensions = map[string]bool{
	extTransports: true, extServiceTransports: true, extAuth: true, extCLI: true,
	extInternal: true, extOneof: true, extName: true, extService: true,
	extPackage: true, extField: true, extNullable: true, extSkip: true,
}

var (
	transportNames = []string{"TRANSPORT_RPC", "TRANSPORT_REST", "TRANSPORT_BUS", "TRANSPORT_MQTT", "TRANSPORT_WS"}
	authTypeNames  = []string{"AUTH_TYPE_API_KEY", "AUTH_TYPE_SESSION", "AUTH_TYPE_WORKLOAD"}
	verbNames      = []string{"VERB_READ", "VERB_CREATE", "VERB_EDIT", "VERB_DELETE"}
)

// enumValue accepts either the full proto name or the bare suffix, so a
// document can say REST rather than TRANSPORT_REST.
func enumValue(prefix string, allowed []string, raw string) (string, bool) {
	v := screaming(raw)
	if !strings.HasPrefix(v, prefix) {
		v = prefix + v
	}
	for _, a := range allowed {
		if a == v {
			return v, true
		}
	}
	return "", false
}

// node is a yaml node paired with the file it came from, so a diagnostic about
// a sidecar entry points into the sidecar and one about a vendor extension
// points into the document.
type node struct {
	file string
	n    *yaml.Node
}

func (n node) diag(msg, hint string) interchange.Diagnostic {
	d := interchange.Diagnostic{Severity: interchange.SeverityError, Path: n.file, Message: msg, Hint: hint}
	if n.n != nil {
		d.Line, d.Col = n.n.Line, n.n.Column
	}
	return d
}

func (n node) entry(key string) (node, bool) {
	if n.n == nil || n.n.Kind != yaml.MappingNode {
		return node{}, false
	}
	for i := 0; i+1 < len(n.n.Content); i += 2 {
		if n.n.Content[i].Value == key {
			return node{file: n.file, n: n.n.Content[i+1]}, true
		}
	}
	return node{}, false
}

func (n node) seq() []node {
	if n.n == nil || n.n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]node, 0, len(n.n.Content))
	for _, c := range n.n.Content {
		out = append(out, node{file: n.file, n: c})
	}
	return out
}

func (n node) str() string {
	if n.n == nil || n.n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.n.Value
}

func (n node) boolean() bool { return n.str() == "true" }

func (n node) strings() []string {
	var out []string
	for _, c := range n.seq() {
		out = append(out, c.str())
	}
	return out
}

// decodeTransports accepts both the shorthand list form and the full form:
//
//	x-interchange-transports: [RPC, REST]
//	x-interchange-transports: {on: [RPC, BUS], group: payments}
func decodeTransports(n node) (*transportOptions, interchange.Diagnostics) {
	var diags interchange.Diagnostics
	out := &transportOptions{}
	list := n
	if n.n != nil && n.n.Kind == yaml.MappingNode {
		on, ok := n.entry("on")
		if !ok {
			return nil, interchange.Diagnostics{n.diag("transports: no 'on' list", "transports: {on: [RPC, REST]}")}
		}
		list = on
		if g, ok := n.entry("group"); ok {
			out.Group = g.str()
		}
	}
	for _, item := range list.seq() {
		v, ok := enumValue("TRANSPORT_", transportNames, item.str())
		if !ok {
			diags = append(diags, item.diag(
				fmt.Sprintf("transports: %q is not a transport", item.str()),
				"one of RPC, REST, BUS, MQTT, WS"))
			continue
		}
		out.On = append(out.On, v)
	}
	if len(out.On) == 0 && len(diags) == 0 {
		diags = append(diags, n.diag("transports: empty", "name at least one of RPC, REST, BUS, MQTT, WS"))
	}
	if len(diags) > 0 {
		return nil, diags
	}
	out.On = dedupe(out.On)
	return out, nil
}

func decodeAuth(n node) (*authOptions, interchange.Diagnostics) {
	var diags interchange.Diagnostics
	out := &authOptions{}
	if types, ok := n.entry("auth_types"); ok {
		for _, item := range types.seq() {
			v, ok := enumValue("AUTH_TYPE_", authTypeNames, item.str())
			if !ok {
				diags = append(diags, item.diag(
					fmt.Sprintf("auth: %q is not an auth type", item.str()),
					"one of SESSION, API_KEY, WORKLOAD"))
				continue
			}
			out.AuthTypes = append(out.AuthTypes, v)
		}
	}
	if p, ok := n.entry("permission"); ok {
		res, _ := p.entry("resource")
		vb, _ := p.entry("verb")
		verb, ok := enumValue("VERB_", verbNames, vb.str())
		if !ok {
			diags = append(diags, p.diag(
				fmt.Sprintf("auth: %q is not a verb", vb.str()),
				"one of READ, CREATE, EDIT, DELETE"))
		}
		if res.str() == "" {
			diags = append(diags, p.diag("auth: permission has no resource", "permission: {resource: payments, verb: READ}"))
		}
		out.Permission = &permission{Resource: res.str(), Verb: verb}
	}
	if p, ok := n.entry("public"); ok {
		out.Public = p.boolean()
	}
	if p, ok := n.entry("platform"); ok {
		out.Platform = p.boolean()
	}
	switch {
	case out.Public && len(out.AuthTypes) > 0:
		diags = append(diags, n.diag("auth: public and auth_types are mutually exclusive",
			"drop auth_types for a public RPC, or drop public"))
	case !out.Public && len(out.AuthTypes) == 0:
		diags = append(diags, n.diag("auth: no auth_types and not public",
			"auth_types: [SESSION], or public: true to say so explicitly"))
	case !out.Public && out.Permission == nil:
		diags = append(diags, n.diag("auth: no permission",
			"permission: {resource: payments, verb: READ}"))
	}
	if len(diags) > 0 {
		return nil, diags
	}
	out.AuthTypes = dedupe(out.AuthTypes)
	return out, nil
}

func decodeCLI(n node) (*cliOptions, interchange.Diagnostics) {
	out := &cliOptions{}
	if p, ok := n.entry("path"); ok {
		out.Path = p.strings()
	}
	if p, ok := n.entry("args"); ok {
		out.Args = p.strings()
	}
	if p, ok := n.entry("short"); ok {
		out.Short = p.str()
	}
	if p, ok := n.entry("long"); ok {
		out.Long = p.str()
	}
	if p, ok := n.entry("skip"); ok {
		out.Skip = p.boolean()
	}
	if len(out.Path) == 0 && !out.Skip {
		return nil, interchange.Diagnostics{n.diag("cli: no command path and not skipped",
			"cli: {path: [payments, list]}, or cli: {skip: true}")}
	}
	return out, nil
}

// sidecar is the universal annotation fallback: a YAML file keyed by full
// procedure string.
type sidecar struct {
	file    string
	entries map[string]node
	keys    map[string]*yaml.Node
	order   []string
	used    map[string]bool
}

// dedupe keeps the first occurrence of each value, so the emitted option
// preserves the order the author wrote rather than an arbitrary one.
func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0]
	for _, s := range ss {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func parseSidecar(file string, content []byte) (*sidecar, interchange.Diagnostics) {
	sc := &sidecar{file: file, entries: map[string]node{}, keys: map[string]*yaml.Node{}, used: map[string]bool{}}
	if len(content) == 0 {
		return sc, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, interchange.Diagnostics{{
			Severity: interchange.SeverityError, Path: file,
			Message: "sidecar is not valid YAML: " + err.Error(),
			Hint:    "see README.md for the sidecar format",
		}}
	}
	top := &root
	if top.Kind == yaml.DocumentNode && len(top.Content) > 0 {
		top = top.Content[0]
	}
	n := node{file: file, n: top}
	procs, ok := n.entry("procedures")
	if !ok {
		return nil, interchange.Diagnostics{n.diag("sidecar has no 'procedures' key",
			"procedures:\n      /pkg.v1.Service/Method:\n        transports: [RPC, REST]")}
	}
	if procs.n.Kind != yaml.MappingNode {
		return nil, interchange.Diagnostics{procs.diag("sidecar 'procedures' is not a mapping",
			"keys are full procedure strings: /pkg.v1.Service/Method")}
	}
	for i := 0; i+1 < len(procs.n.Content); i += 2 {
		k, v := procs.n.Content[i], procs.n.Content[i+1]
		sc.entries[k.Value] = node{file: file, n: v}
		sc.keys[k.Value] = k
		sc.order = append(sc.order, k.Value)
	}
	return sc, nil
}

func (s *sidecar) lookup(procedure string) (node, bool) {
	if s == nil {
		return node{}, false
	}
	n, ok := s.entries[procedure]
	if ok {
		s.used[procedure] = true
	}
	return n, ok
}

// unused reports sidecar keys that matched no procedure. A sidecar entry that
// silently applies to nothing is an annotation the author believes is in
// force, which is the failure mode this project exists to remove.
func (s *sidecar) unused() interchange.Diagnostics {
	if s == nil {
		return nil
	}
	var out interchange.Diagnostics
	for _, k := range s.order {
		if s.used[k] {
			continue
		}
		kn := s.keys[k]
		out = append(out, interchange.Diagnostic{
			Severity: interchange.SeverityError, Path: s.file, Line: kn.Line, Col: kn.Column,
			Message: fmt.Sprintf("sidecar: %s matches no procedure in this document", k),
			Hint:    "procedure strings are /<package>.<Service>/<Method>; check the package and the derived RPC name",
		})
	}
	return out
}
