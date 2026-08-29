# 03 — The envelope

HTTP gives you four things for free: a method name in the path, metadata in headers,
request/response correlation, and a status code. **A message bus gives you none of them.** The
envelope makes all four explicit so every transport has the same capabilities.

This is the core of the design — five of the seven gaps from [§00](00-problem.md) land here.

```protobuf
// api/platform/transport/v1/envelope.proto
syntax = "proto3";
package platform.transport.v1;

// One request, on any transport.
message Request {
  // "/platform.catalog.v1.CatalogService/ListProviders"
  // Deliberately IDENTICAL to the Connect procedure string, so one
  // interceptor chain and one dispatch table serve every binding.
  string procedure = 1;

  // Opaque metadata: credentials, trace context, tenant hint, idempotency
  // key. Mirrors http.Header. Bindings map this to native headers where
  // the transport has them and carry it inline where it does not.
  map<string, string> metadata = 2;

  // The marshalled request message. bytes, not Any: `procedure` already
  // determines the type, and Any costs a type-URL round trip.
  bytes payload = 3;

  // "proto" or "json". Lets a browser-facing binding stay human-readable
  // while service-to-service traffic stays binary.
  string codec = 4;

  // Free in HTTP, mandatory everywhere else.
  string correlation_id = 5;

  // What a client context.Context gives you for free over HTTP.
  // The server MUST derive its handler context from this.
  int64 deadline_unix_ms = 6;
}

message Response {
  string correlation_id = 1;

  // The RPC status code -- NOT an HTTP status. HTTP bindings project
  // it onto one; other bindings carry it as-is.
  int32 code = 2;

  string message = 3;

  // Machine-readable reason from the closed enum (§06).
  string reason = 4;

  map<string, string> metadata = 5;
  bytes payload = 6;
}

// Streaming and async fan-out share one shape.
message Frame {
  string correlation_id = 1;
  enum Kind { KIND_UNSPECIFIED = 0; KIND_MESSAGE = 1; KIND_END = 2; KIND_ERROR = 3; }
  Kind kind = 2;
  uint64 sequence = 3;   // monotonic per stream; lets a receiver drop replays
  bytes payload = 4;
  Response status = 5;   // set only on KIND_END / KIND_ERROR
}
```

## Three deliberate choices

**`procedure`, verbatim.** Reusing the Connect procedure string rather than inventing an identifier
means the authorization interceptor, the metrics labels and the trace span names are the same string
on every transport. Dashboards do not need a per-transport view.

**`bytes`, not `Any`.** `Any` embeds a type URL on every message and forces a registry lookup to
unmarshal. The procedure already names the type — the generated dispatch knows it statically.

**A sequence number on frames.** At-least-once transports redeliver. A monotonic per-stream sequence
lets a receiver discard a replay without the handler ever seeing it. This is the single field that
makes a bus binding safe to run at QoS 1.

## How the three messages share a channel

The envelope shapes above are fixed, and none of them is self-describing. On a transport with no
headers a receiver still has to tell a whole `Request` from one chunk of a large one — so the
engine prefixes a **one-byte discriminator** outside the protobuf:

```
  ┌──────┬────────────────────────────┐
  │ kind │ marshalled envelope        │
  └──────┴────────────────────────────┘
    0x01   Request
    0x02   Response
    0x03   Frame
```

That is the entire wire format the message engine owns. It is documented here because any
non-Go implementation of a driver has to agree with it.

**Chunking works on the framed bytes, not the message.** A payload over the driver's `MaxPayload`
is split into `Frame`s whose payloads concatenate back into `[kind][envelope]` — so the receiver
unframes the reassembly exactly as if it had arrived whole, and nothing above the engine has a
chunked code path. The terminating `KIND_END` frame carries the chunk count in `sequence`.

---

> **The envelope is not a new wire format.** Over HTTP it is never serialized — the Connect binding
> fills the same fields from the URL path, the headers and the body. It is the shared **vocabulary**
> that every binding populates, so the layers above it can be written once.

> ⚠️ **Maturity.** The envelope is *proposed design*. It should be prototyped against one real
> service before phase 4 is committed to.
