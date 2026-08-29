# ADR-0003 — Reserve an annotation band before the second annotation exists

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

A custom annotation is a field number on `google.protobuf.MethodOptions` — and that is a **global**
namespace shared with every other extension anyone links into the same binary. Two annotations at
one number is not a build error. The descriptor parses, and one of the two options is simply gone.
For `transports` that means an RPC silently exposed on roads nobody declared; for the `auth`
option it means an authorization check that stops firing, in a build that compiles and a suite
that passes.

The failure is invisible at the point it is introduced and undebuggable afterwards, which is why
the register has to exist before there is anything to register.

## Decision

Reserve `50000–59999`, the conventional private band, and record every assignment in one table —
`docs/annotation-band.md` — **before** the annotation exists. The table landed in phase 1, in the
same change as the first annotations, so every number in use was on record before a second one
could be proposed. A PR that adds an extension without a row does not merge, and `ix lint` reads
the table: any extension number inside the band with no matching row is an error.

Numbers are never reused and never renumbered. Different extendees may share a number — `50002` is
`transports` on `MethodOptions` and `service_transports` on `ServiceOptions` — because the extendee
is part of the identity, so the two cannot collide. Both still get a row, and the band table is
indexed by the `(extendee, number)` pair rather than by the number alone. A module owns its own
numbers: core does not reserve space for an annotation it will never parse, which is why `50001`,
`50007` and `50008` belong to the optional `/auth` module and their proto lives in that module's
tree.

## Consequences

The check is mechanical rather than cultural, so it survives the people who remember why it
exists. `ix` embeds its own copy of the table, so it can lint a project that has no copy of the
doc; a project's own file wins when it has one.

The costs are small and real: a documentation edit is now part of the definition of done for an
annotation, `ix` carries a markdown parser for a table, and the band is a finite resource that a
sufficiently annotation-happy ecosystem could exhaust — `50005`, `50006` and `50009–59999` are
currently free.

The rule that never renumber is the one with teeth. It means a number claimed for an experiment is
spent forever, and the table records assignments that may outlive their annotations.

## Alternatives

**Allocate on demand.** Rejected: it is the status quo that produces the collision, and the
collision is silent.

**Use a registry service.** Overkill for a private band, and it puts a network dependency in front
of adding an annotation (see ADR-0048, ADR-0054).

**Rely on review.** A reviewer cannot see a collision with an annotation defined in a different
module in a different repository. Only a table can.

## Evidence

- `docs/annotation-band.md` — the register, including the rule that different extendees may share
  a number.
- `ix/internal/band/band.go` — `Low`/`High` bound the range; `Table` is indexed by
  `key(extendee, number)`; `annotation-band.md` is `go:embed`ed as the builtin fallback.
- `ix/internal/lint/lint.go` and `ix lint --band` (`ix/internal/cmd/lint.go`).
- `ix/internal/lint/lint_test.go` — `TestUnregisteredBandNumberIsAnError` against the
  `ix/testdata/badband` fixture, `TestRegisteredAnnotationsPass`, `TestBandTableParses`.
- `api/interchange/transport/v1/transports.proto` — `transports = 50002` on `MethodOptions` and
  `service_transports = 50002` on `ServiceOptions`, each with the band comment.
- `CONTRIBUTING.md` — "Adding an annotation": claim the number first.
