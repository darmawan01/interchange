# ADR-0050 — `(internal)` means public bindings skip it, not that it is unreachable

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

`ix lint` shipped an `INTERNAL_EXPOSED` rule that erred on `(internal) = true` combined with *any*
transport. The reading behind it was the obvious one: if a method is internal, declaring a road for
it is a contradiction, so say so.

It is the wrong reading, and the example proved it. `internal` + `bus` is not a contradiction — it
is precisely how an RPC is made reachable service-to-service and nowhere else. The example's
`Reconcile` declares exactly that, and an acceptance test calls it over a real NATS broker. Had the
rule shipped as written, `make lint` would have failed on this project's own example while the
runtime happily served it: a linter contradicting the runtime it lints for, with CI red and the
code correct.

## Decision

`(internal)` means every *public* binding skips the method. It does not mean unreachable, and it
does not conflict with a transport annotation.

The split is structural, not conventional. The RPC binding checks `md.Internal` in `Mount` and does
not register a handler for the procedure; the REST binding checks it in `restSchema` and drops the
method from the descriptor it hands to the transcoder, so the route never exists rather than
existing and answering badly. The message engine checks no such thing: `Server.Plan` filters on
`ExposedOn(s.cfg.expose)` alone and subscribes an internal method deliberately, because the whole
point of the annotation is a road that only a peer inside the deployment can take.

`ix lint` was corrected to match. `publicRoads` returns the subset of a method's resolved fan-out
that is `rpc` or `rest`, and `INTERNAL_EXPOSED` fires only when that subset is non-empty. The rule
now says what "skipped by every public binding" actually means.

## Consequences

The annotation does something a transport annotation cannot express on its own: `{on: [BUS]}`
already keeps a method off HTTP, but `(internal)` states the intent, and it keeps its meaning if
someone later adds `rpc` to the list — the lint error fires instead of the method quietly appearing
on a partner-facing surface. `Reconcile` is the worked case: `internal` plus `TRANSPORT_BUS` plus
`platform: true`, reachable by a platform workload on the bus, absent from the Connect mux, absent
from the REST routes, absent from the emitted OpenAPI, and explicitly skipped in the generated CLI.

The cost is that "internal" is now a claim about which bindings you mount, not a security boundary.
`publicRoads` covers `rpc` and `rest`; a bus subject, an MQTT topic tree and a socket are wired by
whoever runs the service, which is the judgment the rule encodes. That judgment has an edge:
a WebSocket server is served to browsers, and an `(internal)` method declared on `ws` is subscribed
by the per-socket engine and not flagged by lint. A deployment that mounts the WebSocket handler on
a public listener has to know that. The annotation is a routing decision with an authorization
story beside it (ADR-0019), not a substitute for one.

The rule is also asymmetric on purpose: it flags `internal` + a public road, and says nothing about
`internal` with no road at all, which resolves to the project default and is then caught by the
same check.

## Alternatives

**Error on `(internal)` with any transport** — what shipped first. Forbids the one combination the
annotation exists for.

**Make `(internal)` imply `{on: [BUS]}`** and reject an explicit transport list. Would have made
the fan-out of an internal method invisible in the diff, which is the property §02 and ADR-0006
are built around: a reviewer answers "what does this expose" by reading the annotation, not by
composing an annotation with a rule.

**Enforce `internal` in the engine too, and refuse to subscribe.** Then the annotation would mean
"unreachable", and there would be no way to declare a service-to-service-only RPC at all. The
existing spelling — no annotation, `{on: [BUS]}` — would have to carry that meaning implicitly,
which is exactly the implicitness the annotation removes.

## Evidence

- `ix/internal/lint/lint.go` — `publicRoads` (`rpc`, `rest`) and the `INTERNAL_EXPOSED` rule in
  `checkMethod`, with the reasoning in the comment above it.
- `binding/rpc/rpc.go:69` — `if !md.ExposedOn(b.expose) || md.Internal { continue }` in `Mount`.
- `binding/rest/schema.go:25` — the same check, filtering the descriptor before Vanguard sees it.
- `engine/server.go:139` — `Plan()` filters on `ExposedOn` only; internal methods are subscribed.
- `examples/catalog/api/catalog/v1/catalog.proto` — `Reconcile` declares `(internal) = true`,
  `{on: [TRANSPORT_BUS]}`, `platform: true` and `(cli.command) = {skip: true}`.
- `examples/catalog/acceptance_test.go` — `TestTheTransportsAnnotationIsLoadBearing`, subtests
  "internal is unreachable over HTTP", "internal still serves a platform workload on the bus"
  (against the in-process real NATS broker) and "neither is reachable on the REST surface".
- `binding/rest/rest_test.go` — `TestInternalIsOffTheRoad`: 404 by REST URI and by procedure path.
- Commit `389e1f7` — "ix lint's INTERNAL_EXPOSED rule contradicted the runtime and would have made
  CI red."

See ADR-0006 (a service-level transport default that a per-RPC annotation replaces), ADR-0019
(authorization is a module, not a core requirement) and ADR-0022 (`Capabilities` is data; the
engine has no transport switch).
