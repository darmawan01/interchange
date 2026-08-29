# ADR-0042 — Every frontend needs a home for annotations; the sidecar is the fallback

**Status:** Revisit when a frontend has run against a real service long enough to show whether
sidecars drift from the sources they annotate
**Date:** 2026-08-30 · **Phase:** 6

## Context

The annotations are where most of the value is. `(transports)` decides which roads an RPC travels,
`(auth)` carries the security posture, `(command)` and `google.api.http` decide what surface exists
at all. A frontend that converts messages and services faithfully but cannot express annotations
produces a contract with **no security posture** — an imported service that compiles, dispatches,
and authorizes nothing.

Every source format has a different answer, and some have none. Proto has native options. OpenAPI
has vendor extensions. GraphQL has directives. JSON Schema has no service concept at all, so it has
nowhere to put a per-procedure annotation even in principle. Waiting for each format to grow an
extension mechanism means each new frontend is blocked on inventing syntax, and inventing syntax
per format is how a project ends up with four incompatible annotation dialects.

## Decision

Every frontend gets a native path where the format has one, and the sidecar is the universal
fallback so no frontend is ever blocked. The sidecar is a YAML file keyed by the full procedure
string, with the same annotation vocabulary and the same values as the inline form, delivered to
the frontend as `Sources.Sidecar` and named by `Sources.SidecarPath` so its diagnostics point at it
by name. Proto uses native options; the DSL uses inline blocks; OpenAPI uses `x-interchange-*`
vendor extensions; GraphQL would use directives; anything else uses the sidecar alone.

## Consequences

Adding a frontend is now a question about the format's *shape*, not about its extensibility. JSON
Schema can be imported without a service concept, because the services and their annotations come
from the sidecar. That is what makes the extension point real rather than nominal.

Because both paths decode through the same code in each frontend, they cannot drift apart in what
they accept — OpenAPI's `annot.go` says so explicitly, and the DSL's sidecar entries are validated
with the same vocabulary as inline ones. A sidecar entry that matches no procedure is an error in
both, because an annotation nobody applied reads as though the posture is declared when it is not
(ADR-0044).

`Sources.SidecarPath` exists because the first version did not have it: a diagnostic with no file
is barely a diagnostic, and a sidecar's mistakes are exactly the ones a reader needs pointing at.
It was added to core mid-phase when the DSL hit it.

**The honest cost, recorded as §08 raises it: a contract split across two files is a contract that
can drift between them.** The whole project exists to remove exactly that failure. The sidecar
reintroduces a small, bounded instance of it — the annotations can be edited without touching the
source document, a procedure can be renamed in one file and not the other, and the security posture
lives somewhere a reviewer of the service definition may never open. §08 leaves the question open:
*"Possibly the sidecar should be a migration aid with a deprecation path, not a permanent fixture."*
That question is not resolved here. Two things bound the damage in the meantime: the drift gate,
because both files feed one emitted `.proto` that is committed and reviewed (ADR-0041), and the
unmatched-key error, which turns the most common drift — a rename — into a build failure.

## Alternatives

**Native extension syntax per format, no sidecar.** Rejected: it blocks any format with no
extension point, JSON Schema most obviously, and it makes every new frontend a syntax-design
exercise before it is a conversion exercise.

**Sidecar only, for every non-proto frontend including OpenAPI.** Rejected: it puts the annotation
far from the operation it governs even when the document could carry it inline, which makes the
posture harder to review. Where a format has a native path, the native path is preferred and — in
OpenAPI — wins a conflict (ADR-0044).

**Refuse to import any format that cannot express annotations natively.** Rejected: it would make
the frontend seam useless for most of the formats §09 lists as its reason to exist.

## Evidence

- `frontend.go` — `Sources.Sidecar` and `Sources.SidecarPath`,
  the latter documented as existing because "a diagnostic without a file and a line is barely a
  diagnostic".
- `docs/09-schema-frontends.md` rule 3 — the per-frontend table
  (proto → native options, OpenAPI → `x-interchange-*`, GraphQL → directives, JSON Schema/anything
  → sidecar) and the worked sidecar example.
- `frontend/openapi/annot.go` — both paths decode through one
  file "so the two paths cannot drift apart in what they accept"; `sidecar`, `parseSidecar`,
  `lookup`, and `unused()` which reports keys that matched no procedure.
- `frontend/dsl/annotations.go` — the DSL's sidecar merge.
- `frontend/dsl/testdata/bare.annotations.yaml` and
  `frontend/openapi/testdata/payments.interchange.yaml` — worked
  sidecars under golden test.
- `ix/internal/cmd/import.go` — the `--sidecar` flag,
  "annotations file, for formats with nowhere to put an annotation".
- `docs/08-decisions.md` §"Open questions" — "How much does the
  sidecar undermine the single-source claim?", recorded unresolved.

See ADR-0044 for what happens when both homes carry the same annotation, and ADR-0045 for how the
annotation *descriptors* reach a frontend that has not linked them.
