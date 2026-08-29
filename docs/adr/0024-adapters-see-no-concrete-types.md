# ADR-0024 — A binding adapter may not import a concrete message type

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

The moment a transport adapter imports `catalogv1`, it stops being an adapter. It now knows what a
`ListProvidersRequest` is, which means it can validate one, transform one, or route on one — and
every such capability is a second implementation of the API, living next to the first, free to
disagree with it. The next service needs its own copy of the driver, and the driver's author starts
answering questions that belong to the handler.

The pressure is real and reasonable. A driver that could see the message could set a partition key
from a field, or reject an obviously malformed body before dispatch, or log something useful. Each
is a small win and each one welds the adapter to one service's types.

## Decision

A driver sees **procedure strings, bytes and metadata**, and nothing else. The `Driver` interface
takes and returns `[]byte`; `Inbound.Body` is opaque; `Publish` is given a body it may not parse.
The engine owns everything that requires understanding the payload — framing, chunking,
correlation, deadlines, replay suppression — so the driver has no reason to look.

The same rule holds for bindings. `binding/rpc` serves every method with **one** generic Connect
handler: the concrete types come from `MethodDesc.NewRequest`/`NewResponse` off the descriptor, and
the type parameter (`anyMessage`) carries no information because it does not have to. That is what
"the HTTP binding is generated, nothing to write" means in practice — there is no per-service
handler file.

The permitted exception is `transportv1`: the `Transport` enum and the envelope messages the engine
and every driver share. That is the transport contract itself, not a service's API. The distinction
is exact and the `driver/ws` README records it under "Not a finding": importing `transportv1` for
the enum is fine; importing a generated *service* message type is the violation.

The rule also reaches the modules. `/auth`'s tenant scoping runs in front of every service in a
process and can therefore import none of their generated packages — it finds the tenant by
reflection, by convention or by annotation, never by a concrete type.

## Consequences

One driver serves every service, forever. A driver is small enough to review — the NATS driver is
172 lines of code, the WebSocket driver 84 — and if a driver is much bigger than that, engine
responsibilities have leaked into it, which is a signal rather than a style preference.

The costs. A driver **cannot introspect a payload**: no partition key derived from a field, no
content-aware routing, no early rejection of a malformed body. That is the same cost `bytes` over
`Any` accepts at the envelope level (ADR-0008), paid again here. Anything a driver would have
wanted from the message has to be lifted into metadata by the engine first — which is exactly what
happened when the MQTT driver could not reach the correlation id to populate MQTT 5 Correlation
Data, and the engine had to surface it as `interchange.MetaCorrelationID` in the header map.

## Alternatives

**Let a driver decode the envelope.** Then every driver has to agree with the engine's framing
forever (ADR-0011, ADR-0012), and a framing change is a coordinated release across every adapter.
The WebSocket driver *did* parse envelopes for a while, as a workaround for a missing seam, and the
81 lines that deleted themselves when the seam was fixed are the argument (ADR-0032).

**Generate a driver per service.** Restores the second implementation, multiplied by the number of
services.

## Evidence

- `driver.go` — the `Driver` interface's doc comment states the rule and its reason: "It may not
  import a single concrete message type: the moment it does, it has stopped being an adapter and
  become a second implementation of the API."
- `BUILD-PLAN.md`, phase 4 exit criteria — "The NATS driver imports no concrete message type. —
  review, and `drivertest` cannot pass without it."
- **`drivertest` cannot be passed by a driver that needs one.** The suite supplies its own service
  (`internal/testsvc`, a core-internal package a driver's module cannot import), registers it, and
  hands the driver nothing but a `Pair` of `interchange.Driver` values. A driver that required a
  concrete type would have nowhere to get one. `drivertest/drivertest.go`, `Run` and `start`.
- `binding/rpc/rpc.go` — `anyMessage` and the per-method codec bound to `md.NewRequest`; one
  handler, every method.
- `driver/ws/README.md`, "Not a finding" — the `transportv1` exception, stated precisely.
- `auth/tenant.go` and `auth/README.md`, "Tenant scoping" — the same rule applied to a module.
