# ADR-0014 — A redelivery replays the cached response rather than being dropped

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

The design said *drop*. §03 introduces `Frame.sequence` as the field that "lets a receiver discard a
replay without the handler ever seeing it", and that is the right instinct for a chunk of a chunked
message (ADR-0013). Generalised to a whole request, it is wrong, and building the engine is what
made that visible.

Ask why the redelivery happened. On an at-least-once transport the broker redelivers because it did
not see an acknowledgement — and the overwhelmingly common cause of that is that the *first
response was lost*, or the acknowledgement was, after the handler had already run. Drop the
redelivery and the correct behaviour is achieved on exactly one side of the exchange: the handler
runs once, and the caller that retried is still sitting there with nothing, until its deadline
expires. The broker then redelivers again, and gets dropped again. A drop turns a lost response
into a guaranteed timeout.

## Decision

The engine keeps a response cache keyed by correlation id. The first arrival claims the id, runs the
chain, and stores the marshalled `Response`; a later arrival with the same id does not run the
handler — it waits for the first call's response and replays it to the redelivery's reply address.
The handler runs once and the caller still gets an answer.

The cache is constructed only when a driver reports `AtLeastOnce`, so on a transport that does not
redeliver it costs nothing. A claim that produced no response is abandoned rather than cached, so a
redelivery after a crashed or unmarshallable call gets a fresh attempt instead of waiting forever on
a call that never finished.

This was decided while the engine was being written (commit `3adf5e3`), not foreseen in the design
docs; the MQTT 5 driver in phase 5 (commit `4b9f100`) is what proved it end to end against a real
broker, and found in passing that the reply path acknowledged only on the chunked branch.

## Consequences

Exactly-once *effects* with at-least-once delivery, for the window the cache covers. A QoS 1 peer
that retries gets an answer rather than a timeout, and the answer is the same answer — not a second
execution of a non-idempotent handler. The replayed reply is acknowledged too, which matters: a
redelivery that is answered but not acknowledged is retried forever.

The costs are a cache and its bounds. Memory is proportional to in-flight correlation ids and to
response size, held for a TTL (two minutes by default) after the call completes — a window during
which every response on the road is retained. The TTL is the correctness knob: too short and a late
redelivery re-executes the handler; too long and the cache is a memory leak with a schedule. A
redelivery arriving while the original call is still running blocks on the first call's completion
up to the reply timeout, so a slow handler makes a redelivery slow rather than duplicating it.
Correctness is also per-process: the cache is in memory, so two servers in a competing-consumer
group do not share it, and a redelivery routed to the other member re-executes. That is a real
limit, and it is the honest boundary of what this buys.

Replay suppression dedupes a redelivery; it cannot conjure one. That is why the engine acknowledges
on completion rather than on delivery (ADR-0025) — the two decisions only work together.

## Alternatives

**Drop the redelivery.** Simpler, and what the design doc implied. Rejected once the question "why
did it redeliver?" was asked: it converts a lost response into a guaranteed timeout, and it does so
silently.

**Re-run the handler.** Correct for an idempotent method and a duplicated side effect for every
other one. The framework does not know which is which — `idempotency_level` describes the contract,
not the handler — so it cannot make that call on the service's behalf.

**Push it to the service.** Every handler implements its own idempotency table. That is the status
quo the framework exists to remove: written once per service, in N languages, each subtly different.

**Cache in the broker or an external store.** Would fix the cross-process gap, and it puts a shared
dependency in front of every bus call. Not taken; the in-process cache is honest about its scope.

## Evidence

- `engine/dedupe.go` — the package comment states the decision and the reason verbatim: "Dropping
  the message outright would be simpler and wrong -- the redelivery usually happened *because* the
  first response was lost." `begin`/`complete`/`abandon`/`wait`, with a TTL sweep.
- `engine/server.go` — the dedupe branch: `s.dedupe.begin(correlationID)`; `claimed == false` waits
  for the cached bytes and calls `s.replayCached(...)`, which unmarshals the stored `Response` and
  sends it to the redelivery's reply address. The cache is constructed only under `AtLeastOnce`
  (`dedupeTTL` defaults to `2 * time.Minute`).
- `internal/conformance/engine_test.go` — `TestReplaySuppression`: `caps.AtLeastOnce = true` with
  `memory.WithDuplicateDelivery()`, asserting the response is correct and the handler ran exactly
  once.
- `driver/mqtt/mqtt_test.go` — `TestRedeliverySuppressed` replays a request's exact bytes the way a
  QoS 1 broker does, then asserts three things: the handler ran once, the redelivery *was answered*
  ("the redelivery was dropped instead of replayed: the peer that retried is still waiting"), and
  nothing is left inflight on the broker.
- `CONTRIBUTING.md` names this among the comments worth writing: "why replay suppression replays the
  cached response instead of dropping the message".
