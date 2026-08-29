# ADR-0007 — The envelope shape is not pluggable

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

Nothing else in the pipeline is fixed. Frontends, generators, transport drivers, codecs and
interceptors are each an interface with a registry, and §10 opens by saying so. Against that
backdrop the envelope looks like the sixth extension point: a deployment with an existing message
format, or a broker with its own conventions, would obviously prefer to keep them.

The reason it cannot be is that the envelope is the convergence point. HTTP supplies four things
for free — a method name in the path, metadata in headers, request/response correlation, a status
code — and a message bus supplies none of them. The envelope is where those four become explicit,
and it is what lets one dispatch table and one interceptor chain serve a Connect handler and a
NATS subscriber. Everything above it is written against its field names.

## Decision

The envelope shape is fixed. `Request`, `Response` and `Frame` in
`api/interchange/transport/v1/envelope.proto` are the vocabulary every binding populates; nobody
redefines them. It is one of exactly two things in the design that are deliberately not pluggable
— the other is chain symmetry (ADR-0015, ADR-0016) — and both are fixed for the same reason:
making them configurable would dissolve the guarantee the project exists to provide.

Configurability has a failure mode: so many knobs that no two deployments behave alike and no bug
report is reproducible. The guard is that extension points are interfaces with contracts rather
than free-form hooks, and that the two load-bearing shapes are not extension points at all.

## Consequences

One dispatch, one chain, one handler, on every road. A driver supplies six methods and an honest
`Capabilities` struct and inherits correlation, deadlines, metadata fallback, chunking and replay
suppression — all written once, against these three messages. A per-deployment envelope means no
shared dispatch and no shared chain, and therefore no chain symmetry, which is the only behavioural
promise core makes.

The costs are concrete. An adopter with an existing message format cannot bring it: they map into
the envelope at the edge or they do not use the bus roads. Every field in the envelope is public
API for every driver, in and out of this repository: changing one is a breaking-change event under
ADR-0004's rule, and adding one is a semantic event every driver implementation, Go or not, has to
learn. And a construct the envelope has no field for — streaming beyond the deferred
`Frame` shape (ADR-0051), a native broker facility with no envelope equivalent — cannot be
expressed by a driver locally; it is a change to core or it does not happen.

Note what is *not* fixed: authorization, validation, error taxonomy, codecs, transports and source
formats are all replaceable. Core guarantees your chain runs everywhere, not what is in it.

## Alternatives

**A pluggable envelope interface.** Rejected: the layers above it — dispatch, the chain, the
engine — are written against concrete field names, so the interface would have to expose exactly
those fields, at which point it is the envelope with extra indirection.

**No envelope; per-driver conventions.** This is the status quo the project exists to remove. It
is where a check enforced on HTTP is silently absent on the bus.

**A self-describing envelope (headers plus an open map).** Rejected as the same problem deferred:
correlation and deadlines would become conventions rather than fields, and a convention is what a
driver gets wrong.

## Evidence

- `api/interchange/transport/v1/envelope.proto` — the three fixed messages.
- `docs/10-extensibility.md` — "Two things stay fixed on purpose": the envelope shape and chain
  symmetry.
- `docs/03-envelope.md` — the five of seven §00 gaps that land here.
- `driver.go` and `drivertest/drivertest.go` — a driver's whole contract is six methods plus
  `Capabilities`; it never redefines a message (ADR-0022, ADR-0030).
