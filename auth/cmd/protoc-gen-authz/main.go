// Command protoc-gen-authz emits the permission table declared by the (auth)
// annotation, and fails the build on an RPC that declares nothing or declares
// nonsense.
//
// It is an ordinary protoc plugin with no privileged access -- the test of
// whether the extension point is real. It ships with the /auth module and is
// installed only by adopters who want the build-time gate:
//
//	plugins:
//	  - local: bin/protoc-gen-authz
//	    out: gen/go/authz
//	    strategy: all           # the table is cross-cutting: one pass, whole tree
//	    opt:
//	      - package=authz
//	      - on_missing_annotation=error
package main

import (
	"github.com/darmawan01/interchange/auth/internal/authgen"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	opts := authgen.DefaultOptions()
	protogen.Options{ParamFunc: opts.Set}.Run(func(p *protogen.Plugin) error {
		p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		return authgen.Generate(p, opts)
	})
}
