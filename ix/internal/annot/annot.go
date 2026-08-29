// Package annot reads the interchange annotations off a descriptor.
//
// Every lookup is by extension NUMBER (the band table in
// docs/annotation-band.md is what fixes those) and then by FIELD NAME on the
// resulting message. Nothing here imports a generated annotation type -- not
// core's transports, and emphatically not the optional auth module's, which
// ix must be able to describe without depending on.
package annot

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The band. See docs/annotation-band.md; these numbers never change.
const (
	NumAuth              = 50001 // MethodOptions,  interchange.auth.v1.auth      (optional module)
	NumTransports        = 50002 // MethodOptions,  interchange.transport.v1.transports
	NumServiceTransports = 50002 // ServiceOptions, interchange.transport.v1.service_transports
	NumInternal          = 50003 // MethodOptions,  interchange.transport.v1.internal
	NumCommand           = 50004 // MethodOptions,  interchange.cli.v1.command
	NumTenantIDField     = 50007 // FieldOptions,   interchange.auth.v1.tenant_id_field
	NumProjectIDField    = 50008 // FieldOptions,   interchange.auth.v1.project_id_field

	// NumHTTP is google.api.http, which is not in the band -- it is
	// upstream's, and ix only reads it.
	NumHTTP = 72295728
)

// DefaultTransports is the fan-out an RPC takes when neither it nor its
// service says otherwise (docs/02).
var DefaultTransports = []string{"rpc", "rest"}

// Method is every annotation that applies to one RPC, already resolved:
// per-RPC beats service-level beats project default beats [rpc, rest].
type Method struct {
	Desc protoreflect.MethodDescriptor

	// Transports is the resolved road set, in canonical order.
	Transports []string

	// TransportsFrom says where the resolution came from: "method",
	// "service", "config" or "default". `ix describe` does not print it, but
	// lint and doctor need to tell a declared road from an inherited one.
	TransportsFrom string

	Group      string
	Internal   bool
	Idempotent bool

	// CLI is the (cli.command) path; nil when the annotation is absent.
	CLI      []string
	CLIArgs  []string
	CLIShort string
	CLISkip  bool

	// HTTP is the google.api.http rule, "GET /v1/x" form; empty when absent.
	HTTPMethod string
	HTTPPath   string

	// Auth is nil when the (auth) annotation is absent -- which is a
	// perfectly ordinary state, since authorization is an optional module.
	Auth *Auth
}

// Auth is the optional module's annotation, read structurally.
type Auth struct {
	// Permission is the atom: resource + "." + verb, "providers.read".
	Permission string

	// AuthTypes are the accepted credential kinds with their enum prefix
	// stripped: SESSION, API_KEY, WORKLOAD.
	AuthTypes []string

	Public   bool
	Platform bool
}

// ExposedOn reports whether the resolved fan-out includes a road.
func (m *Method) ExposedOn(t string) bool {
	for _, x := range m.Transports {
		if x == t {
			return true
		}
	}
	return false
}

// Service is the service-level annotation.
type Service struct {
	Transports []string
	Group      string
	Set        bool
}

// ForService reads (service_transports).
func ForService(sd protoreflect.ServiceDescriptor) Service {
	opts, _ := sd.Options().(*descriptorpb.ServiceOptions)
	if opts == nil {
		return Service{}
	}
	msg := extensionMessage(opts, NumServiceTransports)
	if msg == nil {
		return Service{}
	}
	return Service{
		Transports: transportList(msg),
		Group:      stringField(msg, "group"),
		Set:        true,
	}
}

// ForMethod resolves every annotation on an RPC. configDefault is
// transports.default from interchange.yaml; nil means fall through to
// [rpc, rest].
func ForMethod(md protoreflect.MethodDescriptor, configDefault []string) *Method {
	m := &Method{Desc: md}
	opts, _ := md.Options().(*descriptorpb.MethodOptions)

	if opts != nil {
		m.Idempotent = opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS
		m.Internal = boolExtension(opts, NumInternal)

		if t := extensionMessage(opts, NumTransports); t != nil {
			if on := transportList(t); len(on) > 0 {
				m.Transports, m.TransportsFrom = on, "method"
			}
			m.Group = stringField(t, "group")
		}
		if c := extensionMessage(opts, NumCommand); c != nil {
			m.CLI = stringsField(c, "path")
			m.CLIArgs = stringsField(c, "args")
			m.CLIShort = stringField(c, "short")
			m.CLISkip = boolField(c, "skip")
		}
		if a := extensionMessage(opts, NumAuth); a != nil {
			m.Auth = readAuth(a)
		}
		if h := extensionMessage(opts, NumHTTP); h != nil {
			m.HTTPMethod, m.HTTPPath = readHTTP(h)
		}
	}

	if m.Transports == nil {
		svc := ForService(md.Parent().(protoreflect.ServiceDescriptor))
		switch {
		case svc.Set && len(svc.Transports) > 0:
			m.Transports, m.TransportsFrom = svc.Transports, "service"
			if m.Group == "" {
				m.Group = svc.Group
			}
		case len(configDefault) > 0:
			m.Transports, m.TransportsFrom = canonical(configDefault), "config"
		default:
			m.Transports, m.TransportsFrom = DefaultTransports, "default"
		}
	}
	return m
}

func readAuth(m protoreflect.Message) *Auth {
	a := &Auth{
		Public:   boolField(m, "public"),
		Platform: boolField(m, "platform"),
	}
	for _, v := range enumsField(m, "auth_types") {
		a.AuthTypes = append(a.AuthTypes, strings.TrimPrefix(v, "AUTH_TYPE_"))
	}
	if p := messageField(m, "permission"); p != nil {
		res := stringField(p, "resource")
		verb := ""
		if vs := enumField(p, "verb"); vs != "" {
			verb = strings.ToLower(strings.TrimPrefix(vs, "VERB_"))
		}
		switch {
		case res != "" && verb != "" && verb != "unspecified":
			a.Permission = res + "." + verb
		case res != "":
			a.Permission = res
		}
	}
	return a
}

// httpVerbs is google.api.HttpRule's oneof, in the order a rule declares at
// most one of them.
var httpVerbs = []string{"get", "put", "post", "delete", "patch"}

func readHTTP(m protoreflect.Message) (method, path string) {
	for _, v := range httpVerbs {
		if p := stringField(m, v); p != "" {
			return strings.ToUpper(v), p
		}
	}
	if c := messageField(m, "custom"); c != nil {
		return strings.ToUpper(stringField(c, "kind")), stringField(c, "path")
	}
	return "", ""
}

// TenantField finds the request field carrying the tenant: one annotated
// with (tenant_id_field), else a field literally named tenant_id. The second
// half is the convention docs/02 calls a suggestion, so the result says which
// it was.
func TenantField(md protoreflect.MethodDescriptor) (name string, declared bool) {
	fields := md.Input().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		opts, _ := f.Options().(*descriptorpb.FieldOptions)
		if opts != nil && boolExtension(opts, NumTenantIDField) {
			return string(f.Name()), true
		}
	}
	if f := fields.ByName("tenant_id"); f != nil {
		return "tenant_id", false
	}
	return "", false
}

// FieldNames lists a message's fields in declaration order -- what `ix
// describe` prints beside the request and response type.
func FieldNames(md protoreflect.MessageDescriptor) []string {
	fs := md.Fields()
	out := make([]string, 0, fs.Len())
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		n := string(f.Name())
		if f.IsList() {
			n += "[]"
		}
		if f.IsMap() {
			n += "{}"
		}
		out = append(out, n)
	}
	return out
}

// canonicalOrder is the order roads are printed in, everywhere.
var canonicalOrder = map[string]int{"rpc": 0, "rest": 1, "bus": 2, "mqtt": 3, "ws": 4}

// Roads is the full road list in print order.
var Roads = []string{"rpc", "rest", "bus", "mqtt", "ws"}

func canonical(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.ToLower(s)
		if _, ok := canonicalOrder[s]; !ok || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return canonicalOrder[out[i]] < canonicalOrder[out[j]] })
	return out
}

func transportList(m protoreflect.Message) []string {
	var out []string
	for _, v := range enumsField(m, "on") {
		name := strings.ToLower(strings.TrimPrefix(v, "TRANSPORT_"))
		if name == "unspecified" {
			continue
		}
		out = append(out, name)
	}
	return canonical(out)
}

// --- generic descriptor access -------------------------------------------
//
// Everything below reads a message it has no Go type for. That is the point:
// the annotation's shape comes from the descriptor in the image, so an
// annotation ix has never been compiled against still prints.

func extension(opts proto.Message, number int32) (protoreflect.Value, protoreflect.ExtensionType, bool) {
	if opts == nil {
		return protoreflect.Value{}, nil, false
	}
	var val protoreflect.Value
	var xt protoreflect.ExtensionType
	found := false
	proto.RangeExtensions(opts, func(t protoreflect.ExtensionType, v any) bool {
		if int32(t.TypeDescriptor().Number()) != number {
			return true
		}
		val, xt, found = protoreflect.ValueOf(v), t, true
		return false
	})
	if !found {
		return protoreflect.Value{}, nil, false
	}
	return val, xt, true
}

func extensionMessage(opts proto.Message, number int32) protoreflect.Message {
	v, xt, ok := extension(opts, number)
	if !ok || xt.TypeDescriptor().Kind() != protoreflect.MessageKind {
		return nil
	}
	m, ok := v.Interface().(proto.Message)
	if !ok {
		return nil
	}
	return m.ProtoReflect()
}

func boolExtension(opts proto.Message, number int32) bool {
	v, xt, ok := extension(opts, number)
	if !ok || xt.TypeDescriptor().Kind() != protoreflect.BoolKind {
		return false
	}
	b, _ := v.Interface().(bool)
	return b
}

func field(m protoreflect.Message, name string) (protoreflect.FieldDescriptor, protoreflect.Value, bool) {
	if m == nil {
		return nil, protoreflect.Value{}, false
	}
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return nil, protoreflect.Value{}, false
	}
	return fd, m.Get(fd), true
}

func stringField(m protoreflect.Message, name string) string {
	fd, v, ok := field(m, name)
	if !ok || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return ""
	}
	return v.String()
}

func boolField(m protoreflect.Message, name string) bool {
	fd, v, ok := field(m, name)
	if !ok || fd.Kind() != protoreflect.BoolKind {
		return false
	}
	return v.Bool()
}

func stringsField(m protoreflect.Message, name string) []string {
	fd, v, ok := field(m, name)
	if !ok || !fd.IsList() || fd.Kind() != protoreflect.StringKind {
		return nil
	}
	l := v.List()
	out := make([]string, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		out = append(out, l.Get(i).String())
	}
	return out
}

func enumsField(m protoreflect.Message, name string) []string {
	fd, v, ok := field(m, name)
	if !ok || !fd.IsList() || fd.Kind() != protoreflect.EnumKind {
		return nil
	}
	l := v.List()
	out := make([]string, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		out = append(out, enumName(fd, l.Get(i).Enum()))
	}
	return out
}

func enumField(m protoreflect.Message, name string) string {
	fd, v, ok := field(m, name)
	if !ok || fd.IsList() || fd.Kind() != protoreflect.EnumKind {
		return ""
	}
	return enumName(fd, v.Enum())
}

func enumName(fd protoreflect.FieldDescriptor, n protoreflect.EnumNumber) string {
	if ev := fd.Enum().Values().ByNumber(n); ev != nil {
		return string(ev.Name())
	}
	return fmt.Sprint(int32(n))
}

func messageField(m protoreflect.Message, name string) protoreflect.Message {
	fd, v, ok := field(m, name)
	if !ok || fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
		return nil
	}
	if !m.Has(fd) {
		return nil
	}
	return v.Message()
}
