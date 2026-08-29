# ADR-0002 — Proto is the substrate, not the interface

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

ADR-0001 makes `FileDescriptorSet` the IR. Read carelessly that says "learn protobuf or leave",
which excludes most of the teams the project is for: the ones already maintaining an OpenAPI
document for the website and a hand-agreed JSON shape for the worker. Requiring them to rewrite
both in an IDL they do not use is a migration, not an on-ramp, and it is the reason most
contract-first tooling never gets adopted.

The two readings of "proto is the IR" have to be separated. One is *the pipeline speaks
descriptors*. The other is *the user writes .proto*. Only the first is load-bearing.

## Decision

You should not have to learn protobuf to use this. Proto is the substrate; a frontend adapter
normalises your format *into* it, and every stage after that is format-blind. `Frontend` is a
three-method interface — `Name`, `Detect`, `Parse` — and it is the only place in the system where
a source format is understood. A user who never opens a `.proto` file still gets every property of
one: buf's checkers, generated clients in five languages, reflection, the annotation mechanism.

## Consequences

The transport stops being an API decision and the *format* stops being one too. `.proto`, the
Interchange DSL and OpenAPI 3.x ship; TypeSpec, GraphQL SDL and JSON Schema are the extension
point's reason to exist, and adding one requires no change to core — which OpenAPI demonstrated by
needing none.

The cost is that the emitted proto is still what a reviewer must read. The frontend renders it,
`ix import` writes it, and it is committed under the drift gate (ADR-0041), so a DSL author who
wanted never to see protobuf will see it in a diff the first time they change a field. That is
deliberate — an invisible IR is an unreviewable one — but it is not the same as "you never touch
proto", and claiming otherwise would be dishonest.

The second cost is a maintenance commitment per frontend. Each one tracks an evolving external
spec, and each one has to find a home for the `auth` and `transports` annotations or produce a
contract with no security posture (ADR-0042).

## Alternatives

**Proto-only, no frontends.** Simplest, and the pipeline would be identical. Rejected because it
makes the framework unavailable to exactly the teams with the three-contract problem (§00).

**A runtime translation layer.** Accept any format at request time and map per message. Rejected
outright: it puts a conversion on the hot path, makes every format mismatch a production incident,
and is where systems of this shape normally go to die. Transformation happens once, at build time,
and produces an ordinary proto tree.

**A frontend that may return a partial result.** Rejected — see ADR-0039. A frontend that silently
drops what it cannot represent produces a contract that lies, which is worse than the three honest
contracts the project exists to replace.

## Evidence

- `frontend.go` — the `Frontend` interface and `Sources`/`Options`/`Diagnostic`; `Sources.Content`
  is pre-populated because a frontend must not read the filesystem (ADR-0045).
- `frontend.go` `SourceEmitter` and `ix/internal/cmd/import.go` — `ix import` type-asserts for
  `interchange.SourceEmitter` and refuses to run a frontend that cannot render reviewable source.
- `frontend/dsl`, `frontend/openapi` — two shipped frontends, neither of which required a change
  to core.
- `docs/09-schema-frontends.md` — "proto is the substrate, not the interface".
