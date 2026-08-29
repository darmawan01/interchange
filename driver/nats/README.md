# NATS driver

`interchange.Driver` over [nats.go](https://github.com/nats-io/nats.go): subject naming, header
translation and the broker's reply path. Correlation, deadlines, chunking and replay suppression
stay in the engine.

## Wiring

```go
conn, err := nats.Connect(nats.DefaultURL)
drv, err := natsdriver.New(conn)                       // core NATS
drv, err := natsdriver.NewJetStream(ctx, conn)         // durable tier

srv := engine.NewServer(drv, registry)
_ = srv.Start(ctx)

cli, err := engine.NewClient(ctx, drv)
```

`New` does not own the connection it is given, so `Close` leaves it open. The driver built by the
registry factory dialled its own connection and does close it.

## Addressing

| | |
| --- | --- |
| `Address("/pkg.Svc/Method")` | `rpc.pkg.Svc.Method` |
| `ServiceWildcard("pkg.Svc")` | `rpc.pkg.Svc.*` |
| `ReplyAddress()` | one `nats.NewInbox()` per driver |

`WithPrefix("acme")` replaces the `rpc` root — two deployments sharing a cluster use it to stay out
of each other's subject space.

## Config keys

Registered as `"nats"`, so `interchange.NewDriver("nats", cfg)` builds it from `interchange.yaml`.

| key | default | |
| --- | --- | --- |
| `url` | `nats://127.0.0.1:4222` | connection URL |
| `prefix` | `rpc` | first subject token |
| `jetstream` | `false` | `"true"` selects the durable tier |
| `stream` | prefix, upper-cased | JetStream stream name |

## Durability tier

Core NATS is the default and is what request/reply should use: a reply is worthless after the
caller's deadline, so persisting it buys nothing. `NewJetStream` publishes requests into a stream
and serves them from durable consumers, for traffic that has to survive a restart — it declares
`AtLeastOnce: true` (the engine turns on replay suppression) and `NativeReply: false`, because
JetStream does not store the publisher's reply subject, so the return address travels in the
envelope and the reply goes back over core NATS.

The ack is the engine's, through `Inbound.Done`: a message is acked once the call has been handled
and its reply sent, and naked with a one-second delay otherwise, so a handler that dies half way
through leaves work for redelivery rather than losing it. Consumers cap redelivery at five
attempts — a message the engine can never handle is a busy loop, not durability. Core NATS has no
acknowledgement to give and leaves `Done` nil.

## Capabilities

| | core | JetStream |
| --- | --- | --- |
| `NativeHeaders` | yes | yes |
| `NativeReply` | yes | no |
| `CompetingGroup` | yes (queue group) | yes (shared durable consumer) |
| `OrderedPerKey` | yes | yes |
| `MaxPayload` | negotiated `max_payload` less 1 KiB of header headroom | same |
| `AtLeastOnce` | no | yes (acked on completion, not on delivery) |

## Tests

`go test ./...` starts a NATS server, JetStream included, inside the test binary. No installed
`nats-server`, no docker, nothing off localhost.
