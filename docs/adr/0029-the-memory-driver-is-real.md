# ADR-0029 — The in-process driver is a real driver, not a mock

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

The engine needs to be testable without a broker, `ix dev` needs something to run before anyone has
installed NATS, and both of those are usually solved with a mock — a stub that implements enough of
the interface to make the tests pass.

A mock is the wrong shape here for a specific reason. The claim this design makes is that all
per-transport variation is data (ADR-0022), and a mock cannot falsify that claim, because a mock is
written to satisfy whatever the engine currently does. It agrees by construction. Worse, a mock
that is *almost* a driver becomes the place engine assumptions hide: the tests pass, the seam looks
clean, and the first real broker discovers three years of drift.

## Decision

`driver/memory` is a real driver. Same six `Driver` methods, a real `Capabilities` value, real
subject matching (NATS-style: `*` is one token, `>` is the rest — the grammar every driver in the
box uses, which is why one address scheme survives three brokers), real competing-consumer groups,
and no concrete message type anywhere in it (ADR-0024). It runs the whole `drivertest` conformance
suite, unmodified, exactly as NATS, MQTT and WebSocket do.

It has three jobs. It is how the engine is tested brokerless; it is what `ix dev` runs; and it is
**the fourth driver that falsifies the seam** — if the engine ever needs to know which transport it
is on, this driver is where that shows up first, because it is the one with no broker behind it to
blame.

Two options exist for that third job. `WithCapabilities` overrides what the driver declares, so the
same code can present itself as a transport with no headers, no native reply, a payload ceiling and
at-least-once delivery. `WithDuplicateDelivery` makes every publish arrive twice, which is what an
at-least-once transport does on a bad day — replay suppression is tested without waiting for a
broker to misbehave.

## Consequences

`go test ./...` needs no broker, no docker and nothing off localhost, so the conformance suite is
cheap enough to run on every commit. `WithCapabilities` is what makes the degraded conformance run
possible, and that run is the strongest single piece of evidence for ADR-0022: one driver, one
suite, two `Capabilities` values, same result.

The costs: it is a driver to maintain, with real concurrency and a real matcher, rather than twenty
lines of stub. Being in-process, it cannot exercise anything about a network — reordering, partial
writes, a broker that goes away mid-call — so passing here is necessary and not sufficient, and the
real drivers each keep broker-exercising tests of their own. Its competing-consumer pick is
deterministic (lowest subscription id) rather than fair, which is right for a test driver and would
be wrong for anything else. And because it lives in the core module rather than in its own
(ADR-0023), it is one of the few things that could pull a dependency into core — which is why it
has none beyond core itself.

## Alternatives

**A mock or a fake in `internal/`.** Cannot falsify the seam, and hides engine assumptions where no
conformance suite will find them.

**Test the engine against NATS only.** Needs a broker in CI, makes the suite slow, and couples
every engine test to one client library's behaviour.

**Skip the in-process driver and make `ix dev` require a broker.** Turns the first five minutes with
the framework into an infrastructure task.

## Evidence

- `driver/memory/memory.go` — the package doc states the decision and its three reasons; the six
  methods, `DefaultCapabilities`, `WithCapabilities`, `WithDuplicateDelivery`, and `match`.
  `var _ interchange.Driver = (*Driver)(nil)`.
- `driver/memory/memory_test.go` — `TestConformance` and `TestConformanceDegraded`: the same suite
  twice, one `Capabilities` value apart.
- `ix/internal/devsrv/devsrv.go` and `ix/internal/cmd/dev.go` — `ix dev` runs over it, and says so
  in its own help text: "a real driver rather than a mock".
- `ix/internal/address/address.go` — "This is `driver/memory`'s `Address`, which is the reference
  implementation."
- `internal/conformance/engine_test.go` — `TestMetadataFallback`, `TestChunking`,
  `TestDeadlineCrossesTheWire`, `TestExpiredDeadlineIsNotDispatched`: the engine's capability
  behaviour, tested brokerless over this driver.
- `errors/foursurfaces_test.go` and `auth/e2e_test.go` — the modules' both-roads tests use it as
  their bus.
