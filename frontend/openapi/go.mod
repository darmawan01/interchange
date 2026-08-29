module github.com/darmawan01/interchange/frontend/openapi

// 1.25.7 rather than the 1.25.0 the sibling modules use: libopenapi declares
// it, and a module cannot require less than its dependencies.
go 1.25.7

require (
	github.com/bufbuild/protocompile v0.14.1
	github.com/darmawan01/interchange v0.0.0
	github.com/pb33f/libopenapi v0.38.7
	go.yaml.in/yaml/v4 v4.0.0-rc.6
	google.golang.org/genproto/googleapis/api v0.0.0-20260825221802-da73d73af1c5
	google.golang.org/protobuf v1.36.12
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

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/pb33f/jsonpath v0.8.2 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	golang.org/x/sync v0.22.0 // indirect
)

replace github.com/darmawan01/interchange => ../..

replace github.com/darmawan01/interchange/auth => ../../auth

replace github.com/darmawan01/interchange/tools => ../../tools
