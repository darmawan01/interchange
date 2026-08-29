# ADR-0018 — Core ships three interceptors and no more

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

Once a chain exists, the pressure is to fill it. Every framework's stock middleware list grows the
same way: authentication, authorization, validation, rate limiting, idempotency, error mapping,
tracing — each obviously useful, each pulling a dependency and an opinion into the layer everybody
must import. The end state is a core that knows what a permission is, what a violation looks like,
and which reason strings exist, and that cannot be adopted by anyone who already has answers to
those questions.

There is also a cheaper failure available: shipping the extension point but keeping a private door
for the stock stages, so the "pluggable" chain is pluggable for everything except the parts that
matter.

## Decision

Core ships `telemetry`, `recover` and `deadline`, and nothing else. Those three are properties of
**dispatch** rather than of any security or business model:

- `telemetry` labels observations by procedure — the same label on every road, because the
  procedure string is the same on every road (ADR-0009).
- `recover` turns a handler panic into an error. It matters more here than behind an HTTP server:
  on a bus a dropped connection takes the subscriber with it.
- `deadline` enforces the envelope's `deadline_unix_ms`, which is the one thing HTTP gives free and
  a bus does not. It also rejects a call whose deadline passed in flight, so a redelivered message
  does not run work nobody is waiting for.

Everything else is an ordinary module with no privileged access — `/auth` (ADR-0019), `/errors`
(ADR-0021), `/validate` — reached through the same `Named` stage API any adopter uses. That the
stock modules get no special seam is the test of whether the extension point is real.

Core also ships **no OpenTelemetry dependency**. Telemetry goes through an `Observer` seam —
`ObserveCall(ctx, procedure) (context.Context, func(err error))` — with a `SlogObserver` for
development and an adapter you write for anything that matters. `hack/depcheck.sh` would fail if
core grew the dependency.

## Consequences

Core's import graph stays at protobuf plus connect, which is what makes the dependency rule
checkable rather than aspirational. An adopter installs the opinions they want and none of the
others; the modules can version independently of core.

The costs are real. A newcomer gets less out of the box than a batteries-included framework offers,
and the composition root is theirs to write — `examples/catalog/wire.go` exists partly because
somebody has to show what a wired chain looks like. `Config.Observer` being nil means silence, so a
service that forgot to wire an observer emits nothing and looks healthy. And the `Observer`
interface is a smaller surface than OpenTelemetry's: an adapter maps onto it, which loses anything
the seam does not carry.

## Alternatives

**Ship an OpenTelemetry interceptor in core.** One dependency, and then core has a stake in a
tracing library's release cadence. The seam costs an interface and an adapter, and keeps
`depcheck` honest.

**Ship authz in core, off by default.** "Off by default" still means core imports a policy model,
still means the annotation lives in core's band, and still makes core the place a permission bug is
fixed. `docs/08-decisions.md` settles it the other way: core owns chain symmetry, not authorization.

**Ship nothing at all.** Then every adopter reimplements panic recovery and deadline enforcement,
and the bus ones will be subtly wrong, because a deadline that already expired in a queue is not an
obvious case.

## Evidence

- `interceptors.go` — `Telemetry`, `Recover`, `Deadline`, `DefaultChain`, the `Observer` interface,
  `SlogObserver`, and the stage-name constants.
- `hack/depcheck.sh` — core's allowlist is protobuf and connect; nothing else may enter the graph.
- `docs/06-crosscutting.md` — the stock-interceptor table, marking which three are core and which
  belong to modules.
- `auth/authz_test.go` `TestChainIsOrdinary` — a module's stages sit beside core's three under the
  same rules: `{telemetry, recover, deadline, authn, authz}`, and `authz` is removable by name.
- `errors/README.md` — the `/errors` stage installs with
  `After(interchange.StageTelemetry, errors.Stage(...))`, using the same anchors an adopter has.
