# ADR-0038 — The generated CLI reports its own coverage

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

§08 raised a doubt about the CLI generator that applies to every partial generator: *a generated
CLI that covers only 80% of RPCs may be worse than none.* The reasoning is sound. A command tree
mounts only the RPCs carrying a `(interchange.cli.v1.command)` annotation, so a user running
`--help` sees a plausible, complete-looking surface with no indication that four operations are
missing. They cannot distinguish an operation the service does not support from one nobody got
round to annotating, and the gap is invisible precisely to the person it hurts.

The doubt was framed as a question about *how far to take the generator*. It is not. Coverage is
never going to be 100% — `skip: true` exists because some RPCs should not be commands — so the
question is what happens to the part that is not covered.

## Decision

Make the percentage visible rather than guess at it. `protoc-gen-cli` emits a per-service
`<Service>Coverage() clisupport.Coverage` alongside the command tree, splitting every procedure into
*covered* (annotated), *skipped* (annotated `skip: true`, a deliberate omission) and *missing* (no
annotation at all). `Coverage.String()` renders the report `ix` prints, and `Complete()` reports
whether every RPC is one of the first two. The plugin also takes `require_annotation=true`, which
turns the missing list into a build failure naming the RPC and telling the author to annotate it or
mark it `skip: true` to say the omission is deliberate.

## Consequences

The failure mode is addressed at its root: the gap stops being invisible. Two answers ship, picked
per repository — an internal platform sets `require_annotation=true` so a new RPC cannot land
without someone deciding whether it is a command; a repository with a hand-written CLI over its
important paths prints `Coverage()` instead and lives with a partial tree it can see.

`skip: true` matters more than it looks. Without it, `require_annotation=true` would have no way to
distinguish "deliberately not a command" from "forgotten", and the strict mode would be unusable.

Costs: every RPC now needs a decision, which is friction on a generator whose selling point is that
it is free. A service with no annotated RPC still generates a `Register…Commands` function that
mounts nothing — the emitted comment says so and points at `Coverage()`, but it is a function that
does nothing, which is a small piece of dead code per service. And `Coverage()` is only useful if
somebody calls it; nothing forces a binary to print it.

## Alternatives

**Cover every RPC automatically, deriving a command path from the method name.** Rejected: a
derived command tree is a second naming rule nobody agreed to, and it would mount internal RPCs on
a user-facing CLI by default.

**Ship no CLI generator until it can be total.** Rejected: it cannot be total — `skip` is a real
requirement — so this defers forever.

**Warn at build time on an unannotated RPC.** Rejected on the same grounds as the fourth plugin
rule in §07: a warning in a build log is a warning nobody reads. `require_annotation=true` fails
instead, and the default stays permissive so the generator is adoptable.

## Evidence

- `tools/clisupport/clisupport.go` — the `Coverage` type, its
  `Covered`/`Skipped`/`Missing` fields, `Complete()` and `String()`, with the rationale in the doc
  comment: "A CLI that silently fronts 80% of a service is worse than no CLI: the missing 20% is
  invisible until someone needs it."
- `tools/cmd/protoc-gen-cli/emit.go` — `emitCoverage` writes the
  per-service report; `emitCommands` notes when a service mounts nothing.
- `tools/cmd/protoc-gen-cli/cli.go` — the
  `require_annotation=true` failure, which names the RPC and offers `skip: true`.
- `tools/cmd/protoc-gen-cli/cli_test.go` —
  `TestCoverageReport` and `TestRequireAnnotation`;
  `tools/clisupport/clisupport_test.go` — `TestCoverageReport`;
  `tools/internal/gentest/generated_test.go` — `TestCoverage`
  against the committed golden package.
- `docs/08-decisions.md` §"Resolved by building it", and
  `tools/README.md`, which sets out the per-repo choice.
