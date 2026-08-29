# ADR-0012 — Chunking operates on framed bytes, not on the message

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

Brokers have payload ceilings — NATS defaults to about 1 MiB, MQTT brokers vary, a WebSocket peer
has its own limit — and a `Capabilities.MaxPayload` above zero says so. A message over the ceiling
has to be split. The question is *what* gets split.

The obvious answer is the message: chunk the request's payload, send the pieces, reassemble the
payload at the far end and rebuild the `Request` around it. That answer quietly creates a second
code path. A chunked request arrives as pieces plus a header that has to be reconstructed, so
dispatch, replay suppression, metadata handling and the reply path each need a "was this chunked?"
branch — and every one of those branches is a place where a chunked call can behave differently
from a whole one. The WebSocket driver's credential-in-the-handshake case is exactly where that
divergence bites: the workaround it originally carried could not cover a request too large for one
frame.

## Decision

Chunking operates on the *framed* bytes. The engine frames the envelope first — `[kind][protobuf]`,
ADR-0011 — and then splits that byte slice into `Frame`s whose payloads concatenate back into the
same `[kind][protobuf]`. The receiver reassembles and unframes the result exactly as if it had
arrived whole.

Nothing above the engine has a chunked code path. The terminating `KIND_END` frame carries the
chunk count in `sequence`, so the receiver knows when it is done without a length prefix.

## Consequences

There is one path into dispatch. A 40 KB request over a 1 KB ceiling and a 200-byte request over no
ceiling produce identical inputs to `Registry.Dispatch`, so an interceptor, an authorizer and a
handler cannot behave differently on the two — and a test for one is a test for the other. The same
reassembler serves both directions, because a large request and a large response are the same
problem.

It also composes with replay suppression rather than fighting it: a duplicate frame is dropped by
sequence inside the reassembly (ADR-0013), and a whole redelivered message is handled a layer up by
correlation id (ADR-0014).

The costs. The framing byte is carried inside the reassembled body rather than per frame, so the
chunker must budget for the `Frame` wrapper itself when computing the split size — `chunk` marshals
a worst-case probe frame and subtracts its length plus the discriminator, the payload tag and
`binary.MaxVarintLen32`, because a driver reporting a hard broker limit must not be overshot by a
byte. That budget is measured rather than guessed, and it is a piece of arithmetic that has to stay
correct as the `Frame` message evolves. A `MaxPayload` too small to hold the header at all is a
loud error rather than an infinite split.

Buffering is the other cost: a partial message is held in memory until its last frame arrives, which
is what the per-message ceiling and the TTL sweep exist to bound.

## Alternatives

**Chunk the payload, rebuild the envelope.** Rejected for the branch it forces into every layer
above the engine, and for the credential case where that branch was already observed to be wrong.

**Refuse messages over the ceiling.** Honest and cheap, and it makes the payload limit a contract
concern that every service author has to reason about per broker. Rejected: "bus payload ceilings
become your problem" is listed as an accepted cost of the design (§08), not as an accepted cost of
using it.

**Let each driver chunk.** Rejected outright — it is per-broker duplication of the hardest code in
the engine, and drivers that get it slightly differently right are indistinguishable from drivers
that get it wrong.

## Evidence

- `engine/wire.go` — `chunk(correlationID, framed, max)`: the parameter is already-framed bytes; the
  comment states that the frames carry the framed body so the receiver "reassembles bytes it can
  unframe exactly as if they had arrived whole". The overhead probe and the
  `MaxPayload %d is too small` error are in the same function.
- `engine/reassemble.go` — `reassembler.accept` concatenates chunk payloads in sequence order and
  returns the body; the caller unframes it. Shared by both directions.
- `internal/conformance/engine_test.go` — `TestChunking`: a 40,000-byte payload over
  `caps.MaxPayload = 1024`, round-tripped with no per-service code.
- `driver/memory/memory_test.go` — `TestConformanceDegraded` runs the whole conformance suite with
  `MaxPayload = 4096`, so every conformance test also runs chunked.
- `driver/nats/nats_test.go` `TestMaxPayloadChunking` and `driver/mqtt/mqtt_test.go` `TestChunking`
  exercise the same path against real brokers.
