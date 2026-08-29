# WebSocket driver

One contract, every road — and this is the road with the fewest addresses and the most lifecycle.

The driver itself is the small half: a socket has exactly **one channel**, so `Address()` ignores
the procedure and the procedure travels in the envelope. The interesting half is the shim: the
upgrade, the handshake frame that carries a credential a browser cannot put in a header,
demultiplexing concurrent calls over one pipe, and noticing that the pipe is gone.

| file | what it is |
| --- | --- |
| `ws.go` | the six `interchange.Driver` methods and the wire framing — 84 lines of code |
| `conn.go` | one connection: read loop, connection-scoped metadata, subscription table |
| `server.go` | the `http.Handler` that upgrades and runs an engine server per socket |
| `client.go` | `Dial` |
| `options.go` | the knobs |

## Mounting the handler

```go
reg := interchange.NewRegistry()
reg.Register(catalogv1.ProviderServiceDesc(), impl, interchange.DefaultChain(cfg))

mux := http.NewServeMux()
mux.Handle("/ws", ws.NewServer(reg))
```

`NewServer` accepts each socket, builds a driver for it, and starts one `engine.Server` bound to
that driver — subscribed before the first frame is read, stopped when the socket goes away. Pass
engine options through `ws.WithServerOptions(...)`:

```go
mux.Handle("/ws", ws.NewServer(reg,
	ws.WithOrigins("app.example.com"),
	ws.WithServerOptions(
		engine.WithConcurrency(64),
		engine.WithMaxMessage(4<<20), // chunking otherwise streams an unbounded body in
	),
))
```

If a connection has to do something else as well, `Handler` gives you the socket and leaves the
engine server to you. `setup` runs before the read loop starts — so a subscription it makes cannot
miss a frame — and must not block:

```go
h := ws.Handler(func(c *ws.Conn) error {
	srv := engine.NewServer(c.Driver(), reg)
	if err := srv.Start(c.Context()); err != nil {
		return err
	}
	go func() { <-c.Done(); srv.Stop() }()
	return nil
})
```

## Dialling

```go
d, err := ws.Dial(ctx, "wss://example.com/ws",
	ws.WithHandshakeMetadata(interchange.Metadata{"authorization": "Bearer " + tok}))
if err != nil { ... }
defer d.Close()

cli, err := engine.NewClient(ctx, d, engine.WithTimeout(10*time.Second))
```

There is no client wrapper here and there should not be: the driver implements
`interchange.Watcher`, so `engine.NewClient` already fails every pending call the moment the socket
dies rather than leaving it to wait out its deadline.

A dialled `*Driver` is a full `interchange.Driver`, so a connection can carry a server as well as a
client: `AddressWildcard` matches request frames and not reply frames, and the two do not collide.

## The wire

Two message types on the socket, told apart by the WebSocket frame's own type — no protocol
invented on top of that.

**Text frame — the handshake.** A flat JSON object of metadata, valid only as the *first* message
on the connection. Keys are canonicalised to lower case, as they are everywhere else in
Interchange. A late handshake closes the connection with a policy violation: credentials arriving
mid-stream would apply to calls already dispatched under the old ones.

**Binary frame — a call.** A uvarint address length, the address, then the engine's opaque body:

```js
const enc = new TextEncoder();
function frame(addr, body) {              // addr is "ws.rpc" for a request, "ws" for a reply
	const a = enc.encode(addr);
	const out = new Uint8Array(1 + a.length + body.length);
	out[0] = a.length;                    // a uvarint; one byte while the address is this short
	out.set(a, 1);
	out.set(body, 1 + a.length);
	return out;
}

sock.send(JSON.stringify({ authorization: "Bearer " + token })); // once, first
sock.send(frame("ws.rpc", encodedRequest));                      // every call after
```

## One channel, and what follows from it

```go
d.Address("/catalog.v1.ProviderService/Get")  // "ws.rpc"
d.Address("/catalog.v1.ProviderService/List") // "ws.rpc" -- the same channel
d.ServiceWildcard("catalog.v1.ProviderService") // "ws.*"
d.ReplyAddress()                                // "ws" -- the socket itself
```

Three consequences, all of them the design working rather than a wrinkle:

1. **The procedure lives entirely in the envelope.** Routing is `Request.procedure`, read by the
   engine, not by a subject match. `drivertest` recognises this case and permits `Address` to be
   constant.
2. **`ReplyAddress` is a package constant, not per-connection.** `NativeReply` is true, so a
   responder is never told where to answer; on a socket there is exactly one place an answer can
   go, and both ends have to name it the same thing.
3. **A wildcard is still distinct from an address.** The engine subscribes `ws.*` and publishes
   requests to `ws.rpc` and replies to `ws`, so one socket can carry a server and a client without
   either seeing the other's traffic. `drivertest` requires `Address != ServiceWildcard`
   unconditionally; two distinct constants satisfy both it and the one-channel model.

### Capabilities

| | value | why |
| --- | --- | --- |
| `NativeHeaders` | **false** | a WebSocket frame has no headers; the engine folds metadata into the envelope |
| `NativeReply` | **true** | the reply goes back on the same socket |
| `CompetingGroup` | false | a socket has one consumer; load balancing is sticky by connection |
| `MaxPayload` | 512 KiB | anything larger is chunked by the engine, not rejected here |
| `AtLeastOnce` | false | one delivery, and `Inbound.Done` stays nil — a socket has no acknowledgement |

The driver also implements `interchange.Closer` (the engine closes the socket on shutdown) and
`interchange.Watcher` (the engine fails pending calls when the socket dies).

## Where the handshake merge happens, and why it is not a chain modification

A browser cannot set `Authorization` on an upgrade. So the first frame on the socket may carry
metadata, which becomes **connection-scoped**: it is merged into every subsequent call on that
connection. `ws.WithRequestMetadata` feeds the same map from the upgrade request instead — a query
parameter, a cookie, a subprotocol entry — for clients that would rather not spend a frame.

The merge is one line in `Conn.dispatch`: the connection's metadata becomes `Inbound.Header` on
every frame, and the engine folds it into the envelope's metadata for the call. It happens:

- **below dispatch**, in the read loop, before the frame reaches any subscription and before the
  engine has an `Envelope` at all;
- through **the field that exists for it** — `Inbound.Header` is "what this transport knows that
  the envelope does not", and a socket holding a handshake credential knows exactly that;
- with **per-call values winning**, because the engine merges the envelope's own metadata second.
  A call that names `authorization` means that `authorization`.

It is not a chain modification, and the distinction is exact: nothing is added to, removed from,
skipped in or reordered around the interceptor chain. `Registry.Dispatch` still runs the one chain
the registry holds, over an envelope whose metadata now contains a credential — precisely as it
would if the credential had arrived in an HTTP header on a Connect request. Interceptors cannot
tell the two apart, which is the whole claim.

Chunked requests are covered too. The credential rides beside the envelope rather than inside it,
so a request split into `Frame`s carries it on every frame; the engine merges the header of the
frame that completes the message.

## Seam findings

Phase 5 exists to falsify the seam. This driver found three things, all of them now fixed in core —
recorded here because the fixes are what the seam looks like when it holds.

**1. `Inbound.Header` was unreadable to a transport without native headers, which is the only kind
of transport that needs it.** `engine/server.go serve()` merged `in.Header` only when
`Caps().NativeHeaders` was true. A socket declares false — correctly, a frame has no headers — and
therefore had *no* way to contribute inbound metadata to the chain: not through `Header`, not
through the context (dispatch builds a fresh `context.Background()`), and not through a chain
(rightly forbidden). The driver worked around it by rewriting the request envelope below dispatch,
which cost 38 lines and could not cover a chunked request at all.

The guard is gone: the header is merged whatever the transport claims, a transport without native
headers hands over an empty map and nothing changes for it, and the envelope is still merged second
so a per-call value beats a per-connection one. The workaround is deleted — `conn.go` lost 81 lines
— and the chunked hole closed with it.

**2. No driver could tell the engine the transport was gone.** `engine.Client` failed a pending
call on its own `Close` or on the caller's context, and a dead socket is neither. `interchange.Watcher`
is now the optional counterpart to `Closer` — `interface{ Done() <-chan struct{} }`, watched by
`engine.NewClient` — and `*Driver` implements it. The shim's client wrapper is deleted; the
behaviour belongs to every connection-holding driver, not to this one.

**3. `Server.Plan()` emitted the same subscription twice on a single-channel transport.** When one
service mixes queue groups, `Plan()` falls back to `Address(m.Procedure)` per method — one constant
repeated on a socket, so every frame would be dispatched once per exposed method and the handler
would run N times. `Plan()` now deduplicates by `(pattern, group)`, so the driver's own defence is
gone and delivery is the plain thing again: every subscription whose pattern matches.

**Also resolved during the phase:** `internal/testsvc` originally annotated its methods for RPC,
REST and BUS only, so a driver declaring `TRANSPORT_WS` exposed nothing and `engine.Server.Start`
refused to start under `drivertest`. It now declares MQTT and WS as well, and the suite runs
against the honest capability.

**Not a finding:** the driver imports `transportv1` for the `Transport` enum. That is the transport
envelope every driver and the engine share; no generated service message type appears anywhere in
this package, and since the metadata merge moved to `Inbound.Header` the driver no longer parses
the envelope at all.

## Tests

`go test ./...` — no broker, no docker. `httptest.NewServer` plus a dialled client, which is the
real socket.

- `TestConformance` runs the full `drivertest` suite over a live socket.
- `TestHandshakeCredential` sends a credential **only** in the handshake frame — no client metadata
  anywhere — and asserts an interceptor sees it, then asserts a per-call value overrides it.
- `TestRequestMetadata` does the same for a credential on the upgrade URL.
- `TestHandshakeCredentialSurvivesChunking` puts the same credential on a request too big for one
  frame — the case the envelope-rewriting workaround could not cover.
- `TestConcurrentCalls` puts 40 calls on one pipe and checks every reply reached its own caller.
- `TestLargePayloadChunks` sets the read limit *below* the message, so a payload that crossed the
  socket whole would be rejected: the round trip passing is proof the chunking is real.
- `TestCloseFailsInFlight` kills the socket under a blocked handler and requires the call to fail
  promptly with `unavailable` — the `Watcher` path, end to end.
