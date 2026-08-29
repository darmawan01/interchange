package openapi

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// document and the types below are the emitted shape. They are structs rather
// than a generic map so that field order is fixed by the source, and the maps
// they do carry are keyed by strings -- which encoding/json emits in sorted
// order. Between the two, the same input produces the same bytes.
type document struct {
	OpenAPI    string               `json:"openapi"`
	Info       info                 `json:"info"`
	Servers    []server             `json:"servers,omitempty"`
	Paths      map[string]*pathItem `json:"paths"`
	Components components           `json:"components"`
}

type info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type server struct {
	URL string `json:"url"`
}

type pathItem struct {
	Get    *operation `json:"get,omitempty"`
	Put    *operation `json:"put,omitempty"`
	Post   *operation `json:"post,omitempty"`
	Delete *operation `json:"delete,omitempty"`
	Patch  *operation `json:"patch,omitempty"`
}

func (p *pathItem) slot(verb string) **operation {
	switch verb {
	case "get":
		return &p.Get
	case "put":
		return &p.Put
	case "post":
		return &p.Post
	case "delete":
		return &p.Delete
	case "patch":
		return &p.Patch
	}
	return nil
}

type operation struct {
	OperationID string              `json:"operationId"`
	Tags        []string            `json:"tags,omitempty"`
	Description string              `json:"description,omitempty"`
	Parameters  []parameter         `json:"parameters,omitempty"`
	RequestBody *requestBody        `json:"requestBody,omitempty"`
	Responses   map[string]response `json:"responses"`
}

type parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Required    bool    `json:"required,omitempty"`
	Description string  `json:"description,omitempty"`
	Schema      *schema `json:"schema"`
}

type requestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]mediaType `json:"content"`
}

type response struct {
	Description string               `json:"description"`
	Content     map[string]mediaType `json:"content,omitempty"`
}

type mediaType struct {
	Schema *schema `json:"schema"`
}

type components struct {
	Schemas map[string]*schema `json:"schemas"`
}

type schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Items                *schema            `json:"items,omitempty"`
	Properties           map[string]*schema `json:"properties,omitempty"`
	AdditionalProperties *schema            `json:"additionalProperties,omitempty"`
}

func componentRef(name string) string { return "#/components/schemas/" + name }

// ref registers a message in components and returns a reference to it.
// Registering the placeholder before walking the fields is what lets a
// message refer to itself.
func (e *emitter) ref(md protoreflect.MessageDescriptor) *schema {
	name := string(md.FullName())
	if _, done := e.schemas[name]; !done {
		s := &schema{Type: "object", Description: comment(md), Properties: map[string]*schema{}}
		e.schemas[name] = s
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			// The REST surface is snake_case (§08), so a property is named
			// by its proto name and not by its JSON name.
			s.Properties[string(fd.Name())] = e.fieldSchema(fd, true)
		}
	}
	return &schema{Ref: componentRef(name)}
}

// fieldSchema maps one field. cardinality is false where the caller has
// already decided the field is a single value -- a path variable is never the
// whole repeated field.
func (e *emitter) fieldSchema(fd protoreflect.FieldDescriptor, cardinality bool) *schema {
	if cardinality && fd.IsMap() {
		return &schema{
			Type:                 "object",
			Description:          comment(fd),
			AdditionalProperties: e.valueSchema(fd.MapValue()),
		}
	}
	if cardinality && fd.IsList() {
		return &schema{Type: "array", Description: comment(fd), Items: e.valueSchema(fd)}
	}
	s := e.valueSchema(fd)
	if s.Ref == "" {
		s.Description = comment(fd)
	}
	return s
}

// valueSchema maps the element type of a field, ignoring its cardinality.
func (e *emitter) valueSchema(fd protoreflect.FieldDescriptor) *schema {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return &schema{Type: "boolean"}
	case protoreflect.StringKind:
		return &schema{Type: "string"}
	case protoreflect.BytesKind:
		// protojson base64s bytes.
		return &schema{Type: "string", Format: "byte"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return &schema{Type: "integer", Format: "int32"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return &schema{Type: "integer", Format: "uint32"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		// A 64-bit integer does not survive a JSON number, so protojson
		// writes it as a string. Documenting it as an integer would have
		// every generated client reject a valid response.
		return &schema{Type: "string", Format: "int64"}
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return &schema{Type: "string", Format: "uint64"}
	case protoreflect.FloatKind:
		return &schema{Type: "number", Format: "float"}
	case protoreflect.DoubleKind:
		return &schema{Type: "number", Format: "double"}
	case protoreflect.EnumKind:
		return enumSchema(fd.Enum())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if s := wellKnown(fd.Message()); s != nil {
			return s
		}
		return e.ref(fd.Message())
	}
	return &schema{}
}

// enumSchema emits the value names, because that is what protojson writes.
func enumSchema(ed protoreflect.EnumDescriptor) *schema {
	values := ed.Values()
	out := &schema{Type: "string", Description: comment(ed)}
	for i := 0; i < values.Len(); i++ {
		out.Enum = append(out.Enum, string(values.Get(i).Name()))
	}
	return out
}

// wellKnown is the handful of messages protojson does not encode as objects.
// Emitting a $ref for one of these would document a shape no client will ever
// see on the wire.
func wellKnown(md protoreflect.MessageDescriptor) *schema {
	name := string(md.FullName())
	if !strings.HasPrefix(name, "google.protobuf.") {
		return nil
	}
	switch name {
	case "google.protobuf.Timestamp":
		return &schema{Type: "string", Format: "date-time"}
	case "google.protobuf.Duration":
		return &schema{Type: "string", Format: "duration"}
	case "google.protobuf.FieldMask":
		return &schema{Type: "string", Format: "field-mask"}
	case "google.protobuf.BoolValue":
		return &schema{Type: "boolean"}
	case "google.protobuf.StringValue":
		return &schema{Type: "string"}
	case "google.protobuf.BytesValue":
		return &schema{Type: "string", Format: "byte"}
	case "google.protobuf.Int32Value":
		return &schema{Type: "integer", Format: "int32"}
	case "google.protobuf.UInt32Value":
		return &schema{Type: "integer", Format: "uint32"}
	case "google.protobuf.Int64Value":
		return &schema{Type: "string", Format: "int64"}
	case "google.protobuf.UInt64Value":
		return &schema{Type: "string", Format: "uint64"}
	case "google.protobuf.FloatValue":
		return &schema{Type: "number", Format: "float"}
	case "google.protobuf.DoubleValue":
		return &schema{Type: "number", Format: "double"}
	case "google.protobuf.Value", "google.protobuf.Any", "google.protobuf.Struct", "google.protobuf.Empty":
		return &schema{Type: "object"}
	case "google.protobuf.ListValue":
		return &schema{Type: "array", Items: &schema{Type: "object"}}
	}
	return nil
}
