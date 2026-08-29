package dsl

import (
	"fmt"
	"strconv"
	"strings"

	interchange "github.com/darmawan01/interchange"
	"gopkg.in/yaml.v3"
)

// Everything in this file walks yaml.Node rather than decoding into Go
// structs. Two reasons, both load-bearing:
//
//   - a Diagnostic without a line and column is barely better than no
//     diagnostic (§09), and only the node tree carries positions;
//   - decoding into map[string]any would put Go's randomised map iteration
//     between the source and the emitted proto, and the emitted proto is
//     committed under the drift gate.
type collector struct {
	path string
	list interchange.Diagnostics
}

func (c *collector) errorf(n *yaml.Node, hint, format string, args ...any) {
	d := interchange.Diagnostic{
		Severity: interchange.SeverityError,
		Path:     c.path,
		Message:  fmt.Sprintf(format, args...),
		Hint:     hint,
	}
	if n != nil {
		d.Line, d.Col = n.Line, n.Column
	}
	c.list = append(c.list, d)
}

func (c *collector) failed() bool { return c.list.HasErrors() }

// mapping is a YAML mapping with its keys in document order and every key
// still holding its position.
type mapping struct {
	node *yaml.Node
	keys []string
	kv   map[string]pair
}

type pair struct{ key, val *yaml.Node }

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// asMapping rejects anything that is not a mapping, and rejects duplicate
// keys: yaml.v3 keeps both, and silently preferring one is exactly the
// "contract that lies" failure mode.
func (c *collector) asMapping(n *yaml.Node, what string) *mapping {
	n = deref(n)
	if n == nil || n.Kind != yaml.MappingNode {
		c.errorf(n, "write it as `key: value` pairs", "%s must be a mapping", what)
		return nil
	}
	m := &mapping{node: n, kv: map[string]pair{}}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if _, dup := m.kv[k.Value]; dup {
			c.errorf(k, "remove one of the two declarations", "%s: duplicate key %q", what, k.Value)
			continue
		}
		m.kv[k.Value] = pair{k, v}
		m.keys = append(m.keys, k.Value)
	}
	return m
}

// only reports every key that is not in the allowed set. This is what turns a
// misspelled annotation into a build failure instead of a silently dropped
// security posture.
func (c *collector) only(m *mapping, what string, allowed ...string) {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	for _, k := range m.keys {
		if !set[k] {
			c.errorf(m.kv[k].key, "the keys %s accepts are: "+strings.Join(allowed, ", "),
				"%s: unknown key %q", what, k)
		}
	}
}

func (m *mapping) has(key string) bool { _, ok := m.kv[key]; return ok }

func (m *mapping) get(key string) *yaml.Node {
	if p, ok := m.kv[key]; ok {
		return deref(p.val)
	}
	return nil
}

func (m *mapping) keyNode(key string) *yaml.Node {
	if p, ok := m.kv[key]; ok {
		return p.key
	}
	return m.node
}

func (c *collector) scalar(n *yaml.Node, what string) (string, bool) {
	n = deref(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		c.errorf(n, "give it a single value", "%s must be a scalar", what)
		return "", false
	}
	return n.Value, true
}

func (c *collector) str(m *mapping, key, what string) string {
	n := m.get(key)
	if n == nil {
		return ""
	}
	s, ok := c.scalar(n, what)
	if !ok {
		return ""
	}
	return s
}

func (c *collector) requiredStr(m *mapping, key, what, hint string) string {
	if !m.has(key) {
		c.errorf(m.node, hint, "%s: missing required key %q", what, key)
		return ""
	}
	s := c.str(m, key, what+"."+key)
	if s == "" {
		c.errorf(m.get(key), hint, "%s: %q must not be empty", what, key)
	}
	return s
}

func (c *collector) boolean(m *mapping, key, what string) bool {
	n := m.get(key)
	if n == nil {
		return false
	}
	s, ok := c.scalar(n, what)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		c.errorf(n, "use true or false", "%s: %q is not a boolean", what, s)
		return false
	}
	return b
}

func (c *collector) intAt(n *yaml.Node, what string) (int, bool) {
	s, ok := c.scalar(n, what)
	if !ok {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		c.errorf(n, "use a whole number", "%s: %q is not an integer", what, s)
		return 0, false
	}
	return v, true
}

// strList accepts a YAML sequence of scalars, and keeps the nodes so an
// invalid element can be reported at its own position.
func (c *collector) strList(m *mapping, key, what string) ([]string, []*yaml.Node) {
	n := m.get(key)
	if n == nil {
		return nil, nil
	}
	n = deref(n)
	if n.Kind != yaml.SequenceNode {
		c.errorf(n, "write it as a list: ["+key+"]", "%s.%s must be a list", what, key)
		return nil, nil
	}
	var out []string
	var nodes []*yaml.Node
	for _, e := range n.Content {
		s, ok := c.scalar(e, what+"."+key)
		if !ok {
			continue
		}
		out = append(out, s)
		nodes = append(nodes, deref(e))
	}
	return out, nodes
}

// doc pulls the comment attached to a node so a DSL comment survives into the
// emitted .proto, which is the artifact humans review.
func docOf(key *yaml.Node) string {
	if key == nil {
		return ""
	}
	raw := key.HeadComment
	if raw == "" {
		raw = key.LineComment
	}
	if raw == "" {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#")))
	}
	return strings.Join(lines, "\n")
}
