# ADR-0041 — The emitted proto is the artifact

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

`Frontend.Parse` returns a `*descriptorpb.FileDescriptorSet`. That is enough to generate from: the
IR is a real descriptor set, everything downstream is format-blind, and a toolchain could go from
YAML to a typed client without any `.proto` ever existing on disk.

Building the DSL showed what that costs. If descriptors are the only output, the contract exists in
three places — the source document, a binary blob, and whatever the generators produced — and the
one a reviewer would want to read exists in none of them. A field number, a resolved import, a
derived RPC name, an annotation that did or did not survive: all invisible. The frontend becomes an
unreviewable step in the middle of a build, which is precisely the condition §09 rule 4 was written
to prevent, and the drift gate has nothing to gate.

The DSL author reported this as a gap in the seam rather than working around it locally.

## Decision

The emitted `.proto` is the artifact. It is rendered as formatted source, written into the api
tree, committed, reviewed and covered by the same drift gate as generated code (ADR-0033). This is
*enforced*, not asked for: emitting source is the optional `interchange.SourceEmitter` interface —
`ProtoSources(ctx, src, opt) (map[string][]byte, Diagnostics, error)` — added to core mid-phase, and
`ix import` type-asserts for it and refuses to run a frontend that does not implement one, because
such a frontend has nothing reviewable to write.

The DSL goes one step further with the same reasoning: it renders `.proto` source and then compiles
*that* with protocompile, so the descriptors come out of a real compiler and the source text is
what produced them.

## Consequences

A frontend user's contract is a normal `.proto` file that every other tool in the ecosystem already
understands — buf lint, buf breaking, `ix describe`, any protoc plugin ever written. Taking over by
hand is a one-way door available at any moment (ADR-0040), and it is available precisely because
the artifact is ordinary source rather than an internal representation.

It is also what makes the DSL's own error reporting honest. Because the emitted source is compiled,
the frontend can state that a protocompile rejection is a defect in the frontend and say so, rather
than surfacing a location in generated source to someone holding a YAML file.

Costs: `SourceEmitter` is a second thing every frontend must implement, and it is the harder half —
rendering formatted, deterministic, byte-stable proto source is more work than building descriptors.
An adapter author who only wanted descriptors is blocked at `ix import`, by design. The interface is
optional in Go's type system but mandatory in practice, which is a slightly dishonest shape and is
noted as such: `Parse` remains the interface, `SourceEmitter` remains an assertion.

And there is now a third file in the picture — source document, emitted proto, generated code — with
the emitted proto committed alongside its input. Re-importing a changed document produces a diff a
human has to read, which is the point, but it is a review burden that a build-artifact design would
not have.

## Alternatives

**Descriptors only, generate straight from `Parse`.** What the interface originally allowed.
Rejected once built: the IR is invisible, nothing is reviewable, and the drift gate has no subject.

**Emit a `FileDescriptorSet` file and commit that.** Rejected: it is committed and diffable in the
technical sense and unreadable in every sense that matters.

**Make `ProtoSources` part of `Frontend` itself.** Considered; rejected because the proto frontend
is the identity case and a frontend used purely as an in-process library has no tree to write. The
assertion in `ix import` puts the requirement where it actually applies.

## Evidence

- `frontend.go` — the `SourceEmitter` interface, whose doc
  comment states the reasoning: "A frontend that returns only descriptors leaves the IR invisible,
  which is the thing that rule exists to prevent, so `ix import` type-asserts for this and refuses
  to write a tree without it."
- `ix/internal/cmd/import.go` — the assertion and its error:
  *"the %s frontend cannot emit .proto source, so `ix import` has nothing reviewable to write"*,
  exit 2. On success it prints "commit the emitted proto: it is the contract now, and `ix verify`
  gates it".
- `frontend/dsl/render.go` and `compile.go` — render then
  compile; `frontend/dsl/testdata/catalog.golden.proto` is the golden-tested emitted artifact.
- `frontend/openapi/emit.go` — the same capability for OpenAPI,
  with `golden_test.go`.
- `BUILD-PLAN.md` phase 6 exit criterion: "Emitted proto is
  committed and under the drift gate. — `SourceEmitter`; `ix import` refuses a frontend without one".
- Commit `74404af` records the discovery: `SourceEmitter` is listed under "The seam gaps it found,
  all now in core", because `Parse` returning descriptors alone left the IR invisible.
