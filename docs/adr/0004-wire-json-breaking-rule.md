# ADR-0004 — `WIRE_JSON`, not `FILE`, for breaking-change detection

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

buf's breaking-change detector runs against a baseline and answers one question: is this edit
compatible? What "compatible" means is a rule category, and the two plausible ones differ a lot.
`FILE` treats the file as the contract — moving a message to another file, renaming a package,
changing a Go package option are all breaking. `WIRE_JSON` treats the *wire* as the contract: the
binary encoding and the JSON field names must stay compatible, and anything that preserves both is
allowed.

This has to be decided once, at the start, because the rule is the baseline. Changing it later
re-baselines every check: edits the old rule accepted may be rejected by the new one and vice
versa, and the first run after the change reports a wall of findings nobody can triage.

## Decision

`WIRE_JSON`, set once in the root `buf.yaml` and inherited by every module in the workspace.
`FILE` is stricter than a public JSON surface actually requires: what a client depends on is the
field numbers, the field types and the JSON names, not which file a message happens to live in.

## Consequences

Refactors stay possible. A contract can be reorganised across files, a message can move, and a
package can be tidied without a breaking-change finding — while a renamed JSON field, a changed
field number or a narrowed type still fails, which is the failure class the gate exists to catch
(§00: a renamed field caught by a runtime failure rather than a build failure).

The cost is that source-level identity is unprotected. A downstream consumer that generates code
from file paths, imports a specific `.proto` by name, or depends on the Go package a message lands
in gets no protection from this gate. That is a real category of breakage and this rule declines
to catch it, on the grounds that a public JSON surface is the thing with external consumers.

The second cost is the one that motivated deciding early: the rule is now expensive to change. Any
future move to `FILE` re-baselines everything.

## Alternatives

**`FILE`.** Rejected as stricter than the surface requires — it turns routine refactors into
breaking-change findings, and a gate that fires on non-breaking edits is a gate people learn to
override.

**`WIRE` alone.** Protects the binary encoding but not the JSON names. Interchange has public JSON
surfaces on both the REST and the RPC roads, with different casing conventions per surface (§08),
so JSON names are part of the contract and `WIRE` alone would not defend them.

**Per-module rules.** Rejected: two spellings of one check is how a gate starts passing in one
place and failing in another — the same reasoning that collapsed CI's `ix breaking` and the
Makefile's `buf breaking` into a single command.

## Evidence

- `buf.yaml` (repo root) — `breaking: use: [WIRE_JSON]`, with the reason in a comment; one
  workspace, five modules, one rule.
- `Makefile` — the `breaking` target: `buf breaking --against '.git#branch=$(BASE_REF)'`, which CI
  calls with the base ref passed in.
- `ix/internal/cmd/fmt.go` — `newBreaking`: `ix breaking --against …` shells out to buf with the
  project's configured rule, and its help text states the `WIRE_JSON`-over-`FILE` reason.
- `docs/02-contract.md` and `docs/08-decisions.md` — the decision row: `WIRE_JSON` once any JSON
  surface is public.
