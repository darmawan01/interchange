package auth_test

import (
	"context"
	"testing"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/auth"
	authv1 "github.com/darmawan01/interchange/auth/gen/go/interchange/auth/v1"
	"github.com/darmawan01/interchange/auth/internal/fixture"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The fixture contract, compiled in TestMain so every test reads the same
// descriptors -- the same ones a generated ServiceDesc would carry.
const (
	service          = "interchange.authtest.v1.PingService"
	procPing         = "/" + service + "/Ping"
	procPublic       = "/" + service + "/PingPublic"
	procPlatform     = "/" + service + "/PingPlatform"
	procAlias        = "/" + service + "/PingAlias"
	procUnannotated  = "/" + service + "/PingUnannotated"
	tokenReader      = "reader-token"
	tokenWriter      = "writer-token"
	tokenNoTenant    = "other-tenant-token"
	tokenWorkloadKey = "workload-key"
	tokenWeakKey     = "workload-key-read-only"
)

var pingDesc *interchange.ServiceDesc

func TestMain(m *testing.M) {
	files := fixture.MustCompile("pingsvc.proto")
	sd, err := fixture.Service(files, service)
	if err != nil {
		panic(err)
	}
	pingDesc = fixture.ServiceDesc(sd, []transportv1.Transport{
		transportv1.Transport_TRANSPORT_RPC,
		transportv1.Transport_TRANSPORT_BUS,
	})
	m.Run()
}

// echo is the fixture handler: it copies note across so a test can tell an
// allowed call from a denied one by its result rather than by its absence.
var echo = fixture.Handler(func(_ context.Context, procedure string, req proto.Message) (proto.Message, error) {
	in := req.ProtoReflect()
	out := newResponse(procedure)
	setString(out, "note", getString(in, "note"))
	return out, nil
})

func methodDesc(procedure string) *interchange.MethodDesc {
	for i := range pingDesc.Methods {
		if pingDesc.Methods[i].Procedure == procedure {
			return &pingDesc.Methods[i]
		}
	}
	panic("no such procedure: " + procedure)
}

func newRequest(procedure string) proto.Message  { return methodDesc(procedure).NewRequest() }
func newResponse(procedure string) proto.Message { return methodDesc(procedure).NewResponse() }

func setString(m proto.Message, field, value string) {
	r := m.ProtoReflect()
	fd := r.Descriptor().Fields().ByName(protoreflect.Name(field))
	if fd == nil {
		panic("no field " + field + " on " + string(r.Descriptor().FullName()))
	}
	r.Set(fd, protoreflect.ValueOfString(value))
}

func getString(m protoreflect.Message, field string) string {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(field))
	if fd == nil {
		return ""
	}
	return m.Get(fd).String()
}

// request builds a fixture request with the named string fields set.
func request(procedure string, fields map[string]string) proto.Message {
	msg := newRequest(procedure)
	for k, v := range fields {
		setString(msg, k, v)
	}
	return msg
}

// dispatch registers the fixture service behind chain and makes one call --
// through interchange.Registry.Dispatch, because that is the only place a
// chain runs and the only place MethodDesc reaches the context.
func dispatch(t *testing.T, chain *interchange.ChainSpec, procedure string, msg proto.Message, md interchange.Metadata) (*interchange.Envelope, error) {
	t.Helper()
	reg := interchange.NewRegistry()
	if err := reg.Register(pingDesc, echo, chain); err != nil {
		t.Fatalf("register: %v", err)
	}
	if md == nil {
		md = interchange.Metadata{}
	}
	if msg == nil {
		msg = newRequest(procedure)
	}
	return reg.Dispatch(context.Background(), &interchange.Envelope{
		Procedure: procedure,
		Metadata:  md,
		Msg:       msg,
	})
}

// tokens is the stock authenticator's table: a tenant-scoped reader, a writer,
// a principal belonging to another tenant, and a workload key.
func tokens() *auth.TokenAuthenticator {
	return auth.NewTokenAuthenticator(map[string]*auth.Principal{
		tokenReader: {
			Subject:  "user:reader",
			AuthType: authv1.AuthType_AUTH_TYPE_SESSION,
			Roles:    []string{"reader"},
			Tenants:  []string{"acme"},
		},
		tokenWriter: {
			Subject:  "user:writer",
			AuthType: authv1.AuthType_AUTH_TYPE_SESSION,
			Roles:    []string{"writer"},
			Tenants:  []string{"acme"},
		},
		tokenNoTenant: {
			Subject:  "user:other",
			AuthType: authv1.AuthType_AUTH_TYPE_SESSION,
			Roles:    []string{"reader"},
			Tenants:  []string{"globex"},
		},
		tokenWorkloadKey: {
			Subject:  "svc:indexer",
			AuthType: authv1.AuthType_AUTH_TYPE_WORKLOAD,
			Roles:    []string{"platform"},
		},
		// The right kind of credential holding the wrong permission: what
		// separates a denial by auth type from a denial by atom.
		tokenWeakKey: {
			Subject:  "svc:reader",
			AuthType: authv1.AuthType_AUTH_TYPE_WORKLOAD,
			Roles:    []string{"reader"},
		},
	})
}

func rbac(t *testing.T) *auth.RBAC {
	t.Helper()
	r, err := auth.NewRBAC(map[string][]string{
		"reader":   {"ping.read"},
		"writer":   {"ping.read", "ping.edit"},
		"platform": {"ping.*"},
	})
	if err != nil {
		t.Fatalf("rbac: %v", err)
	}
	return r
}

func bearer(token string) interchange.Metadata {
	return interchange.NewMetadata(map[string]string{"authorization": "Bearer " + token})
}

// assertDenied checks the code and the reason, because the reason is what a
// client branches on and what has to be identical on every road.
func assertDenied(t *testing.T, err error, code interchange.Code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a denial with reason %s, got none", reason)
	}
	if got := interchange.CodeOf(err); got != code {
		t.Fatalf("code is %v, want %v (err: %v)", got, code, err)
	}
	if got := interchange.ReasonOf(err); got != reason {
		t.Fatalf("reason is %q, want %q (err: %v)", got, reason, err)
	}
}
