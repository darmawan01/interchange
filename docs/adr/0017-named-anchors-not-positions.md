# ADR-0017 — Named anchors, not positional ordering

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

An interceptor chain is a list, and the obvious API for extending a list is an index: insert at 2,
replace 0. That works until somebody upstream inserts a stage — a new telemetry wrapper, an
idempotency stage in a shared composition root — and every index below it means something else.
Nothing errors. The chain still builds, the service still serves, and a deployment that meant "run
my tenant check after the deadline stage" now runs it after telemetry, one position too early,
where the deadline has not been applied yet.

That is the worst shape a failure can have: silent, correct-looking, and only visible in the
behaviour of a check that is now in the wrong place.

## Decision

Stages are named, and every combinator refers to a name. `Chain(...)` takes `Stage` values built by
`Named(name, interceptor)`; `After`, `Before` and `Replace` take an anchor string; `Remove`,
`Append` and `Prepend` complete the set. Core's three stage names are exported constants
(`StageTelemetry`, `StageRecover`, `StageDeadline`) and are therefore API — as are `/auth`'s
`StageAuthn` and `StageAuthz`.

A `ChainSpec` is **immutable**: every combinator returns a new value, so handing the same chain to
two call sites cannot let one mutate the other's. Errors accumulate rather than panic — a missing
anchor, a duplicate name, a nil interceptor or an unnamed stage sets `ChainSpec.err`, which
`Register` returns and `Wrap` refuses to proceed past. Failure is loud, and it happens at wiring
time, before the process serves anything.

## Consequences

Inserting a stage upstream cannot silently reorder a downstream chain; at worst it collides on a
name, which is an error naming the stage. Deleting or renaming a stage breaks every chain that
anchored to it — at startup, with the anchor named in the message, rather than in production.
`ChainSpec.Names()` is a readable list a test can compare across roads (ADR-0015) and `ix describe`
can print.

The costs: names are now a compatibility surface. Renaming `deadline` is a breaking change for
every adopter who anchored to it, which is why the constants exist rather than bare literals.
Uniqueness is enforced, so the same interceptor cannot appear twice under one name — running a
stage twice deliberately means naming the second instance something else. And an anchor error is
only discovered when `Register` is called, not at compile time; a chain built in a `var` block and
never registered carries its error silently.

## Alternatives

**Positional indices.** Rejected in `docs/08-decisions.md`: "positional chains break silently when a
stage is inserted upstream". The failure mode is the argument.

**Priority numbers.** Trades one arithmetic problem for another — everyone picks 100, then 50, then
25 — and gives no name to anchor against when a stage has to sit between two specific others.

**Panic on a bad anchor instead of accumulating an error.** Loud, but it makes chain construction
un-testable and un-composable: a helper that builds a chain cannot report a problem to its caller.
`MustWrap` exists for wiring code that would panic on the next line anyway.

## Evidence

- `chain.go` — `Stage`, `Named`, the combinators, `validateStages`, and the immutability of
  `ChainSpec.with`. `Wrap` returns the accumulated error rather than a chain that is not the one you
  asked for; `dispatch.go` `Register` surfaces it as `interchange: Register(%s): %w`.
- `chain_test.go`:
  - `TestChainOrderIsOutermostFirst` — stage 0 is outermost.
  - `TestChainNamedAnchors` — `After`/`Before`/`Replace` produce the expected order.
  - `TestChainMissingAnchorIsLoud` — a missing anchor errors and `Wrap` refuses.
  - `TestChainRejectsDuplicateAndNilStages`.
  - `TestChainIsImmutable` — deriving does not mutate the base.
  - `TestEmptyChainIsValid`.
- `auth/authz_test.go` `TestChainIsOrdinary` — the auth module's own stages are removable by name,
  which is the property this decision buys an outside module.
- `docs/08-decisions.md`, "Interceptor ordering | positional / named anchors | **Named**".
