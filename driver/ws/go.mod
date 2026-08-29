module github.com/darmawan01/interchange/driver/ws

go 1.25.0

require (
	github.com/darmawan01/interchange v0.0.0
	google.golang.org/protobuf v1.36.12
)

require github.com/coder/websocket v1.8.15

replace github.com/darmawan01/interchange => ../..
