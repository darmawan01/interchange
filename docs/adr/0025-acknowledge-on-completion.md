# ADR-0025 — Acknowledge on completion, not on delivery

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

**This decision was not foreseen. It came out of building the NATS JetStream driver against an
engine that had no place to put it.**

`Inbound` originally carried an address, a header map, a body and a `Reply` function. There was no
completion hook, so a driver over a broker with explicit acknowledgement had exactly one moment
available to it: delivery. JetStream acked when the message arrived; a QoS 1 MQTT driver would have
PUBACKed the same way.

That is a different guarantee from the one the capability claims. Acking on arrival tells the
broker a message was processed while the handler has not started. A handler that panics, a process
that is killed mid-call, a decode that fails — all of it is work the broker already considers
delivered, and it silently vanishes. Replay suppression dedupes a redelivery (ADR-0014); it cannot
conjure one. **Delivered** and **handled** are different events, and only the second is worth
calling at-least-once.

## Decision

`Inbound` gains an optional `Done func(err error)`. The engine calls it **exactly once, after the
call has been handled and its reply sent**. `Done(nil)` means handled; `Done(err)` means not, and
the driver may redeliver if its transport can. A driver whose transport has no acknowledgement
leaves it nil and none of this costs anything.

A chunked message holds every frame's acknowledgement until the whole message is handled. The
reassembler collects the per-frame callbacks in `partial.acks` and hands them back with the
completed body; `ackAll` reports one outcome to all of them. A partial message that expires on the
sweep reports failure, so a transport that can redeliver does.

## Consequences

At-least-once means what the capability says. A crash mid-handler leaves work for redelivery
instead of losing it. Each driver expresses "not handled" in its own dialect: JetStream naks with a
one-second delay, capped at five attempts so a message the engine can never handle is not a busy
loop; MQTT simply leaves the packet unacknowledged, because MQTT has no negative acknowledgement
and silence is the only way to say so.

The costs are the engine's. **Acknowledgements now have to be threaded through reassembly and
dedupe** — `handle` carries an `acks` slice through unframing, reassembly and dispatch, every early
return has to `drop` rather than `return`, and the reassembler holds callbacks alongside chunks with
its own TTL sweep to release them. That is genuine complexity in the two most delicate files in the
engine. It also means a slow handler holds an acknowledgement open, and on MQTT that has a visible
cost: paho flushes acks in receipt order, so one unhandled message stalls the acks behind it on
that connection.

The path is easy to get half right, which is the second finding. The MQTT driver later showed that
`engine.Client.onReply` acknowledged only on the reassembly path — a reply arriving whole, the
common case, was never PUBACKed, stayed inflight until the broker's timer, and stalled everything
behind it. Every path out of `onReply` now acknowledges, including a reply whose correlation id
nobody is waiting for any more: it did arrive, and saying so is what stops it arriving forever.

## Alternatives

**Ack on delivery.** What the first driver did. It buys at-least-once *delivery*, which is not the
property anybody wanted.

**Let each driver decide when to ack.** Every driver would then reinvent "when is a call finished",
and the answer involves the reply, chunking and the dedupe cache — all engine state a driver cannot
see.

**Ack after dispatch but before the reply is sent.** Loses the case where the reply itself fails,
which on a bus is precisely the case redelivery exists for.

## Evidence

- `4a6728e` — "Two seam fixes the drivers found, fixed where every driver inherits them": the
  commit that introduced `Inbound.Done`, carrying its reasoning. A `git log -S` over `driver.go`
  for that field names this commit and no other.
- `4b9f100` — the follow-on finding: the client-side `onReply` ack path, found by the MQTT driver.
- `driver.go` — `Inbound.Done`'s doc comment, which is the decision in prose.
- `engine/server.go` — `handle` builds and carries `acks`; `drop` and `ackAll`; `serve` returns the
  outcome that is reported.
- `engine/reassemble.go` — `partial.acks` ("Acking a chunk on arrival would tell the broker a
  message was processed while it is still half a message sitting in memory"), `accept` returning
  held callbacks, and `sweepLocked` failing them on expiry.
- `driver/nats/jetstream.go` — ack on success, nak with `nakDelay`, `maxDeliver = 5`.
- `driver/mqtt/README.md`, "Acknowledgement" — the PUBACK is `Inbound.Done`, and what leaving it
  unacknowledged costs.
- `docs/04-bindings.md`, "Acknowledgement is a capability like any other".
