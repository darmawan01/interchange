// protoc-gen-bus emits, for every proto service, the *interchange.ServiceDesc
// that both the HTTP binding and the bus binding dispatch through -- which is
// what makes "one contract, every road" a fact about the code rather than a
// slogan. It also emits the server interface, a typed bus client for services
// that travel a broker, and a registration helper.
//
// It does not emit subscribers. The engine's server subscribes from the
// registry (Registry.MethodsOn), so the ServiceDesc below already is the
// subscriber: generating one per service would be a second source of truth
// for the same fan-out.
package main

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	protogen.Options{}.Run(func(p *protogen.Plugin) error {
		p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		return generate(p)
	})
}
