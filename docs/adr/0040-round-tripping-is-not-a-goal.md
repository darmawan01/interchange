# ADR-0040 — Round-tripping is not a goal

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

The moment a project has a `format → proto` frontend, someone asks for `proto → format`, and then
for the two to compose into an identity function. It sounds like a small extra and it is a
different project. Making `proto → OpenAPI → proto` an identity requires preserving everything the
IR has no place for: key order, comment placement, `$ref` structure versus inlining, which schemas
were named and which were anonymous, the exact spelling of every path template. Each one becomes a
compatibility surface, every frontend has to preserve it, and each new frontend multiplies the
matrix. It is a tar pit that would consume the project's whole maintenance budget and produce
nothing a user can point at.

There is also a correctness argument. Round-tripping implies the source format remains the place
the contract lives, which makes the descriptors an invisible intermediate — the exact condition
§09 rule 4 exists to prevent.

## Decision

Round-tripping is not a goal and will not be built. The transformation runs one way. The emitted
proto is the artifact — committed, reviewed, drift-gated — and the source format is an input, not a
mirror. `proto → DSL` does not exist. A user who imports a document and then edits the emitted
`.proto` has walked through a one-way door on purpose; the DSL README says so in those words.

## Consequences

Each frontend has exactly one direction to get right, one set of tests, and no obligation to
preserve anything the IR does not carry. That is what keeps a frontend a few thousand lines rather
than an ongoing negotiation with a spec.

It also makes "say it in proto" a cheap answer rather than a defeat. The DSL is small by design and
lists eight construct classes it deliberately cannot express — `oneof`, streaming, field-level
options, `reserved`, `additional_bindings`, proto2, cross-file references — and for every one the
advice is to run `ix import` once, commit the `.proto`, and edit it from then on. That advice only
works because there is no round trip to preserve.

The cost is that adoption is a migration, not an integration. A team that imports its OpenAPI
document does not get to keep editing the OpenAPI document; the emitted proto is the contract from
that point, and the original becomes a historical artefact. For a team whose OpenAPI document is
generated from something else, or maintained by another group, that is a genuine blocker rather
than an inconvenience — and there is no supported answer beyond "re-import, and review the diff".

A related cost: a REST surface is still produced downstream, by transcoding from `google.api.http`
annotations, so the project does emit OpenAPI-shaped output. That output is not the input document
and does not try to be.

## Alternatives

**Best-effort round-tripping with a documented lossy set.** Rejected: the documented lossy set
grows with every user, and "best effort" on a contract is the same failure ADR-0039 refuses.

**Round-trip only the Interchange DSL, which we control.** Rejected: it is the frontend that needs
it least — the DSL is deliberately smaller than proto, so `proto → DSL` would fail on most real
proto files, and the ones it succeeded on would be the ones nobody needs converted.

**Keep the source format as the source of truth and regenerate descriptors each build.** Rejected
for the reason in ADR-0041: it leaves the IR invisible and unreviewable, and it makes every
downstream tool depend on the frontend being installed.

## Evidence

- `docs/09-schema-frontends.md` rule 2: "Round-trip is not a
  goal… The **generated proto is the artifact**: it is emitted, committed and reviewed. The source
  format is an input, not a mirror."
- `BUILD-PLAN.md` phase 6, "Not a goal: round-tripping" — named
  as a tar pit before the phase started.
- `frontend/dsl/README.md` §"What the DSL deliberately cannot
  express" — eight rows, every one answered with "say it in proto", closing with "There is no round
  trip to preserve — `proto → DSL` is not a goal and will not be built."
- `frontend/dsl/roundtrip_test.go` — the name is about the
  *toolchain*, not the format. `TestRoundTripThroughTheToolchain` parses the DSL, builds a registry
  from the emitted descriptors with `protodesc.NewFiles`, and asserts the annotation extensions read
  back through the ordinary generated accessors — "a DSL user gets the same contract a proto user
  gets". Nothing in the repository converts proto back to any source format.

See ADR-0041 for the enforcement of "the emitted proto is the artifact", and ADR-0033 for the drift
gate it sits under.
