# ADR-0044 — Inline and sidecar annotations conflict rather than take precedence

**Status:** Revisit when a third frontend forces the inline/sidecar rule to be settled repo-wide
**Date:** 2026-08-30 · **Phase:** 6

## Context

ADR-0042 gives every frontend two homes for an annotation: the native inline path, and the sidecar
as the universal fallback. Two homes for one value means a rule for what happens when both are
filled, and the ordinary answer is a precedence rule — "the sidecar wins", or "inline wins",
documented in a README.

For annotations that carry a security posture, silent precedence is a specific and severe failure.
Someone reads the service definition, sees `auth: {permission: {resource: providers, verb: READ}}`
on an RPC, and concludes the RPC is protected. A sidecar file they did not open, possibly written by
someone else, possibly months earlier, carries `public: true` for the same procedure. Under
precedence, the build succeeds, the emitted proto carries one of the two, and the file the reviewer
actually read was the one that lost. Nothing anywhere says so.

The mirror case is a sidecar entry that matches no RPC — a procedure that was renamed, or misspelled
when the sidecar was written. The author believes a posture is in force. It is not, and under a
best-effort reading nothing reports it.

## Decision

In the Interchange DSL, an annotation set both inline and in the sidecar is an **error**, not a
precedence rule: the diagnostic names the procedure and the key, points at the sidecar node, and
hints "remove one of them — keep `<key>` inline, or in the sidecar, not both". A sidecar entry whose
key matches no procedure in the parsed sources is likewise an error, in both frontends. Nothing is
emitted in either case.

## Consequences

The security posture of an RPC is stated in exactly one place, and a reviewer who reads that place
has read the whole answer. A team migrating from inline annotations to a sidecar, or back, is forced
to complete the move rather than leaving both half-populated — which is the state that produces the
failure above. Because the check is per key rather than per procedure, a procedure may carry
`transports` inline and `auth` in the sidecar; only the same key in both is a conflict.

The cost is a class of build failure that a precedence rule would not produce, hitting hardest
during exactly the migration the sidecar is meant to support. Adding a sidecar to a project that
already annotates inline fails until every duplicated key is removed, one at a time.

**The divergence, recorded honestly: the OpenAPI frontend does not implement this rule.** It uses
precedence — the vendor extension wins — with a stated rationale that is not the same situation:
`x-interchange-*` extensions live in the document, the sidecar exists for *a document you cannot
edit*, and so "the annotation nearest the operation is the one a reviewer reads". That is a
defensible reading of a different problem, but it is precedence, and precedence is what this record
rejects for the DSL. The two frontends therefore behave differently on the same input class. Both do
agree on the unmatched-key half: a sidecar key matching no procedure is an error in OpenAPI too. The
inconsistency is real, is not resolved here, and should be settled before a third frontend copies
whichever one it reads first.

## Alternatives

**Sidecar wins.** Rejected: the file a reviewer of the service definition is least likely to open
silently overrides the file they are reading.

**Inline wins.** Rejected for the DSL for the symmetric reason — the sidecar author has no signal
that their entry did nothing — and it is what OpenAPI chose, on the argument that a document you can
edit should not be overridden by an external file.

**Merge the two, field by field.** Rejected on the same grounds as the transports annotation
(ADR-0006): a reviewer would have to compose two sources in their head to answer "what does this
expose", and the whole point of an annotation is that the answer is visible in the diff.

**Warn on conflict, apply a precedence.** Rejected: a warning in a build log is a warning nobody
reads — §07's fourth rule for plugin authors, applied to frontends.

## Evidence

- `frontend/dsl/annotations.go` — `collector.merge`, whose doc
  comment states the reasoning ("silent precedence is how a security posture gets overwritten by a
  file nobody was reading") and whose `conflict` helper is called for each of the seven annotation
  keys: `transports`, `group`, `http`, `auth`, `cli`, `internal`, `idempotency`.
- `frontend/dsl/model.go` — the `origin` field, which exists to
  distinguish the two homes in a conflict diagnostic.
- `frontend/dsl/diag_test.go` — two of the 18 `TestTotalOrLoud`
  cases: "annotated both inline and in the sidecar" and "sidecar entry matches no rpc", each
  asserting the diagnostic lands in the sidecar with a line and column.
- `frontend/dsl/README.md` §"The sidecar" — the two rules, in
  those words.
- `frontend/openapi/annot.go` — the divergence, at the top of the
  file: "Precedence: a vendor extension wins." `sidecar.unused()` is the shared half — "sidecar: %s
  matches no procedure in this document".

See ADR-0042 for why two homes exist at all, and ADR-0020 for the same instinct applied at run time:
an absent annotation is not a permissive one.
