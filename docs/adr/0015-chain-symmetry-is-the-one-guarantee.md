# ADR-0015 — Chain symmetry is core's only behavioural invariant

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

A framework that fans one contract out onto five transports has to answer what, exactly, it
promises about the roads being the same. The tempting answer is "everything": one authorization
model, one validation model, one error taxonomy, all mandatory, all in core. That answer forces
core to know what a permission is, what a violation is, and what a reason string means — three
opinions that belong to a product, not to a dispatch layer, and each of which makes core
un-adoptable by anyone who already has their own.

The narrow answer is worth more, because the failure this project exists to remove is not "no
authorization". It is **authorization enforced on one road and forgotten on another** — a check
written into an HTTP handler and silently absent from the bus subscriber, discovered in an
incident.

## Decision

Core promises exactly one behavioural thing: **whatever interceptor chain you configure runs
identically on every transport**, in the same order, over the same envelope. That is the whole
guarantee. Core ships `Interceptor`, `Stage`, `ChainSpec` and `Registry.Dispatch`, and it takes no
position on what any stage does. Authorization, validation, rate limiting, idempotency and the
error taxonomy are ordinary modules with no privileged access (ADR-0018, ADR-0019, ADR-0021), and
an empty chain is valid.

The guarantee is stated for consistency, not for safety, and the distinction is the point: it says
the same stages run everywhere, not that any particular stage exists.

## Consequences

The classic multi-transport failure becomes structurally impossible rather than merely discouraged
— one chain, one dispatch, no second place to enforce anything (ADR-0016 is how). Core needs no
concept of a user, a tenant or a policy, so an adopter with an existing authorization system
installs none of ours and loses nothing.

The cost, recorded in §08 and worth repeating here: **chain symmetry guarantees consistency, not
safety.** An adopter can ship a multi-transport service with an empty chain and no authorization at
all, and every gate in this repo will pass. `interchange.Chain()` is a supported configuration and
`TestCoreServesWithoutTheModule` asserts it works. Whether `ix lint` should warn about it is still
an open question in §08 — a warning is not a mandate, and it may be exactly the opinion a framework
should not have.

The second cost is smaller and real: because the guarantee is about traversal and not about
outcome, "the chain ran" is not "the chain decided correctly". A stage that reads transport-specific
metadata can still behave differently on two roads. Nothing here prevents that; it only guarantees
the stage got the chance.

## Alternatives

**Mandatory authz in core.** Loses every adopter with a gateway or an existing policy engine, and
forces the annotation band and permission model into core where a bug in it is a breaking change
for everyone. Rejected in §08: "Core owns chain symmetry, not authorization."

**Guarantee nothing; document a convention.** Then each binding is free to run its own middleware
and the failure this project exists to remove comes back, dressed as a style guide.

**Guarantee more — a mandatory minimum chain.** Whatever the minimum is, it is wrong for the
internal service behind a gateway that wants no chain at all, and it does not remove the need for
the general mechanism.

## Evidence

- `chain.go`, `interceptors.go`, `dispatch.go` — the entire surface of the guarantee.
- `internal/conformance/symmetry_test.go` `TestChainSymmetry` — one chain configured once, an HTTP
  road and a bus road that share nothing at the network layer, identical traversal asserted against
  `ChainSpec.Names()`.
- `binding/rest/rest_test.go` `TestChainSymmetry` — the same for the REST road.
- `auth/e2e_test.go` `TestCoreServesWithoutTheModule` — the cost, as a passing test: an empty chain
  serves on every road.
- `docs/06-crosscutting.md`, and the "Costs accepted" list in `docs/08-decisions.md`.
