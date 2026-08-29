package openapi

import (
	"strconv"
	"strings"

	"github.com/darmawan01/interchange"
	yaml "go.yaml.in/yaml/v4"
)

// Positions come from the raw YAML tree, addressed by JSON pointer, rather
// than from the object model's low-level nodes. One source of truth for every
// location, and the pointer that finds it is also the string a diagnostic
// names the construct by ("components/schemas/Payment: ..."), so the two can
// never disagree.
//
// JSON is parsed by the same code path: YAML 1.2 is a superset of JSON, so a
// .json document yields the same tree with the same positions.
type source struct {
	path string     // the file name, for diagnostics
	root *yaml.Node // the document's mapping node
}

func newSource(path string, content []byte) (*source, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(content, &n); err != nil {
		return nil, err
	}
	root := &n
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	return &source{path: path, root: root}, nil
}

// ptr appends a segment to a JSON pointer, escaping per RFC 6901 so a path
// key like /v1/payments/{id} does not split the pointer.
func ptr(base string, seg string) string {
	seg = strings.ReplaceAll(seg, "~", "~0")
	seg = strings.ReplaceAll(seg, "/", "~1")
	return base + "/" + seg
}

func unescapePtr(seg string) string {
	seg = strings.ReplaceAll(seg, "~1", "/")
	return strings.ReplaceAll(seg, "~0", "~")
}

// find resolves a JSON pointer to the node that names it: the key node for a
// mapping entry, the value node for a sequence element. A pointer that runs
// past the end of the tree resolves to the deepest node it did reach, so a
// diagnostic still points somewhere useful instead of at line 0.
func (s *source) find(pointer string) (*yaml.Node, bool) {
	cur := s.root
	best := cur
	if pointer == "" || pointer == "/" {
		return cur, true
	}
	for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		seg := unescapePtr(raw)
		next, key := child(cur, seg)
		if next == nil {
			return best, false
		}
		if key != nil {
			best = key
		} else {
			best = next
		}
		cur = next
	}
	return best, true
}

// child descends one pointer segment, returning the value node and, for a
// mapping, the key node that named it.
func child(n *yaml.Node, seg string) (val, key *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				return n.Content[i+1], n.Content[i]
			}
		}
	case yaml.SequenceNode:
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(n.Content) {
			return nil, nil
		}
		return n.Content[i], nil
	}
	return nil, nil
}

// at builds a diagnostic located at a JSON pointer.
func (s *source) at(sev interchange.Severity, pointer, msg, hint string) interchange.Diagnostic {
	d := interchange.Diagnostic{
		Severity: sev,
		Path:     s.path,
		Message:  where(pointer) + ": " + msg,
		Hint:     hint,
	}
	if n, _ := s.find(pointer); n != nil {
		d.Line, d.Col = n.Line, n.Column
	}
	return d
}

// where renders a JSON pointer the way the docs do: components/schemas/Payment,
// paths./payments.post. The path form keeps a URL template readable, which a
// slash-separated pointer does not.
func where(pointer string) string {
	segs := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, s := range segs {
		segs[i] = unescapePtr(s)
	}
	if len(segs) >= 3 && segs[0] == "paths" {
		return "paths." + segs[1] + "." + strings.Join(segs[2:], ".")
	}
	return strings.Join(segs, "/")
}

// refs collects every $ref in the document with the pointer that reaches it,
// in document order.
type refSite struct {
	pointer string // pointer to the $ref key itself
	target  string // its value
}

func (s *source) refs() []refSite {
	var out []refSite
	var walk func(n *yaml.Node, at string)
	walk = func(n *yaml.Node, at string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				p := ptr(at, k.Value)
				if k.Value == "$ref" && v.Kind == yaml.ScalarNode {
					out = append(out, refSite{pointer: p, target: v.Value})
					continue
				}
				walk(v, p)
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, ptr(at, strconv.Itoa(i)))
			}
		}
	}
	walk(s.root, "")
	return out
}

// resolves reports whether an internal $ref points at a node that exists.
func (s *source) resolves(target string) bool {
	_, ok := s.find(strings.TrimPrefix(target, "#"))
	return ok
}

// scalar reads a scalar at a pointer.
func (s *source) scalar(pointer string) (string, bool) {
	n, ok := s.value(pointer)
	if !ok || n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	return n.Value, true
}

// keys lists a mapping's keys at a pointer, in document order.
func (s *source) keys(pointer string) []string {
	n, ok := s.value(pointer)
	if !ok || n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}

// value resolves a pointer to its value node.
func (s *source) value(pointer string) (*yaml.Node, bool) {
	cur := s.root
	if pointer == "" || pointer == "/" {
		return cur, true
	}
	for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		next, _ := child(cur, unescapePtr(raw))
		if next == nil {
			return nil, false
		}
		cur = next
	}
	return cur, true
}
