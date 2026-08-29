// Package testsvc is a hand-written stand-in for generated service code,
// over the fixture contract in validate/testdata. It exists so the module can
// be tested against a real .proto compiled by buf -- rules read off a
// descriptor, not a struct tag -- before protoc-gen-bus exists.
package testsvc

import (
	"context"

	"github.com/darmawan01/interchange"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	testdatav1 "github.com/darmawan01/interchange/validate/internal/testpb/interchange/validate/testdata/v1"
	"google.golang.org/protobuf/proto"
)

// Service is the fully-qualified name of the fixture service.
const Service = "interchange.validate.testdata.v1.ProviderService"

// CreateProviderProcedure is the one procedure it exposes.
var CreateProviderProcedure = interchange.Procedure(Service, "CreateProvider")

// Impl is the handler.
type Impl struct {
	// Called records every request that reached the handler. A validation
	// test's sharpest assertion is that this stayed empty.
	Called []*testdatav1.CreateProviderRequest
}

// CreateProvider echoes the name back as an id.
func (i *Impl) CreateProvider(_ context.Context, in *testdatav1.CreateProviderRequest) (*testdatav1.CreateProviderResponse, error) {
	i.Called = append(i.Called, in)
	return &testdatav1.CreateProviderResponse{Id: "provider/" + in.GetName()}, nil
}

// Desc returns the service descriptor as generated code would emit it.
func Desc() *interchange.ServiceDesc {
	return &interchange.ServiceDesc{
		Name: Service,
		Methods: []interchange.MethodDesc{
			{
				Name:        "CreateProvider",
				Service:     Service,
				Procedure:   CreateProviderProcedure,
				NewRequest:  func() proto.Message { return &testdatav1.CreateProviderRequest{} },
				NewResponse: func() proto.Message { return &testdatav1.CreateProviderResponse{} },
				Transports: []transportv1.Transport{
					transportv1.Transport_TRANSPORT_RPC,
					transportv1.Transport_TRANSPORT_BUS,
				},
				Handler: func(ctx context.Context, impl any, req proto.Message) (proto.Message, error) {
					return impl.(*Impl).CreateProvider(ctx, req.(*testdatav1.CreateProviderRequest))
				},
			},
		},
	}
}
