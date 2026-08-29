# ADR-0039 — Total, or loud

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

Phase 6 lets people adopt without writing protobuf: a frontend adapter turns OpenAPI, or a small
YAML DSL, into canonical descriptors, and everything downstream is format-blind. Every importer
ever written faces the same pressure at the same moment — the source document contains a construct
with no clean target form, and the shortest path to a working demo is to drop it and carry on.

That path is fatal here. The problem this project exists to solve is a service defined three or
four times in formats nothing mechanically connects, so that a renamed field surfaces as a runtime
failure. A frontend that silently drops an `oneOf`, a header parameter or an `auth` annotation
produces a single contract that *lies* — and one lying contract is worse than three honest ones,
because the three at least fail visibly at the seams. Silence is worse than duplication.

## Decision

Every frontend is total or loud. A construct is either represented in the emitted proto, or it
produces an error diagnostic carrying the exact source location — path, line and column — and a
`→` hint saying what to do instead, and **nothing is written**. No best-effort output, no "mostly
works", no partial descriptor set returned alongside an error. `Frontend.Parse` documents this as a
MUST, `Diagnostics.HasErrors` is what callers check, and `ix import` prints the count and refuses to
write a tree until every item is resolved.

## Consequences

An imported contract can be trusted the way a hand-written one can. A reviewer reading the emitted
`.proto` knows nothing was dropped on the way in, because nothing *can* be dropped without failing
the run.

The refusals are the work. Two frontends ship and their refusal tables are the most-tested part of
each: **18 construct classes in the DSL and 25 in OpenAPI**, each pinned by a named case asserting
the severity, the exact line, a non-zero column, the hint text, and that no partial descriptor set
came back.

The cost lands on exactly the people the frontends exist to help. An importer that refuses a
real-world OpenAPI document is work for the user before it is value: they arrive with a document
that already describes their service, and the tool tells them to go fix twelve things first. The
worked fixture wired into `ix import` reports three constructs needing a decision, and that is a
small document. Some refusals are unavoidable spec mismatches — proto3 cannot encode
present-but-null, so a property that is required *and* nullable has no honest target — and no amount
of effort in the frontend removes them.

Two mitigations soften it without weakening the rule. Some refusals are opt-*out* rather than fatal:
`x-interchange-skip: true` drops a property or parameter deliberately, `x-interchange-nullable:
optional` resolves the required-and-nullable case, `x-interchange-oneof` opts a `oneOf` in.
Deliberate, written down, visible in the document. And diagnostics come from the source, never from
the compiler: the DSL validates every construct against `yaml.Node` positions before rendering a
byte of proto, because a location in generated source is useless to the person holding the YAML.

The rule is scoped honestly elsewhere: a missing `auth` annotation is a *warning* in the DSL and a
configurable error in OpenAPI (`on_missing_auth`), because authorization is an optional module
(ADR-0019) and its policy is that module's to set, not the frontend's.

## Alternatives

**Best-effort import with a summary report.** The industry default. Rejected: the report is read
once, at import time, and the contract is read forever. A dropped construct that appeared in a log
line three months ago is indistinguishable from one that was never there.

**Emit a partial tree and mark the gaps with comments in the generated proto.** Rejected: generated
comments do not stop the tree being compiled, generated against, and shipped. The gap has to block.

**Represent everything approximately** — map `oneOf` to a message with all fields optional, map a
header parameter to a field. Rejected: an approximation is a silent semantic change, which is the
same failure wearing a more confident face.

## Evidence

- `frontend.go` — `Diagnostic` (severity, path, line, col,
  message, hint), `Diagnostics.HasErrors`/`Err`, and the MUST on `Frontend.Parse`. `Diagnostic.String`
  renders the `→` hint line.
- `frontend/dsl/diag_test.go` — `TestTotalOrLoud`, **18 cases**,
  each fixture marking the offending line with `#!` so the expected line number cannot drift out of
  date. Verified by count.
- `frontend/openapi/refuse_test.go` — `TestRefusals`, **25
  cases**, asserting "an error, the exact source location, and a hint — and nothing written".
  Verified by count. (The prose table in `frontend/openapi/README.md` lists more rows than that,
  because several rows cover opt-out variants of one refusal; the test is the enforced number.)
- `ix/internal/cmd/import.go` — the command refuses to write a
  partial tree: `nothing written — resolve the N item(s) above, then re-run`, exit 3.
- `BUILD-PLAN.md` — phase 6 exit criterion, and the cross-phase
  gate "A frontend is total or loud — never emit a partial contract".
- Commit `54bb942` on OpenAPI's `$ref` handling: refs are validated before the model is built,
  because libopenapi drops the object holding an unresolvable ref and carries on — "which is a
  partial contract, and refusing to produce one is the whole rule."

See ADR-0041 for what gets written when a frontend does succeed, and ADR-0042 for the annotation
paths the refusals police.
