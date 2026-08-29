// Package testsvc is a hand-written stand-in for generated service code. It
// exists so core can be tested before protoc-gen-bus exists, and so a core
// test never depends on the example service.
package testsvc

import (
	"context"

	"github.com/darmawan01/interchange"
	commonv1 "github.com/darmawan01/interchange/gen/go/interchange/common/v1"
	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
)

// Service is the fully-qualified name of the test service.
const Service = "interchange.test.v1.EchoService"

// Procedures on the test service.
var (
	EchoProcedure    = interchange.Procedure(Service, "Echo")
	FailProcedure    = interchange.Procedure(Service, "Fail")
	BusOnlyProcedure = interchange.Procedure(Service, "BusOnly")
)

// Impl is the handler. Both methods take and return a Problem, which is a
// message that already exists -- the test is about dispatch, not about types.
type Impl struct {
	Echo func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error)
	Fail func(ctx context.Context, in *commonv1.Problem) (*commonv1.Problem, error)
}

func newProblem() proto.Message { return &commonv1.Problem{} }

// Desc returns the service descriptor as generated code would emit it.
func Desc() *interchange.ServiceDesc {
	all := []transportv1.Transport{
		transportv1.Transport_TRANSPORT_RPC,
		transportv1.Transport_TRANSPORT_REST,
		transportv1.Transport_TRANSPORT_BUS,
	}
	return &interchange.ServiceDesc{
		Name: Service,
		Methods: []interchange.MethodDesc{
			{
				Name:        "Echo",
				Service:     Service,
				Procedure:   EchoProcedure,
				NewRequest:  newProblem,
				NewResponse: newProblem,
				Transports:  all,
				Idempotent:  true,
				Handler: func(ctx context.Context, impl any, req proto.Message) (proto.Message, error) {
					return impl.(*Impl).Echo(ctx, req.(*commonv1.Problem))
				},
			},
			{
				Name:        "Fail",
				Service:     Service,
				Procedure:   FailProcedure,
				NewRequest:  newProblem,
				NewResponse: newProblem,
				Transports:  all,
				Handler: func(ctx context.Context, impl any, req proto.Message) (proto.Message, error) {
					return impl.(*Impl).Fail(ctx, req.(*commonv1.Problem))
				},
			},
			{
				// Declared on the bus only: an RPC binding must refuse it,
				// which is what makes the annotation load-bearing rather
				// than decorative.
				Name:        "BusOnly",
				Service:     Service,
				Procedure:   BusOnlyProcedure,
				NewRequest:  newProblem,
				NewResponse: newProblem,
				Transports:  []transportv1.Transport{transportv1.Transport_TRANSPORT_BUS},
				Handler: func(ctx context.Context, impl any, req proto.Message) (proto.Message, error) {
					return impl.(*Impl).Echo(ctx, req.(*commonv1.Problem))
				},
			},
		},
	}
}

// EchoImpl returns an implementation that echoes its input with a marker.
func EchoImpl() *Impl {
	return &Impl{
		Echo: func(_ context.Context, in *commonv1.Problem) (*commonv1.Problem, error) {
			out := proto.Clone(in).(*commonv1.Problem)
			out.Title = "echo:" + in.GetTitle()
			return out, nil
		},
		Fail: func(context.Context, *commonv1.Problem) (*commonv1.Problem, error) {
			return nil, interchange.Errorf(interchange.CodeNotFound, "no such provider").
				WithReason("PROVIDER_NOT_FOUND")
		},
	}
}
