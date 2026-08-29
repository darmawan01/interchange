# ADR-0013 — A monotonic sequence per stream

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

At-least-once transports redeliver. A NATS JetStream consumer redelivers an unacknowledged message;
an MQTT 5 broker redelivers a QoS 1 packet whose PUBACK was lost. That is the delivery guarantee
working correctly, not a fault — and a chunked message makes it dangerous, because the unit that
gets redelivered is one *frame*.

Without an ordinal on the frame, a reassembler holding a list of chunks cannot distinguish a
redelivered chunk from a new one. It appends, and the reassembled body is corrupt: the right bytes
in the wrong quantity, which unmarshals into garbage or fails at a random offset with an error that
names neither the transport nor the redelivery. Broker ordering is not a defence either — a
redelivery arrives out of order by definition.

## Decision

`Frame.sequence` is a `uint64`, monotonic per correlation id, assigned by the chunker: `0`, `1`, `2`
… for the `KIND_MESSAGE` frames, and the terminating `KIND_END` frame carries the chunk *count* in
the same field. The reassembler keys chunks by sequence rather than appending them, so a duplicate
sequence is dropped — the receiver never sees it twice — and reassembly is complete only when every
index from `0` to `total-1` is present.

This is the single field that makes a bus binding safe to run at QoS 1.

## Consequences

Frame-level redelivery becomes invisible above the engine. Out-of-order arrival is handled by the
same mechanism for free, since the map is indexed rather than ordered, and a body is only emitted
when the index set is complete — a gap with the end already seen simply waits for the redelivery
rather than producing a short message.

The acknowledgement story stays honest. A duplicate frame is acknowledged immediately, because it
is already accounted for; the frames of an incomplete message are held and their acknowledgements
released together when the message is handled (ADR-0025), and a partial message that expires
reports failure so a transport that can redeliver does.

The costs: a `uint64` on every frame, and state proportional to the frames of every in-flight
chunked message — bounded by a TTL sweep and a per-message size ceiling, both of which are
configuration that has to be set sensibly. Reusing `sequence` for the chunk count on `KIND_END` is
economical and slightly overloaded; a reader has to know the convention.

Note the layer. This is frame-level suppression *within* one chunked message. A whole message
redelivered on an at-least-once transport is a different problem, solved a layer up by correlation
id — see ADR-0014, which replays the cached response rather than dropping the call.

## Alternatives

**No sequence; rely on broker ordering.** Rejected: a redelivery is out of order by construction,
so there is no ordering to rely on.

**A per-frame content hash.** Detects the duplicate without an ordinal, but says nothing about
position, so out-of-order arrival still breaks reassembly — and it costs a hash per frame.

**Refuse to chunk on at-least-once transports.** Rejected: MQTT 5 at QoS 1 with a payload ceiling is
the ordinary case, not the exotic one, and the driver already refuses QoS 0 precisely because
at-most-once silently loses one chunk of a chunked message.

## Evidence

- `api/interchange/transport/v1/envelope.proto` — `uint64 sequence = 3`, "Monotonic per stream;
  lets a receiver drop replays".
- `engine/wire.go` — `chunk` assigns `seq` per `KIND_MESSAGE` frame and puts the count in the
  `KIND_END` frame's `Sequence`.
- `engine/reassemble.go` — `reassembler.accept`: `if _, dup := p.chunks[f.GetSequence()]; dup` drops
  the replay and acknowledges it immediately; completion requires every index `0..total-1`, and a
  gap with the end already seen waits for the redelivery.
- `driver/mqtt/mqtt.go` — `AtLeastOnce: true` with the comment "QoS 1 redelivers; the engine
  suppresses the replay"; QoS 0 is refused for the reason above.
- `driver/mqtt/mqtt_test.go` `TestRedeliverySuppressed` replays the exact bytes of a request the way
  a QoS 1 broker does — a whole-message redelivery, so it exercises the correlation-id layer of
  ADR-0014 on top of this one. `driver/memory/memory_test.go` `TestConformanceDegraded` runs the
  conformance suite with both `AtLeastOnce: true` and `MaxPayload = 4096`, which is where chunked
  frames and redelivery meet.
