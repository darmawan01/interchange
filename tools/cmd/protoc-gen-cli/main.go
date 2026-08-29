// protoc-gen-cli emits a Cobra command tree from the (interchange.cli.v1.command)
// annotation.
//
// The trade-off it is built around: a generated CLI that fronts only the
// annotated RPCs is a tool whose gaps are invisible -- a user cannot tell an
// unsupported operation from one nobody got round to annotating. So every
// generated service also reports its own Coverage(), and building with
// require_annotation=true turns a missing annotation into a build failure
// instead of a hole. Choose per repo: an internal platform should require the
// annotation; a repo with a hand-written CLI for its important paths should
// not.
package main

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

type config struct {
	// requireAnnotation fails the build on any RPC that is neither annotated
	// nor explicitly skipped.
	requireAnnotation bool
}

func (c *config) set(name, value string) error {
	switch name {
	case "require_annotation":
		switch value {
		case "", "true", "1":
			c.requireAnnotation = true
		case "false", "0":
			c.requireAnnotation = false
		default:
			return fmt.Errorf("require_annotation: %q is not a boolean", value)
		}
		return nil
	default:
		return fmt.Errorf("unknown option %q", name)
	}
}

func main() {
	cfg := &config{}
	opts := protogen.Options{ParamFunc: cfg.set}
	opts.Run(func(p *protogen.Plugin) error {
		p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		return generate(p, cfg)
	})
}
