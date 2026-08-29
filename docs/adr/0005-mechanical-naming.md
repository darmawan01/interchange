# ADR-0005 — Naming is the derivation rule, so it is load-bearing

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

A method reaches five surfaces: an HTTP path and verb, a REST route, a NATS subject, a CLI
subcommand and an SDK method. Each of those can be *maintained* — written down per method, per
surface — or *derived* from the method name. Maintaining them is five places for one operation to
drift, which is the three-contract problem wearing a different hat.

Deriving them requires the name to carry information. `GetProvider` can produce
`GET /v1/providers/{id}` and `catalog provider <id>` only because `Get` means something. A method
called `ProviderFetch` produces nothing useful, and the derivation has to fall back to a manual
override — at which point the surface is being maintained again.

## Decision

Naming is the derivation rule, so it stops being a style preference and becomes load-bearing. The
verb prefix determines the HTTP method, the NATS subject and the CLI subcommand; the derivation
table is in §02. Services are `{Entity}Service`. Fields are `snake_case`, ids end `_id`, timestamps
end `_at`, durations carry their unit. Ids are strings, never integers. Enums are singular
PascalCase with a mandatory `{ENUM}_UNSPECIFIED = 0` and are append-only forever.

Because these are not style, they are not left to review. `ix lint` checks them directly — buf's
`STANDARD` lint rules do not know that a service name is an input to a URL.

## Consequences

One name produces five correct artifacts, and adding a sixth surface costs nothing per method. A
reviewer reading the proto can predict the URL, the subject and the command without opening the
generated code, which is what makes `ix describe` a summary rather than a discovery tool.

The cost is paid by a service that will not adopt the convention. Every derived surface gets a
worse default: an unrecognised verb produces a `POST` where a `GET` was meant, a CLI subcommand
that reads badly, and a subject that does not group with its siblings. The escape hatches exist —
`google.api.http` overrides the route, `(interchange.cli.v1.command)` overrides the CLI path — but
each override is a line of maintenance the convention was there to remove, and an adopter with an
existing non-conforming tree pays for all of them at once.

The second cost is that these rules are now enforced by a tool with an opinion. `ix lint` will
report a field named `createdAt` or a service named `Catalog`, and a team that disagrees has to
argue with CI rather than with a style guide.

## Alternatives

**Annotate every surface explicitly.** Honest and unambiguous, and it is what a plain RPC stack
does. Rejected because it reintroduces per-surface maintenance: the URL and the subject are then
two more things that can disagree with the method.

**Derive, but silently fall back on a non-conforming name.** Rejected as the worst of both: the
artifact is wrong rather than missing, and nothing tells the author.

**Leave naming to review.** Rejected on the same grounds as the annotation band (ADR-0003): a
convention nothing checks is a convention that decays, and here the decay produces wrong URLs
rather than untidy ones.

## Evidence

- `docs/02-contract.md` — the derivation table: RPC verb to HTTP, NATS subject and CLI.
- `ix/internal/lint/lint.go` — the package comment states the reason directly: the URL, the
  subject, the CLI command and the SDK method are all derived, so a service that does not end in
  `Service` or a field that is not `snake_case` produces a derived artifact that is *wrong* rather
  than ugly. The rules are named: `SERVICE_SUFFIX`, `FIELD_SNAKE_CASE`, `ID_IS_STRING`,
  `TIMESTAMP_SUFFIX`, `DURATION_UNIT`, `ENUM_ZERO_UNSPECIFIED`, `ENUM_APPEND_ONLY`, and
  `REST_NO_HTTP_RULE` for a method on the REST road with no derivable address.
- `tools/cmd/protoc-gen-cli` — the CLI tree, derived, with `(interchange.cli.v1.command)` as the
  override (ADR-0038).
