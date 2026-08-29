# 04 — Bindings

Five roads — **but not five engines**. The message-oriented transports share one machine, and the
capability matrix below is its input rather than five separate specifications.

## Two families, not five builds

The transports split cleanly in two, and the split is the one that matters: **a channel is an
endpoint**.

**Family 1 — request / response.** HTTP. Correlation, metadata and status are all supplied by the
protocol. Nothing to invent, and the generated code already exists.

**Family 2 — channel / message.** NATS · MQTT · WebSocket. All three are the same shape: a frame
arrives on a named channel, and a reply may go back on another. They differ in the name syntax and
in which conveniences the broker provides — **not in the model**.

```
request / response                channel / message
┌──────────────────────┐   ┌───────────┬───────────┬───────────┐
│ HTTP connect-go      │   │   NATS    │  MQTT 5   │ WebSocket │
│ binding — generated, │   │  driver   │  driver   │  driver   │
│ nothing to write     │   └─────┬─────┴─────┬─────┴─────┬─────┘
└──────────┬───────────┘         └───────────┼───────────┘
           │                                 ▼
           │              ┌──────────────────────────────────────┐
           │              │          message engine              │
           │              │ correlation · deadlines · retries ·  │
           │              │ chunking · stream frames · metadata  │
           │              │ fallback                             │
           │              └──────────────────┬───────────────────┘
           └─────────────────────┬───────────┘
                                 ▼
        dispatch · interceptor chain · your handler
                  identical for both families
```

**So the build is one HTTP binding you do not write, one message engine you write once, and one
thin driver per broker.**

## The driver interface

Everything a broker-specific driver has to supply. **If a driver is much bigger than this, engine
responsibilities have leaked into it.**

```go
type Driver interface {
    // Send one frame to a named channel. Header is dropped by drivers
    // whose transport has no native metadata -- the engine has already
    // folded it into the envelope in that case (see Caps).
    Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error

    // Receive frames matching a pattern. `group` requests competing-consumer
    // delivery where the transport supports it (NATS queue group, MQTT $share).
    Subscribe(ctx context.Context, pattern, group string, fn func(Inbound)) (Unsubscribe, error)

    // The address replies to this client come back on: a NATS inbox, an
    // MQTT response topic, or -- for WebSocket -- the socket itself.
    ReplyAddress() string

    // Address grammar. procedure -> channel name, and the wildcard used
    // to subscribe to a whole service.
    Address(procedure string) string
    ServiceWildcard(service string) string

    Caps() Capabilities
}

type Inbound struct {
    Address string
    Header  map[string]string                    // empty when !Caps.NativeHeaders
    Body    []byte
    Reply   func([]byte, map[string]string) error // nil when !Caps.NativeReply

    // Called once, after the call has been handled and its reply sent.
    // nil when the transport has no acknowledgement. Done(nil) means
    // handled; Done(err) means redeliver if you can.
    Done    func(error)
}

// The engine reads this and degrades gracefully. It is the ONLY place
// per-transport behaviour is allowed to differ.
type Capabilities struct {
    NativeHeaders  bool // NATS yes · MQTT 5 yes · WebSocket no
    NativeReply    bool // NATS inbox · MQTT 5 response topic · WS same socket
    CompetingGroup bool // NATS queue group · MQTT $share · WS no
    OrderedPerKey  bool
    MaxPayload     int  // engine chunks into Frames above this
    AtLeastOnce    bool // engine enables replay suppression via sequence
}
```

## What the engine owns, once

- **Correlation.** A pending-call map keyed by `correlation_id`, with timeout eviction. Written
  once; every driver inherits it.
- **Deadlines.** Turning `deadline_unix_ms` into a server-side context, and cancelling a pending
  call when the client's context dies.
- **Metadata fallback.** When `NativeHeaders` is false, fold the map into the envelope before
  marshalling and lift it back out on receipt — so the interceptor chain never learns which
  transport it is on.
- **Chunking.** Split a payload over `MaxPayload` into sequenced `Frame`s and reassemble. This is
  why the bus payload ceiling stops being a per-service problem.
- **Replay suppression.** When `AtLeastOnce` is set, drop frames whose `sequence` has already been
  seen for that correlation.
- **Streaming.** Frame ordering, `KIND_END` termination, and back-pressure — the same code
  regardless of broker.
- **Dispatch.** Procedure → generated handler, through the interceptor chain.

## What you actually build

| Component | Who writes it | Rough size |
| --- | --- | --- |
| HTTP / RPC binding | generated | nothing to write |
| REST binding | generated + a transcoder library | nothing to write |
| **Message engine** | **you, once** | **the bulk of the work** — everything above |
| NATS driver | you | ~150 lines, *estimated* |
| MQTT 5 driver | you | ~150 lines, *estimated* |
| WebSocket driver | you | ~150 lines, plus a connection-lifecycle shim |
| Per-service binding code | generated | nothing to write, ever |

**WebSocket is the degenerate case, not the hard one.** Its addressing is the simplest of the
three — there is only one channel, so the procedure lives entirely in the envelope and `Address()`
returns a constant. What it adds is the connection lifecycle the brokers handle for you: accepting
the socket, the handshake frame that carries credentials (a browser cannot set an `Authorization`
header on an upgrade), and demultiplexing concurrent calls over one pipe. That is a **shim around
the driver, not extra protocol**.

## Per-transport capability matrix

This is the table the `Caps()` values are drawn from — and the reason the engine can be written
**without a single switch on transport type**.

| | RPC (Connect) | REST | NATS | MQTT | WebSocket |
| --- | --- | --- | --- | --- | --- |
| **Address** | `POST /pkg.Svc/Method` | `GET /v1/providers` | `rpc.pkg.Svc.Method` | `org/rpc/Svc/Method` | one socket, envelope carries it |
| **Metadata** | HTTP headers | HTTP headers | NATS headers | MQTT 5 user properties | envelope map |
| **Correlation** | free (the response) | free | reply inbox | Response Topic + Correlation Data | `correlation_id` |
| **Status** | HTTP code + body | `problem+json` | `Response.code` | `Response.code` | `Response.code` |
| **Deadline** | client context | client context | request timeout | `deadline_unix_ms` | `deadline_unix_ms` |
| **Load balancing** | L7 proxy | L7 proxy | queue group | shared subscription | sticky by connection |
| **Server stream** | native | SSE | — | — | — |
| **Acknowledgement** | the response | the response | JetStream ack | QoS 1 puback | — |

## Acknowledgement is a capability like any other

A broker with explicit acknowledgement makes a distinction the others do not: **delivered** and
**handled** are different events. Acking on delivery buys at-least-once *delivery*, which is not
the thing anyone wants — a handler that crashes half way through loses work the broker already
considers done, and replay suppression cannot conjure a redelivery that was never scheduled.

So `Inbound.Done` is called by the engine after the call has been handled and its reply sent, and
a chunked message holds every frame's acknowledgement until the whole message is handled. A
transport with nothing to acknowledge leaves it nil and pays nothing.

> **NATS core and NATS JetStream are two drivers, not one with a flag.** The durable tier cannot
> preserve the publisher's reply subject — a delivered JetStream message carries the consumer's
> ack subject instead — so it declares `NativeReply: false` and the return address rides in the
> envelope, exactly as it does on a transport that never had one. The capability matrix row above
> is true of the core tier. This is what "the engine adapts, and the driver declares" is for: the
> difference cost one boolean and no engine code.

> ⚠️ **Maturity.** The NATS binding follows a pattern in production, adapted here to the envelope.
> The **MQTT and WebSocket bindings are proposed design**, and the driver line-counts are estimates.
