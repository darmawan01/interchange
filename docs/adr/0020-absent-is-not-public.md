# ADR-0020 — Absent ≠ public, and an unwired resolver denies

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

Once authorization is annotation-driven, two defaults have to be chosen, and both are choices
between a silent failure and a loud one.

The first: what does an RPC with no `(auth)` annotation mean? "Public" is the convenient answer and
the dangerous one. A service annotated 90% of the way through a migration then has ten methods that
a reviewer reads as *reviewed and open* when they are really *not yet considered*. A
half-annotated service is worse than an unannotated one, because the annotations make the reviewer
believe the absences mean something.

The second: what happens when a stage is installed but its dependency is not? `Authz(cfg, nil)`
compiles. So does an annotation that implies a tenant check on a deployment that passed no
`WithTenantScoper`. Allowing in those cases turns a wiring mistake into an open door, in a build
that compiles and a suite that passes.

## Decision

Three rules, all belonging to `/auth` rather than to core:

1. **A missing annotation is a denial** under the module's default policy (`on_missing_annotation:
   error`), with reason `AUTHZ_ANNOTATION_MISSING`, and `protoc-gen-authz` fails the build on the
   same input. The denial message names the way out.
2. **A public RPC says `public: true` explicitly.** It is therefore greppable, diffable and
   reviewable — the set of open endpoints is a search, not an inference from silence. A public RPC
   is allowed without the `Authorizer` being consulted at all.
3. **An unwired dependency denies.** No `Authorizer`, no `TenantScoper` on a tenant-scoped RPC, no
   authenticator, or an envelope that never went through a `Registry` — all `PERMISSION_DENIED`
   with `AUTHZ_NOT_WIRED`. An RPC needing a resolver it does not have is a wiring bug, not an open
   door.

`on_missing_annotation` accepts `error` (default), `warn` and `ignore`, for migrating an existing
service. It is **a policy of this module**, declared in its config; core has no such knob and takes
no position.

## Consequences

The safe direction is the default, and the unsafe directions are explicit and visible in a diff.
The build-time gate catches a missing annotation on an RPC nobody has called yet, which is where
you want to find it; the runtime check catches what needs the message body.

The costs: annotating is now mandatory work on the on-ramp — every RPC, including the trivial ones,
needs a line before it will serve. `warn` and `ignore` exist for exactly that migration, and each
is a way to turn the guarantee off. Fail-closed also means a deployment mistake presents as
`PERMISSION_DENIED` rather than as a startup error, which reads at 3am like a policy problem rather
than a wiring problem — the distinct `AUTHZ_NOT_WIRED` reason exists to shorten that.

An annotation that is *present and wrong* — a permission with no verb, an atom outside a configured
`known_atoms` set — fails the build under every policy, because it was written and reviewed.

## Alternatives

**Absent means public.** The classic default, and the reason "we thought that endpoint was
protected" is a recurring incident class. Rejected in `docs/08-decisions.md`: build failure, as a
module policy rather than a framework rule.

**Absent means denied, silently, with no build gate.** Safe but discovered in production traffic
rather than in CI.

**Allow when a dependency is unwired, and log.** Logs are not read until after the incident.

## Evidence

Runtime, in `auth/authz_test.go`:

- `TestAbsentIsNotPublic` — an unannotated RPC, an authenticated caller holding every role, denied
  with `ReasonAnnotationMissing`; the message is asserted to mention `public: true`.
- `TestPublicIsAllowed` — the explicit opt-out serves an anonymous caller, and asserts the
  `Authorizer` was **not** consulted.
- `TestFailClosedOnNilResolver` — the same call is allowed with a `TenantScoper` wired and denied
  with `ReasonNotWired` without one.
- `TestFailClosedOnNilAuthorizer`, `TestFailClosedWithoutARegistry` — the other two unwired cases.
- `TestStrictnessPolicy` — the zero `Config` is `StrictError`, and all three settings behave.
- `TestTenantScopedRPCNeedsATenant` — `AUTHZ_TENANT_MISSING` rather than an unscoped query.

Build time, in `auth/internal/authgen/authgen_test.go`: `TestMissingAnnotationFailsTheBuild`,
`TestMissingAnnotationPolicyIsConfigurable`, `TestUnknownPermissionFailsTheBuild`,
`TestKnownAtomsClosesTheSet`.

Both roads, in `auth/e2e_test.go`: `TestMissingAnnotationDeniesOnBothRoads`.

Implementation and prose: `auth/authz.go` (the ordered fail-closed decision list is the function's
doc comment), `auth/config.go`, `auth/README.md` "The three rules" and "Strictness",
`docs/08-decisions.md`.
