# ADR-0034 — `protoc-gen-bus` emits no subscribers

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

§07 was written before the plugin was. Its pipeline diagram listed "bus subscribers + clients"
among the outputs of `protoc-gen-bus`, and that was the obvious shape: the `(transports)`
annotation says which roads an RPC travels, so generate the code that subscribes to them.

Building it made the problem visible. The message engine does not need generated subscriptions. It
already walks the registry: `engine.Server.Plan` iterates the registered `ServiceDesc` values,
filters methods by the transports they declare, and emits one wildcard subscription per service
whose methods share a queue group or one per procedure where they do not. The `ServiceDesc` the
plugin emits *is* the subscriber. Generating a subscription beside it would put two readers on the
same annotation, and two readers of one annotation can disagree — the generated one is frozen at
build time, the engine's is computed at start-up from whatever is actually in the registry, and
nothing would reconcile them.

## Decision

`protoc-gen-bus` emits the `ServiceDesc`, the server interface, a registration helper and typed bus
clients, and no subscription code at all. Fan-out is derived at run time from the registry by
`engine.Server.Plan`. The divergence from §07 was flagged rather than implemented, and the document
was amended in the same commit that shipped the plugin.

## Consequences

There is exactly one answer to "what does this service subscribe to", it is computed from the same
`MethodDesc` values that dispatch uses, and `ix describe` and the driver conformance tests both
read it. A service that mixes queue groups subscribes per procedure without anyone regenerating
anything, because that decision is made where the groups are actually known.

`Plan` also deduplicates, which a generated subscriber would have had to learn separately: on a
single-channel transport such as WebSocket (ADR-0028) every procedure resolves to the same address,
and subscribing twice runs the handler twice for one message.

The cost is that subscription behaviour is not visible in a diff the way generated code is. A
change to `Plan`'s grouping rule changes the fan-out of every service at once with no generated
file moving, so `ix verify` cannot catch it — only the engine's own tests can. It also means a
reader who wants to know what a service subscribes to must run `ix describe` rather than open a
file.

The client half is generated, and deliberately partial: only services with a method on
`TRANSPORT_BUS`, `_MQTT` or `_WS` get a bus client, and only those methods get client methods. A
generated call to a procedure nothing subscribes to is a timeout wearing a confident signature.

## Alternatives

**Generate subscriptions and have the engine consume them.** This is what §07 originally described.
It loses to the two-readers problem: the generated set and the registry-derived set are computed
from the same annotation by different code at different times, and the failure when they disagree
is a silently unserved procedure.

**Generate subscriptions and drop the registry walk.** Rejected: `Plan` needs the exposure
configuration (`cfg.expose`) and the driver's `Capabilities` — whether the transport supports
competing groups at all — neither of which exists at generate time.

## Evidence

- `engine/server.go` — `Server.Plan` (`func (s *Server) Plan()
  []Subscription`), including the deduplication comment and the group/per-procedure split.
- `tools/cmd/protoc-gen-bus/` — the whole plugin; nothing in
  `bus.go` emits a subscription. `emitBusClient` skips methods that do not travel a broker.
- `docs/07-codegen.md` — the amendment, as a blockquote directly
  under the pipeline diagram.
- `tools/README.md` — "It does **not** emit subscribers."
- Commit `c1eea48` records the sequence: the plugin author found the divergence, said so, and
  amended the document rather than building what it described.

See ADR-0022 for why `Capabilities` being data is what lets `Plan` make this decision at run time.
