# ADR-0009 — The procedure string is the Connect procedure string, verbatim

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

Every road needs a name for the operation. Connect and gRPC already have one:
`/pkg.Service/Method`, in the URL path. A bus has none — a subject or a topic is an address, not
an identifier, and it is frequently shaped by broker conventions that have nothing to do with the
contract. Something has to go in `Request.procedure`.

The tempting move is to mint an Interchange identifier: shorter, broker-friendly, free of the
leading slash that reads oddly outside HTTP. The cost of minting one is not obvious until you
follow where the string goes.

## Decision

`Request.procedure` is the Connect procedure string, verbatim, on every transport. The generated
Connect client and the generated bus client emit the same constant from the same contract, and
core mints it in one function: `Procedure(service, method)` returns `"/" + service + "/" + method`.

## Consequences

The procedure string is the join key between an authorization decision, a dashboard and a trace.
Because it is one string, the authz check, the metric label and the trace span carry the same
value on every road — so a dashboard needs no per-transport view, a trace crossing HTTP into the
bus keeps one span name, and an authorization rule written once applies to both. If it differed by
road, none of those three could be correlated across roads, and the per-road divergence would be
invisible: each surface would look internally consistent.

It also means dispatch is one table. `Registry.Dispatch` looks up a procedure and does not know
which binding handed it over, which is what makes chain symmetry structural (ADR-0016).

The costs: the leading slash is HTTP-shaped and looks foreign in a NATS subject or an MQTT topic,
and the string is longer than a minted id would be — it rides in every bus message, and on a
transport that also derives its address from the procedure the name is effectively carried twice.
Interchange also inherits Connect's naming: a change in that convention is a change here.

## Alternatives

**A minted Interchange id.** Rejected: it buys a few bytes and costs the join key. Every
observability and authorization surface would need a mapping table, and a mapping table is a place
for two names to disagree.

**Per-transport identifiers with a translation layer.** Strictly worse — the same divergence, plus
a translation step on the hot path.

**A hash of the procedure.** Compact and stable, and unreadable in exactly the situations where
you are reading it: a trace, a metric label, a denied-authorization log line.

## Evidence

- `api/interchange/transport/v1/envelope.proto` — `string procedure = 1`, commented "Deliberately
  IDENTICAL to the Connect procedure string, so one interceptor chain and one dispatch table serve
  every binding".
- `envelope.go` — `Procedure(service, method)`, `ServiceOf`, `MethodOf`; the `Envelope.Procedure`
  doc comment states the same reason.
- `examples/catalog/acceptance_test.go` —
  `TestSameProcedureStringInAuthzCheckMetricsLabelsAndTraceSpanOnBothRoads`. It first asserts the
  two generated constants agree before a call is made
  (`catalogv1connect.CatalogServiceListProvidersProcedure` and
  `catalogv1bus.CatalogServiceListProvidersProcedure`, both
  `/catalog.v1.CatalogService/ListProviders`), then calls the method over HTTP and over a real NATS
  broker and asserts the captured trace spans, metric labels and authorizer procedures are each
  exactly `[want, want]`.
- `dispatch.go` — `Registry.methods` is keyed by procedure; there is no per-binding key space.
