# ADR-0030 — The conformance suite is public API

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

Adding a transport is the extension point with the most surface and the least supervision. Four
drivers ship in this repository and each was reviewed by whoever wrote the engine; the fifth will be
written by somebody who has never read `engine/server.go`, against a broker nobody here has run,
and the only thing standing between their `Capabilities` value and a production incident is whether
their tests happened to cover the case where a reply arrives before its subscription is live.

The usual arrangement is an internal test helper: the drivers in the box are held to a bar, and
everyone else is held to their own judgement. That makes the seam nominally public and actually
private — the interface is exported, the definition of "correct" is not.

## Decision

`drivertest` is a public package, exported from the core module, and it is the definition of a
correct driver. A third party runs the same suite the in-box drivers run:

```go
drivertest.Run(t, func(t *testing.T) drivertest.Pair {
    return drivertest.Pair{Server: srv, Client: cli}
})
```

Nine groups — `Capabilities`, `Unary`, `Error`, `Metadata`, `Deadline`, `UnknownProcedure`,
`Concurrent`, `LargePayload`, `Addressing` — and every one of them is really the same question:
*does the engine still work when this capability is absent?* The suite supplies its own service, so
a driver that needed a concrete message type would have nowhere to get one (ADR-0024). A driver that
passes needs no broker-specific tests to be trusted by the engine.

The corollary is in `CONTRIBUTING.md` and is the rule that matters most for anyone adding a broker:
**if your driver needs a change to the engine, that is a finding to report, not a patch to make.**

## Consequences

The bar is the same for everyone, and it is executable rather than described. It also gives the
engine a place to encode hard-won behaviour once — the suite retries a warm-up call because a broker
subscription is not always live the instant `Subscribe` returns, which is a lesson every driver
author would otherwise learn separately, as a flaky test.

The costs are the ones every public API carries. The suite is now a compatibility surface: adding a
test to it can break a third-party driver's build, so tightening the definition of correct is a
breaking change that needs the same care as changing `Driver` itself. It also constrains the engine
— `drivertest` asserting something means the engine has promised it.

**And a public suite still has to be right.** This decision surfaced its own defect. `drivertest`
could only be run by a driver declaring `TRANSPORT_BUS`: `internal/testsvc` annotated its methods
for RPC, REST and BUS only, and `Run` passed no `engine.Expose`, so an honest `TRANSPORT_MQTT`
driver failed `Start` with *"no procedure is exposed on TRANSPORT_MQTT"*. The MQTT driver found it —
the first driver written that was not a bus. A conformance suite that works for one transport is not
a conformance suite. `Run` now passes `engine.Expose(pair.Server.Caps().Transport)` and the fixture
declares every road, which as a side effect makes the suite assert that `Capabilities.Transport`
actually routes.

That defect is the argument for the decision rather than against it: it existed because only
bus-shaped drivers had ever run the suite, and it was found the first time something else did.

## Alternatives

**An internal helper.** In-box drivers held to a bar, third parties to their own judgement — which
is the failure this ADR is about.

**A written checklist in the contributing guide.** Nobody's CI runs a checklist.

**Certify drivers by review.** Does not scale past the people who wrote the engine, and review does
not catch a payload ceiling that is wrong by a kilobyte.

## Evidence

- `drivertest/drivertest.go` — the package doc states the decision; `Run`, `Pair`, `Factory`,
  `start` (with the warm-up retry and the comment on why `Start` failing here is the point), and
  `isSingleChannel`, which is how the suite accommodates a constant `Address` without weakening
  `Address != ServiceWildcard`.
- `driver/memory/memory_test.go`, `driver/nats/nats_test.go` (`TestConformance`,
  `TestConformanceJetStream`), `driver/mqtt/mqtt_test.go`, `driver/ws/ws_test.go` — four drivers,
  one suite.
- `driver/mqtt/README.md`, "Seam findings" #1 — the defect, as the driver that found it recorded it.
- `4a6728e` — the fix ("A conformance suite that works for one transport is not a conformance
  suite").
- `CONTRIBUTING.md` — the eight gates, and the report-don't-patch rule.
