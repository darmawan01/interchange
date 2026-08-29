// Package testfixture is a hand-written stand-in for generated service code,
// built over a real compiled descriptor set.
//
// The descriptors matter here in a way they do not for a hand-built
// ServiceDesc: the transcoder routes on the google.api.http annotation, so a
// fixture that invents its descriptor tests nothing. The .proto in api/ is
// compiled by buf and the resulting FileDescriptorSet is embedded, which is
// also what lets the messages be dynamic -- no generated Go, and therefore no
// chance of a Go type quietly supplying what an annotation should have.
//
// Regenerate testdata/fixture.binpb from the repo root with:
//
//	buf build --config binding/rest/internal/testfixture/buf.workspace.yaml \
//	  -o binding/rest/internal/testfixture/testdata/fixture.binpb .
package testfixture

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rest/internal/protoann"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

//go:embed testdata/fixture.binpb
var descriptorSet []byte

// Service is the fully-qualified name of the fixture service.
const Service = "rest.test.v1.ProbeService"

// FilePath is where the fixture's own .proto lands in the descriptor set.
const FilePath = "rest/test/v1/probe.proto"

// Procedures on the fixture service.
var (
	GetProbeProcedure        = interchange.Procedure(Service, "GetProbe")
	CreateProbeProcedure     = interchange.Procedure(Service, "CreateProbe")
	FailProbeProcedure       = interchange.Procedure(Service, "FailProbe")
	RpcOnlyProbeProcedure    = interchange.Procedure(Service, "RpcOnlyProbe")
	PingProbeProcedure       = interchange.Procedure(Service, "PingProbe")
	ReconcileProbesProcedure = interchange.Procedure(Service, "ReconcileProbes")
)

// FailReason is what FailProbe returns, on whichever road it is called.
const FailReason = "PROBE_NOT_FOUND"

var (
	once  sync.Once
	files *protoregistry.Files
	fdset *descriptorpb.FileDescriptorSet
	load  error
)

func loaded() (*protoregistry.Files, error) {
	once.Do(func() {
		fdset = &descriptorpb.FileDescriptorSet{}
		// The default resolver is what turns the serialized (google.api.http)
		// and (interchange.transport.v1.transports) bytes back into extension
		// fields: both extension types are linked into this binary.
		if load = proto.Unmarshal(descriptorSet, fdset); load != nil {
			return
		}
		files, load = protodesc.NewFiles(fdset)
	})
	return files, load
}

// FileDescriptorSet returns the compiled fixture, for a consumer that reads
// descriptors rather than a registry -- the OpenAPI emitter, for one.
func FileDescriptorSet() (*descriptorpb.FileDescriptorSet, error) {
	if _, err := loaded(); err != nil {
		return nil, err
	}
	return fdset, nil
}

// ServiceDescriptor returns the fixture service's descriptor.
func ServiceDescriptor() (protoreflect.ServiceDescriptor, error) {
	reg, err := loaded()
	if err != nil {
		return nil, err
	}
	fd, err := reg.FindFileByPath(FilePath)
	if err != nil {
		return nil, err
	}
	sd := fd.Services().ByName("ProbeService")
	if sd == nil {
		return nil, fmt.Errorf("testfixture: %s has no ProbeService", FilePath)
	}
	return sd, nil
}

// Desc returns the service descriptor as generated code would emit it: the
// roads and the internal flag resolved from the annotations, the message
// factories bound to the descriptor, the handler bound to the method.
func Desc() (*interchange.ServiceDesc, error) {
	svc, err := ServiceDescriptor()
	if err != nil {
		return nil, err
	}
	out := &interchange.ServiceDesc{Name: string(svc.FullName()), Desc: svc}
	methods := svc.Methods()
	for i := 0; i < methods.Len(); i++ {
		md := methods.Get(i)
		on, err := protoann.Transports(md)
		if err != nil {
			return nil, err
		}
		handler, ok := handlers[string(md.Name())]
		if !ok {
			return nil, fmt.Errorf("testfixture: no handler for %s", md.FullName())
		}
		in, outMsg := md.Input(), md.Output()
		out.Methods = append(out.Methods, interchange.MethodDesc{
			Name:        string(md.Name()),
			Service:     string(svc.FullName()),
			Procedure:   interchange.Procedure(string(svc.FullName()), string(md.Name())),
			NewRequest:  func() proto.Message { return dynamicpb.NewMessage(in) },
			NewResponse: func() proto.Message { return dynamicpb.NewMessage(outMsg) },
			Transports:  on,
			Idempotent:  protoann.Idempotent(md),
			Internal:    protoann.Internal(md),
			Desc:        md,
			Handler:     handler,
		})
	}
	return out, nil
}

// Impl is the fixture implementation. It exists to make the transcoding
// visible: every handler echoes what was bound onto the request, so a test
// can tell a path variable from a query parameter from a body field.
type Impl struct{}

// New returns the fixture implementation.
func New() *Impl { return &Impl{} }

var handlers = map[string]func(context.Context, any, proto.Message) (proto.Message, error){
	"GetProbe":        getProbe,
	"CreateProbe":     createProbe,
	"FailProbe":       failProbe,
	"RpcOnlyProbe":    rpcOnlyProbe,
	"PingProbe":       pingProbe,
	"ReconcileProbes": reconcileProbes,
}

func getProbe(_ context.Context, _ any, req proto.Message) (proto.Message, error) {
	in := req.ProtoReflect()
	out := respond(req, "GetProbeResponse")
	probe := mutable(out, "probe")
	setString(probe, "probe_id", str(in, "probe_id"))
	setString(probe, "display_name", "probe:"+str(in, "probe_id"))
	setEnum(probe, "state", 1) // PROBE_STATE_READY
	setBool(probe, "active", true)
	// The page size arrives as a nested query parameter, page.page_size.
	// Echoing it is how a test proves the transcoder walked into the message.
	page := in.Get(field(in, "page")).Message()
	setInt32(probe, "attempt_count", int32(page.Get(field(page, "page_size")).Int()))
	appendString(probe, "tags", "alpha")
	return out, nil
}

func createProbe(_ context.Context, _ any, req proto.Message) (proto.Message, error) {
	in := req.ProtoReflect()
	out := respond(req, "CreateProbeResponse")
	probe := mutable(out, "probe")
	setString(probe, "probe_id", "probe_created")
	setString(probe, "display_name", str(in, "display_name"))
	setEnum(probe, "state", 1)
	tags := in.Get(field(in, "tags")).List()
	for i := 0; i < tags.Len(); i++ {
		appendString(probe, "tags", tags.Get(i).String())
	}
	return out, nil
}

func failProbe(_ context.Context, _ any, req proto.Message) (proto.Message, error) {
	return nil, interchange.Errorf(interchange.CodeNotFound,
		"no such probe %q", str(req.ProtoReflect(), "probe_id")).WithReason(FailReason)
}

func rpcOnlyProbe(_ context.Context, _ any, req proto.Message) (proto.Message, error) {
	out := respond(req, "RpcOnlyProbeResponse")
	probe := mutable(out, "probe")
	setString(probe, "probe_id", str(req.ProtoReflect(), "probe_id"))
	setString(probe, "display_name", "rpc-only")
	return out, nil
}

func pingProbe(_ context.Context, _ any, req proto.Message) (proto.Message, error) {
	out := respond(req, "PingProbeResponse")
	setString(out.ProtoReflect(), "probe_id", str(req.ProtoReflect(), "probe_id"))
	return out, nil
}

func reconcileProbes(_ context.Context, _ any, req proto.Message) (proto.Message, error) {
	out := respond(req, "ReconcileProbesResponse")
	setInt32(out.ProtoReflect(), "reconciled", 7)
	return out, nil
}

// respond mints the response message that pairs with a request. Going through
// the descriptor rather than a Go type is the point: the fixture has no
// generated code, so nothing here can accidentally depend on some.
func respond(req proto.Message, name protoreflect.Name) proto.Message {
	md := req.ProtoReflect().Descriptor().ParentFile().Messages().ByName(name)
	return dynamicpb.NewMessage(md)
}

func field(m protoreflect.Message, name protoreflect.Name) protoreflect.FieldDescriptor {
	fd := m.Descriptor().Fields().ByName(name)
	if fd == nil {
		panic(fmt.Sprintf("testfixture: %s has no field %s", m.Descriptor().FullName(), name))
	}
	return fd
}

func mutable(m proto.Message, name protoreflect.Name) protoreflect.Message {
	r := m.ProtoReflect()
	return r.Mutable(field(r, name)).Message()
}

func str(m protoreflect.Message, name protoreflect.Name) string {
	return m.Get(field(m, name)).String()
}

func setString(m protoreflect.Message, name protoreflect.Name, v string) {
	m.Set(field(m, name), protoreflect.ValueOfString(v))
}

func setInt32(m protoreflect.Message, name protoreflect.Name, v int32) {
	m.Set(field(m, name), protoreflect.ValueOfInt32(v))
}

func setBool(m protoreflect.Message, name protoreflect.Name, v bool) {
	m.Set(field(m, name), protoreflect.ValueOfBool(v))
}

func setEnum(m protoreflect.Message, name protoreflect.Name, v protoreflect.EnumNumber) {
	m.Set(field(m, name), protoreflect.ValueOfEnum(v))
}

func appendString(m protoreflect.Message, name protoreflect.Name, v string) {
	list := m.Mutable(field(m, name)).List()
	list.Append(protoreflect.ValueOfString(v))
}

// The helpers below are the fixture's stand-in for generated Go types: a test
// needs to build a request and read a response, and there is deliberately no
// generated struct to do it with. They panic rather than return an error --
// a fixture that cannot mint its own message is a broken test, not a failed
// call.

// NewMessage mints a message from the fixture's descriptors by its bare name.
func NewMessage(name string) proto.Message {
	files, err := loaded()
	if err != nil {
		panic(err)
	}
	fd, err := files.FindFileByPath(FilePath)
	if err != nil {
		panic(err)
	}
	md := fd.Messages().ByName(protoreflect.Name(name))
	if md == nil {
		panic(fmt.Sprintf("testfixture: no message %s in %s", name, FilePath))
	}
	return dynamicpb.NewMessage(md)
}

// SetString sets a top-level string field.
func SetString(m proto.Message, name, v string) {
	setString(m.ProtoReflect(), protoreflect.Name(name), v)
}

// GetString reads a string field, walking into sub-messages along path.
func GetString(m proto.Message, path ...string) string {
	cur := m.ProtoReflect()
	for i, name := range path {
		fd := field(cur, protoreflect.Name(name))
		if i == len(path)-1 {
			return cur.Get(fd).String()
		}
		cur = cur.Get(fd).Message()
	}
	return ""
}

// GetInt32 reads an int32 field, walking into sub-messages along path.
func GetInt32(m proto.Message, path ...string) int32 {
	cur := m.ProtoReflect()
	for i, name := range path {
		fd := field(cur, protoreflect.Name(name))
		if i == len(path)-1 {
			return int32(cur.Get(fd).Int())
		}
		cur = cur.Get(fd).Message()
	}
	return 0
}
