module github.com/darmawan01/interchange/frontend/dsl

go 1.25.0

require (
	github.com/bufbuild/protocompile v0.14.1
	github.com/darmawan01/interchange v0.0.0
	google.golang.org/genproto/googleapis/api v0.0.0-20250811230008-5f3141c8851a
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

// Tests only. The frontend imports neither module: an auth or cli annotation
// arrives as descriptors in Options.Deps, so an optional module stays
// optional. The tests link the generated types to assert the extension values
// survived the transform, and internal/nolink proves the frontend works
// without them.
require (
	github.com/darmawan01/interchange/auth v0.0.0
	github.com/darmawan01/interchange/tools v0.0.0
)

require golang.org/x/sync v0.8.0 // indirect

replace github.com/darmawan01/interchange => ../..

replace github.com/darmawan01/interchange/auth => ../../auth

replace github.com/darmawan01/interchange/tools => ../../tools
