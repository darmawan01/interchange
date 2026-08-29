# ADR-0022 — `Capabilities` is data; the engine has no transport switch

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

Five transports differ in ways that matter to a request/response layer. NATS has headers and a
reply inbox; MQTT 5 has user properties and a response topic; a WebSocket frame has neither headers
nor a broker to route a reply; JetStream and QoS 1 redeliver; every broker has a payload ceiling
and they are all different numbers.

The default way to absorb that is a switch — `if transport == NATS { … }` — written once in the
engine and then again in the next place somebody needs it. Every such branch is a place a sixth
transport has to be added, and a place two branches can disagree. Worse, the branches encode
*which broker* rather than *what it can do*, so a driver that is unusual for its family (JetStream,
which is NATS but cannot route a reply) has nowhere honest to sit.

## Decision

A driver **declares**; the engine **adapts**. `Capabilities` is a plain struct of booleans and
numbers — `NativeHeaders`, `NativeReply`, `CompetingGroup`, `OrderedPerKey`, `MaxPayload`,
`AtLeastOnce` — and it is the *only* place per-transport behaviour is allowed to differ. The engine
reads it and degrades:

| Capability absent | What the engine does |
| --- | --- |
| `NativeHeaders` | folds metadata into the envelope; the driver's `hdr` map is ignored |
| `NativeReply` | publishes to the address in `MetaReplyTo` instead of `Inbound.Reply` |
| `CompetingGroup` | subscribes with no group |
| `MaxPayload > 0` | chunks into `Frame`s and reassembles |
| `AtLeastOnce` | enables replay suppression keyed on the correlation id |

The `Driver` interface stays at six methods. Adding a transport is writing those six and telling
the truth in one struct.

**One qualification, stated rather than hidden** (BUILD-PLAN, phase 4): the engine compares
`Caps().Transport` for equality — it is the default value of `engine.Expose`, and
`MethodDesc.ExposedOn` uses it to decide which procedures to subscribe, and to refuse a procedure
that a wildcard subscription delivered but the annotation does not expose. That is **routing, not behaviour**: no
code path branches on *which* transport it is. But it is not literally zero comparisons, and
pretending otherwise helps nobody.

## Consequences

The payoff is measurable. JetStream cannot preserve the publisher's reply subject, so it declares
`NativeReply: false` and the return address rides in the envelope exactly as it does on a transport
that never had one — **no engine code changed** (ADR-0027). Nothing above the driver noticed. That
is the first real evidence the model carries its weight.

The cost is that the capability set is now API. Adding a field is a change every driver has to
consider, and the engine must behave correctly for every combination — including combinations no
broker produces. The WebSocket driver's `NativeHeaders: false` with `NativeReply: true` is exactly
that pairing, and it is why that driver found three engine bugs the two brokers could not
(ADR-0028, ADR-0032). A driver that lies in its `Capabilities` — claiming a reply path it does not
have — fails in the engine rather than in itself, which is a harder failure to read.

## Alternatives

**A switch on transport type.** Every branch is a place the sixth transport is forgotten. It also
makes "is this driver correct?" un-answerable without reading the engine.

**An interface per capability, type-asserted.** Go's version of the same switch, with the branch
hidden in an assertion. A value can be printed, logged and asserted on —
`driver/ws/ws_test.go` `TestOneChannel` checks that a driver's declaration is honest, which is not
something you can assert about a type switch.

**Require every driver to implement everything.** Then a WebSocket driver has to invent headers and
a memory driver has to invent redelivery, and each invention is a private protocol.

## Evidence

- `driver.go` — `Capabilities` and its doc comment ("the ONLY place per-transport behaviour is
  allowed to differ"), and the six-method `Driver` interface.
- `engine/server.go` and `engine/client.go` — every degradation reads `s.caps`; `Transport` appears
  only as the default for `Expose` and in `ExposedOn` checks, both routing.
- `engine/reassemble.go`, `engine/dedupe.go` — chunking and replay suppression, enabled by
  capability rather than by broker.
- **`driver/memory/memory_test.go`** — `TestConformance` runs the whole `drivertest` suite, and
  `TestConformanceDegraded` runs the *same suite over the same driver* with `NativeHeaders: false`,
  `NativeReply: false`, `AtLeastOnce: true` and `MaxPayload: 4096`. Same suite, same result, one
  `Capabilities` value apart — which is what "all variation comes from `Caps()`" has to mean to be
  worth claiming.
- `driver/nats/nats.go` — `NativeReply: d.stream == nil`, `AtLeastOnce: d.stream != nil`: the two
  tiers differ by one boolean each.
- `BUILD-PLAN.md`, phase 4 exit criteria — the criterion and its qualification.
- `docs/04-bindings.md` — the capability matrix the `Caps()` values are drawn from.
