// Package openapi emits an OpenAPI 3.1 document from the descriptors the REST
// binding routes on.
//
// It is the same source of truth read a second way, not a second contract: a
// path exists in the document because an RPC carries a google.api.http rule
// and declares the REST road, and it is absent for exactly the reasons the
// binding refuses to mount it. A method the binding will not serve must not
// appear here, and an `internal` one appearing here is a leak.
//
// The output is a committed artifact under the drift gate, so it is
// deterministic by construction: paths, properties and every other map are
// emitted in sorted order, and nothing in it depends on the clock, the
// filesystem or the order someone typed an annotation.
package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/darmawan01/interchange/binding/rest/internal/protoann"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Options configures emission.
type Options struct {
	// Title and Version fill in the document's info block. Both are
	// required: an OpenAPI document without them is not one.
	Title   string
	Version string

	// Description is the info block's prose.
	Description string

	// Servers are the base URLs partners call.
	Servers []string

	// Expose is the road a method must declare to appear in the document.
	// The zero value means TRANSPORT_REST.
	Expose transportv1.Transport

	// Files restricts emission to these file paths. Empty means every file
	// that declares a service.
	Files []string
}

// FromFileDescriptorSet emits the document for every service in a compiled
// descriptor set -- the shape a codegen step has on hand.
func FromFileDescriptorSet(fds *descriptorpb.FileDescriptorSet, opts Options) ([]byte, error) {
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	var descs []protoreflect.FileDescriptor
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		descs = append(descs, fd)
		return true
	})
	// RangeFiles is unordered, and this artifact is compared byte for byte.
	slices.SortFunc(descs, func(a, b protoreflect.FileDescriptor) int {
		return strings.Compare(a.Path(), b.Path())
	})
	return FromFiles(descs, opts)
}

// FromFiles emits the document for the services declared in files.
func FromFiles(files []protoreflect.FileDescriptor, opts Options) ([]byte, error) {
	if opts.Title == "" || opts.Version == "" {
		return nil, fmt.Errorf("openapi: Title and Version are required")
	}
	if opts.Expose == transportv1.Transport_TRANSPORT_UNSPECIFIED {
		opts.Expose = transportv1.Transport_TRANSPORT_REST
	}

	e := &emitter{
		opts:    opts,
		paths:   map[string]*pathItem{},
		schemas: map[string]*schema{},
	}
	for _, fd := range files {
		if len(opts.Files) > 0 && !slices.Contains(opts.Files, fd.Path()) {
			continue
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			if err := e.service(services.Get(i)); err != nil {
				return nil, err
			}
		}
	}

	doc := &document{
		OpenAPI: "3.1.0",
		Info: info{
			Title:       opts.Title,
			Version:     opts.Version,
			Description: opts.Description,
		},
		Paths:      e.paths,
		Components: components{Schemas: e.schemas},
	}
	for _, url := range opts.Servers {
		doc.Servers = append(doc.Servers, server{URL: url})
	}
	// Every operation can fail, and on this surface a failure is RFC 9457.
	// The schema is in components whether or not a path referenced it, so
	// the error shape is documented once rather than per operation.
	e.ref(commonv1.File_interchange_common_v1_types_proto.Messages().ByName("Problem"))

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// A URI in a path template is not HTML, and escaping it makes the
	// document unreadable for the audience it exists for.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	return buf.Bytes(), nil
}

type emitter struct {
	opts    Options
	paths   map[string]*pathItem
	schemas map[string]*schema
}

func (e *emitter) service(sd protoreflect.ServiceDescriptor) error {
	methods := sd.Methods()
	for i := 0; i < methods.Len(); i++ {
		md := methods.Get(i)
		if protoann.Internal(md) {
			continue
		}
		on, err := protoann.ExposedOn(md, e.opts.Expose)
		if err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		if !on {
			continue
		}
		rule, ok := protoann.HTTPRule(md)
		if !ok {
			// The binding refuses to mount this for the same reason. A
			// document that quietly omits it would disagree with the surface
			// it describes.
			return fmt.Errorf("openapi: %s declares %s but carries no google.api.http rule",
				md.FullName(), e.opts.Expose)
		}
		if err := e.method(md, rule, 0); err != nil {
			return err
		}
		for j, extra := range rule.GetAdditionalBindings() {
			if len(extra.GetAdditionalBindings()) > 0 {
				return fmt.Errorf("openapi: %s: nested additional_bindings are not representable",
					md.FullName())
			}
			if err := e.method(md, extra, j+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// method turns one HTTP rule into one operation. n distinguishes the
// additional bindings of a method, which are separate operations on separate
// paths and therefore need distinct operation ids.
func (e *emitter) method(md protoreflect.MethodDescriptor, rule *annotations.HttpRule, n int) error {
	if md.IsStreamingClient() || md.IsStreamingServer() {
		return fmt.Errorf("openapi: %s is streaming, which this surface does not carry", md.FullName())
	}
	verb, template, err := ruleTarget(rule)
	if err != nil {
		return fmt.Errorf("openapi: %s: %w", md.FullName(), err)
	}
	path, vars, err := parseTemplate(template, md.Input())
	if err != nil {
		return fmt.Errorf("openapi: %s: %w", md.FullName(), err)
	}

	op := &operation{
		OperationID: operationID(md, n),
		Tags:        []string{string(md.Parent().Name())},
		Description: comment(md),
		Responses: map[string]response{
			"200": {
				Description: "OK",
				Content:     jsonContent(e.ref(md.Output())),
			},
			"default": {
				Description: "An error, as RFC 9457 problem detail.",
				Content: map[string]mediaType{
					problemContentType: {Schema: &schema{Ref: componentRef(problemSchemaName)}},
				},
			},
		},
	}

	bound := map[string]bool{}
	for _, v := range vars {
		bound[v.path] = true
		op.Parameters = append(op.Parameters, parameter{
			Name:        v.path,
			In:          "path",
			Required:    true,
			Description: comment(v.field),
			Schema:      e.fieldSchema(v.field, false),
		})
	}

	body := rule.GetBody()
	switch body {
	case "":
		// Everything not in the path arrives in the query string.
		op.Parameters = append(op.Parameters, e.queryParameters(md.Input(), bound, nil)...)
	case "*":
		op.RequestBody = &requestBody{
			Required: true,
			Content:  jsonContent(e.bodySchema(md.Input(), bound)),
		}
	default:
		fd := md.Input().Fields().ByName(protoreflect.Name(body))
		if fd == nil {
			return fmt.Errorf("openapi: %s: body %q is not a field of %s", md.FullName(), body, md.Input().FullName())
		}
		bound[body] = true
		op.RequestBody = &requestBody{Required: true, Content: jsonContent(e.fieldSchema(fd, true))}
		op.Parameters = append(op.Parameters, e.queryParameters(md.Input(), bound, nil)...)
	}
	slices.SortFunc(op.Parameters, func(a, b parameter) int {
		if a.In != b.In {
			return strings.Compare(a.In, b.In)
		}
		return strings.Compare(a.Name, b.Name)
	})

	item := e.paths[path]
	if item == nil {
		item = &pathItem{}
		e.paths[path] = item
	}
	slot := item.slot(verb)
	if slot == nil {
		return fmt.Errorf("openapi: %s: %s is not a method this document can carry", md.FullName(), verb)
	}
	if *slot != nil {
		return fmt.Errorf("openapi: %s %s is claimed by both %s and %s", verb, path, (*slot).OperationID, op.OperationID)
	}
	*slot = op
	return nil
}

// queryParameters walks the request message for everything the URI did not
// bind. A message-valued field becomes dotted parameters, which is how
// google.api.http addresses a nested field.
func (e *emitter) queryParameters(md protoreflect.MessageDescriptor, bound map[string]bool, prefix []string) []parameter {
	var out []parameter
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := strings.Join(append(append([]string{}, prefix...), string(fd.Name())), ".")
		if bound[name] {
			continue
		}
		if fd.IsMap() {
			// A map has no query-string spelling in the transcoder.
			continue
		}
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && wellKnown(fd.Message()) == nil {
			if len(prefix) >= maxQueryDepth {
				continue
			}
			out = append(out, e.queryParameters(fd.Message(), bound, append(append([]string{}, prefix...), string(fd.Name())))...)
			continue
		}
		out = append(out, parameter{
			Name:        name,
			In:          "query",
			Description: comment(fd),
			Schema:      e.fieldSchema(fd, true),
		})
	}
	return out
}

// maxQueryDepth stops a recursive message from producing an infinite
// parameter list. Anything deeper belongs in a body.
const maxQueryDepth = 3

// bodySchema is the request message as a body. When the URI already bound
// some of its fields, the body is the rest of it -- repeating a path variable
// in the body would document a value a partner cannot usefully send.
func (e *emitter) bodySchema(md protoreflect.MessageDescriptor, bound map[string]bool) *schema {
	if len(bound) == 0 {
		return e.ref(md)
	}
	out := &schema{Type: "object", Properties: map[string]*schema{}}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if bound[string(fd.Name())] {
			continue
		}
		out.Properties[string(fd.Name())] = e.fieldSchema(fd, true)
	}
	return out
}

func jsonContent(s *schema) map[string]mediaType {
	return map[string]mediaType{"application/json": {Schema: s}}
}

const (
	problemContentType = "application/problem+json"
	problemSchemaName  = "interchange.common.v1.Problem"
)

// operationID is stable across runs because it is derived from the contract,
// not from iteration order.
func operationID(md protoreflect.MethodDescriptor, n int) string {
	id := string(md.Parent().Name()) + "_" + string(md.Name())
	if n > 0 {
		id = fmt.Sprintf("%s_%d", id, n+1)
	}
	return id
}

func ruleTarget(rule *annotations.HttpRule) (verb, template string, err error) {
	switch p := rule.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return "get", p.Get, nil
	case *annotations.HttpRule_Put:
		return "put", p.Put, nil
	case *annotations.HttpRule_Post:
		return "post", p.Post, nil
	case *annotations.HttpRule_Delete:
		return "delete", p.Delete, nil
	case *annotations.HttpRule_Patch:
		return "patch", p.Patch, nil
	case *annotations.HttpRule_Custom:
		return strings.ToLower(p.Custom.GetKind()), p.Custom.GetPath(), nil
	default:
		return "", "", fmt.Errorf("the http rule names no method")
	}
}

type pathVar struct {
	path  string
	field protoreflect.FieldDescriptor
}

// parseTemplate turns a google.api.http path template into an OpenAPI path
// and the fields its variables bind to. `{name=providers/*}` keeps only the
// variable: the pattern constrains what the segment may contain, and OpenAPI
// has nowhere to say so.
func parseTemplate(template string, md protoreflect.MessageDescriptor) (string, []pathVar, error) {
	var out strings.Builder
	var vars []pathVar
	for i := 0; i < len(template); {
		c := template[i]
		if c != '{' {
			out.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(template[i:], '}')
		if end < 0 {
			return "", nil, fmt.Errorf("unbalanced %q in path template %q", "{", template)
		}
		spec := template[i+1 : i+end]
		i += end + 1
		name := spec
		if eq := strings.IndexByte(spec, '='); eq >= 0 {
			name = spec[:eq]
		}
		fd, err := resolveField(md, name)
		if err != nil {
			return "", nil, err
		}
		vars = append(vars, pathVar{path: name, field: fd})
		out.WriteString("{" + name + "}")
	}
	slices.SortFunc(vars, func(a, b pathVar) int { return strings.Compare(a.path, b.path) })
	return out.String(), vars, nil
}

func resolveField(md protoreflect.MessageDescriptor, path string) (protoreflect.FieldDescriptor, error) {
	parts := strings.Split(path, ".")
	cur := md
	for i, part := range parts {
		fd := cur.Fields().ByName(protoreflect.Name(part))
		if fd == nil {
			return nil, fmt.Errorf("path variable %q names %q, which is not a field of %s", path, part, cur.FullName())
		}
		if i == len(parts)-1 {
			return fd, nil
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil, fmt.Errorf("path variable %q walks into %q, which is not a message", path, part)
		}
		cur = fd.Message()
	}
	return nil, fmt.Errorf("empty path variable")
}

// comment is the leading comment on a descriptor, which is the only
// documentation a partner-facing document can have that does not drift: it is
// the comment in the contract.
func comment(d protoreflect.Descriptor) string {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	text := loc.LeadingComments
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
