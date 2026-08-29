# 06 — Adding a transport

Six methods, an honest `Capabilities`, and the conformance suite. If a driver is much bigger than
that, engine responsibilities have leaked into it.

The smallest complete reference is [`driver/memory/memory.go`](../../driver/memory/memory.go) — 242
lines including the in-process broker it talks to. It is a **real driver, not a mock**: it
implements the same six methods, declares `Capabilities` the same way, and imports no concrete
message type ([ADR-0029](../adr/0029-the-memory-driver-is-real.md)). Read it beside this page.

Measured sizes for the drivers in the box: NATS 277 lines, MQTT 261, WebSocket 417 — of which the
driver *proper* is 172 and 84. The difference is connection handshakes, capability negotiation and
config parsing, which no design document accounts for.

## The two rules

**1 · A driver may not import a concrete message type.** A driver sees procedure strings, bytes and
metadata. Never `Provider`, never `ListProvidersRequest`. The moment it imports one it has stopped
being an adapter and become a second implementation of the API
([ADR-0024](../adr/0024-adapters-see-no-concrete-types.md)). This is enforced by review *and* by
`drivertest` — a driver that needs a concrete type cannot pass it.

**2 · If your driver needs a change to the engine, that is a finding to report — not a patch to
make.** Every later driver inherits a seam that was moved to accommodate one broker, and the fix
belongs in the engine where every driver gets it, not in a special case in yours. This is not
theoretical:

> **The engine seam was wrong six times, and each driver found a different one.**
>
> - acknowledging on delivery rather than on completion;
> - a correlation id a driver could not reach;
> - inbound metadata dropped on exactly the transports that needed it;
> - replies acknowledged only on the chunked path;
> - a subscription plan that ran a handler N times on a single-channel transport;
> - a conformance suite that only one transport could run.
>
> That is the argument for building four drivers before calling the seam right — and for treating
> the next one as likely to find a seventh.

A driver much bigger than ~150 lines is usually rule 2 wearing a disguise.

## The interface

```go
type Driver interface {
	// Publish sends one frame to a named channel. hdr is dropped by drivers
	// whose transport has no native metadata -- the engine has already folded
	// it into the envelope in that case (see Caps).
	Publish(ctx context.Context, addr string, body []byte, hdr map[string]string) error

	// Subscribe receives frames matching a pattern. group requests
	// competing-consumer delivery where the transport supports it.
	Subscribe(ctx context.Context, pattern, group string, fn func(Inbound)) (Unsubscribe, error)

	// ReplyAddress is where replies to this client come back: a NATS inbox,
	// an MQTT response topic, or -- for WebSocket -- the socket itself.
	ReplyAddress() string

	// Address maps a procedure to a channel name.
	Address(procedure string) string

	// ServiceWildcard is the pattern that subscribes to a whole service.
	ServiceWildcard(service string) string

	Caps() Capabilities
}
```

### `Address` and `ServiceWildcard`

Every driver in the box uses NATS-style subject grammar — `*` is one token, `>` is the rest — which
is why one address scheme survives three brokers:

```go
// "/pkg.Svc/Method" becomes "rpc.pkg.Svc.Method"
func (d *Driver) Address(procedure string) string {
	return "rpc." + interchange.ServiceOf(procedure) + "." + interchange.MethodOf(procedure)
}

func (d *Driver) ServiceWildcard(service string) string { return "rpc." + service + ".*" }
```

`interchange.ServiceOf` and `MethodOf` split the procedure string for you. If your transport has
one channel and no addressing at all — a WebSocket — return a constant from both and let the
procedure travel in the envelope ([ADR-0028](../adr/0028-websocket-is-one-channel.md)).

### `Publish` and `Subscribe`

`Publish` takes bytes and a flat header map, and nothing else. `Subscribe` hands each frame to `fn`
as an `Inbound`; the returned `Unsubscribe` must be safe to call once. The memory driver's whole
subscribe is a map insert and a closure that deletes the entry.

## `Inbound`

```go
type Inbound struct {
	Address string
	Header  map[string]string
	Body    []byte
	Reply   func(ctx context.Context, body []byte, hdr map[string]string) error
	Done    func(err error)
}
```

**`Header`** is metadata the transport supplied out of band. On a transport with native headers it
is those headers. On one without, it is whatever the driver knows that the envelope does not — a
WebSocket's connection-scoped credential, established by a handshake frame because a browser cannot
set an `Authorization` header on an upgrade. The engine merges it *beneath* the envelope's own
metadata, so a per-call value always wins over a per-connection one
([ADR-0032](../adr/0032-inbound-header-is-always-merged.md)).

**`Reply`** is nil when `Caps().NativeReply` is false; the engine then falls back to publishing to
the address in the envelope's metadata. The context is the engine's and bounds the wait: a driver
whose transport can block behind a silent broker should not have to invent its own deadline, and
must not be able to hang shutdown.

**`Done`**, when non-nil, reports the outcome of this message back to the transport, **exactly
once, after the call has been handled and its reply sent**. Wire it to your broker's ack.

`Done` is the seam that a driver moved. Acking on delivery and acking on completion are different
guarantees, and only the second is worth calling at-least-once: a handler that crashes half way
through a message the broker already considers delivered is work that silently vanishes. Replay
suppression dedupes a redelivery; it cannot conjure one
([ADR-0025](../adr/0025-acknowledge-on-completion.md)).

- `Done(nil)` — handled.
- `Done(err)` — not handled; the driver may redeliver if the transport can.
- A transport with no acknowledgement leaves it **nil**.

## `Capabilities` — a claim the engine is entitled to believe

```go
type Capabilities struct {
	Name           string
	Transport      transportv1.Transport

	NativeHeaders  bool // NATS yes · MQTT 5 yes · WebSocket no
	NativeReply    bool // NATS inbox · MQTT 5 response topic · WS same socket
	CompetingGroup bool // NATS queue group · MQTT $share · WS no
	OrderedPerKey  bool
	MaxPayload     int  // engine chunks into Frames above this
	AtLeastOnce    bool // engine enables replay suppression via sequence
}
```

This is the **only** place per-transport behaviour is allowed to differ, and it is data rather than
a type switch — which is what lets the engine be written without a `switch` on transport type
([ADR-0022](../adr/0022-capabilities-is-data.md)).

**Be honest.** A driver that reports `NativeReply: true` and does not route a reply is *broken*,
not merely degraded. Each field turns a piece of engine machinery on or off:

| Field | What the engine does when it is false / zero |
| --- | --- |
| `NativeHeaders` | folds metadata into the envelope instead of passing `hdr` |
| `NativeReply` | publishes the reply to the address in the envelope's metadata |
| `CompetingGroup` | ignores the `group` argument, and every subscriber gets every message |
| `MaxPayload` | no chunking; above it, the body is split into `Frame`s ([ADR-0012](../adr/0012-chunking-operates-on-framed-bytes.md)) |
| `AtLeastOnce` | no replay suppression; with it, a redelivery replays the cached response ([ADR-0014](../adr/0014-replay-the-cached-response.md)) |

`Transport` is routing metadata: the engine uses it to decide which procedures to subscribe, never
to choose behaviour.

The memory driver's is the honest minimum for an in-process bus:

```go
func DefaultCapabilities() interchange.Capabilities {
	return interchange.Capabilities{
		Name:           "memory",
		Transport:      transportv1.Transport_TRANSPORT_BUS,
		NativeHeaders:  true,
		NativeReply:    true,
		CompetingGroup: true,
		OrderedPerKey:  true,
	}
}
```

It also takes `memory.WithCapabilities(c)`, which is how the suite is run against a transport with
no headers, no native reply, a payload ceiling or at-least-once delivery — the whole point of
`Capabilities` being data.

## Two optional interfaces

```go
// Closer: a driver holding a connection implements it and the engine closes
// it on shutdown.
type Closer interface{ Close() error }

// Watcher: for a driver that holds a connection rather than talking to a
// broker that outlives it. The engine watches Done and fails every pending
// call when the transport is gone.
type Watcher interface{ Done() <-chan struct{} }
```

Implement `Closer` if you own a connection. Implement `Watcher` if the connection can *die* —
without it a client on a dead socket waits out its deadline for a reply that provably will not
arrive. Correct, but a needlessly slow way to learn something the driver already knew. The
WebSocket driver implements both.

## Registering it by name

```go
func init() {
	interchange.RegisterDriver("mqtt", func(cfg map[string]string) (interchange.Driver, error) {
		c := Config{URL: cfg["url"], Prefix: cfg["prefix"], ClientID: cfg["client_id"]}
		// ... parse the rest, rejecting bad values with a real error
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		return Connect(ctx, c)
	})
}
```

That makes the driver available to `interchange.NewDriver(name, cfg)` and to a server that wires
drivers from `interchange.yaml`'s `transports.drivers`. It is optional — the catalog example
constructs its NATS driver directly.

## Running the conformance suite

`drivertest` is **public API**: a third party adding a broker runs the same suite the drivers in the
box run, and a driver that passes it needs no broker-specific tests to be trusted by the engine
([ADR-0030](../adr/0030-drivertest-is-public.md)).

```go
func TestConformance(t *testing.T) {
	drivertest.Run(t, func(t *testing.T) drivertest.Pair {
		// Build a fresh transport for this one test. Register cleanup with t.
		bus := memory.New()
		return drivertest.Pair{
			Server: bus.Driver("server"),
			Client: bus.Driver("client"),
		}
	})
}
```

```go
// Pair is a server-side and a client-side driver attached to the same
// transport. They may be the same value if the driver is symmetric.
type Pair struct {
	Server interchange.Driver
	Client interchange.Driver
}

type Factory func(t *testing.T) Pair
```

Nine subtests. Every one of them is really one question — *does the engine still work when this
capability is absent?*

| Subtest | Asserts |
| --- | --- |
| `Capabilities` | the declared value is internally consistent |
| `Unary` | a request reaches the handler and the response comes back |
| `Error` | a handler error arrives with its code and reason intact |
| `Metadata` | headers survive, natively or folded into the envelope |
| `Deadline` | the caller's deadline bounds the call and the reply |
| `UnknownProcedure` | an unsubscribed procedure fails rather than hangs |
| `Concurrent` | replies are correlated, not merely first-come |
| `LargePayload` | chunking and reassembly across `MaxPayload` |
| `Addressing` | `Address`, `ServiceWildcard` and `ReplyAddress` agree |

The suite is the sixth engine bug's fix, incidentally: an earlier version only one transport could
run, which is worth remembering when a subtest looks like it is asserting something specific to
NATS. If it genuinely cannot apply to your transport, that is a finding about the suite.

**Run it against a real broker, not a fake.** The NATS driver's tests start a real NATS server
inside the test binary; MQTT's start a real broker; the WebSocket tests run over real sockets from
`httptest`. No docker, and no mock of the thing being adapted.

## The checklist

1. Six methods. Nothing else in the type that the engine calls.
2. No concrete message type in the import graph. Bytes, procedure strings, metadata.
3. `Capabilities` you would defend in review. `false` costs you a feature; a wrong `true` costs
   someone a correctness bug.
4. `Inbound.Done` wired to the ack if your transport has one, nil if it does not. Exactly once,
   after the reply.
5. `Closer` if you hold a connection; `Watcher` if it can die.
6. `drivertest.Run` green against the real broker.
7. A README saying what the transport cannot do — the ones in `driver/*/README.md` are the model.
8. Anything you had to change in the engine, written up as a finding.

Next: [07 Bring your own format](07-bring-your-own-format.md).
