module github.com/darmawan01/interchange/examples/catalog

go 1.25.0

// The example installs every optional module, because the point of an example
// is to show the seams working together. None of them is required: core plus
// an empty chain serves the same registry (see acceptance_test.go).
require (
	connectrpc.com/connect v1.20.0
	github.com/darmawan01/interchange v0.0.0
	github.com/darmawan01/interchange/auth v0.0.0
	github.com/darmawan01/interchange/binding/rest v0.0.0
	github.com/darmawan01/interchange/driver/nats v0.0.0
	github.com/darmawan01/interchange/errors v0.0.0
	github.com/darmawan01/interchange/tools v0.0.0
	github.com/darmawan01/interchange/validate v0.0.0
	github.com/nats-io/nats-server/v2 v2.12.2
	github.com/nats-io/nats.go v1.53.1
	github.com/spf13/cobra v1.10.2
	google.golang.org/protobuf v1.36.12
)

require google.golang.org/genproto/googleapis/api v0.0.0-20260223185530-2f722ef697dc

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20260825204119-511051f7f437.1 // indirect
	buf.build/go/protovalidate v1.3.0 // indirect
	cel.dev/expr v0.25.1 // indirect
	connectrpc.com/vanguard v0.4.0 // indirect
	github.com/antithesishq/antithesis-sdk-go v0.7.2-default-no-op // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/google/cel-go v0.30.0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/nats-io/jwt/v2 v2.8.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/exp v0.0.0-20250813145105-42675adae3e6 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260223185530-2f722ef697dc // indirect
)

replace github.com/darmawan01/interchange => ../..

replace github.com/darmawan01/interchange/auth => ../../auth

replace github.com/darmawan01/interchange/binding/rest => ../../binding/rest

replace github.com/darmawan01/interchange/driver/nats => ../../driver/nats

replace github.com/darmawan01/interchange/errors => ../../errors

replace github.com/darmawan01/interchange/tools => ../../tools

replace github.com/darmawan01/interchange/validate => ../../validate
