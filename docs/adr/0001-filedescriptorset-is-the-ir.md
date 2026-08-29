# ADR-0001 — `FileDescriptorSet` is the IR, not a bespoke AST

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

Everything downstream of the contract — Go and TypeScript generators, the bus client plugin, the
CLI tree, the permission table, the REST transcoder, `ix describe` — reads one intermediate
representation. Something had to be chosen before the first generator was written, and the choice
is close to irreversible: every plugin, every frontend and every lint rule is written against
whatever shape it is.

The tempting option is a purpose-built AST. It would have exactly the fields Interchange needs,
no proto2 vestiges, no `optional` ambiguity, no descriptor-resolution rules to learn. The reason
not to is arithmetic rather than taste: a bespoke AST starts with zero tooling and everything has
to be written twice.

## Decision

The IR is a real `descriptorpb.FileDescriptorSet` — the same structure `protoc` and `buf build`
emit. Frontends produce one (`Frontend.Parse` returns `*descriptorpb.FileDescriptorSet`),
generators consume one, and `ix` builds one by shelling out to buf and re-parsing the image so
custom options resolve. No stage in the pipeline sees anything else.

## Consequences

Inheriting the descriptor ecosystem is the whole leverage. buf's linter and its breaking-change
detector work on the contract with no adapter (see ADR-0004). Every protoc plugin ever written is
a candidate generator. gRPC reflection, schema registries and the entire generator ecosystem read
descriptors natively. Custom options ride *inside* the descriptor, which is what makes an
annotation readable at build time by a plugin and at runtime by reflection — the dual availability
the whole annotation mechanism depends on (§02).

The cost lands on the frontends. Proto's expressiveness becomes the ceiling for every source
format, so a construct proto cannot express is a construct the contract cannot carry. That is
precisely why the OpenAPI frontend is lossy and refuses twenty-five construct classes rather than
approximating them (ADR-0039, ADR-0040): `oneOf`/`anyOf` have no canonical proto form, and
`required` + `nullable` cannot be encoded in proto3 at all. A bespoke AST could have represented
those. It would then have had to explain them to a generator ecosystem that has never heard of it.

The second cost is descriptor handling itself. A descriptor built by protocompile or by a frontend
carries custom options as `dynamicpb` values, and `proto.GetExtension` against those returns the
zero value — an annotation that reads as absent, which for the `auth` option is an authorization
check that stops firing. Core had to grow `ResolveOptions` to hand annotations back intact
(ADR-0035).

## Alternatives

**A bespoke AST.** Lost on cost: buf's two checkers, the plugin protocol, reflection and every
existing generator would each have had to be reimplemented against it, badly, before the project
delivered its first useful artifact.

**A neutral IR with a descriptor projection.** Two representations means two places for a
construct to be dropped, and the projection becomes the real contract anyway. Rejected as the
bespoke AST plus a translation layer.

## Evidence

- `frontend.go` — `Frontend.Parse` returns `*descriptorpb.FileDescriptorSet`; `Options.Deps` is
  one too.
- `ix/internal/image/image.go` — the keystone: `buf build -o -` produces the image, ix unmarshals
  it twice so every custom option arrives as a typed field, by number rather than by importing
  the module that declares it.
- `docs/09-schema-frontends.md` — "the highest-leverage decision in the design".
- `docs/08-decisions.md` — the IR row: `FileDescriptorSet`, not a bespoke AST.
