# ADR-0011 — A one-byte discriminator in front of every envelope

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

Three envelope messages share one channel: `Request`, `Response` and `Frame` (ADR-0007). None of
them is self-describing — protobuf's binary encoding carries field numbers, not a message name —
and on a transport with no headers there is nowhere else to put a type tag.

The consequence is concrete. A receiver holding a byte slice cannot tell a whole `Request` from one
chunk of a large one: both are valid protobuf, and misreading either way is silent. `Request` and
`Frame` both begin with a string field at number 1, so a `Frame` will often parse as a `Request`
with a nonsense procedure rather than failing.

## Decision

The engine prefixes a single byte outside the protobuf: `0x01` `Request`, `0x02` `Response`,
`0x03` `Frame`. That is the entire wire format the message engine owns, and it is owned by the
engine rather than by any driver — a driver publishes an opaque body and never inspects it.

It is documented in §03 rather than left in the source, because any non-Go implementation of a
driver has to agree with it byte for byte.

## Consequences

Framing is unambiguous and costs one byte per message. `unframe` rejects an unknown leading byte
outright rather than attempting a parse, so a foreign message on the channel is a loud error
instead of a phantom request — which matters most on a shared broker where the subject namespace is
not exclusively ours.

Because the tag sits outside the proto, chunking can carry framed bytes through unchanged
(ADR-0012): a reassembled body still starts with its own discriminator and is unframed exactly as
if it had arrived whole.

The costs. One byte is now part of the public wire contract: the values can never be reassigned,
and adding a fourth envelope message means claiming a fourth byte — a compatibility event for every
driver, including ones outside this repository. The prefix also means the body is not a valid
protobuf message on its own, so a generic broker inspector that tries to decode it as
`transportv1.Request` fails at byte zero, and any non-Go driver must implement the framing itself
rather than pointing a stock protobuf decoder at the payload. On a transport that *does* have
headers the byte is redundant with a header we could have set — it is carried anyway, because a
format that varies with `Capabilities` is a format with two versions.

## Alternatives

**A `oneof` wrapper message.** The idiomatic protobuf answer, and it works — at the price of an
extra message layer on every payload, an extra allocation and an extra copy on the chunking path,
and a wrapper that has to be unwrapped before anything can look at the envelope. One byte does the
same job.

**Transport headers.** Rejected because it is exactly the facility the envelope exists to replace.
It works on MQTT 5 user properties and fails on a raw WebSocket, and a wire format that only some
drivers can use is not a wire format.

**Infer from the parse.** Try `Request`, fall back to `Frame`. Rejected: the shapes overlap, so the
inference is wrong rather than uncertain, and it is wrong silently.

## Evidence

- `engine/wire.go` — `kindRequest`/`kindResponse`/`kindFrame` as `byte` constants with the reason
  in the block comment; `frame(kind, m)` prepends, `unframe(b)` strips and rejects an unknown kind
  with `engine: unknown wire kind %d`.
- `docs/03-envelope.md` — "How the three messages share a channel", with the byte table, stated to
  be there because any non-Go driver has to agree with it.
- Commit `27fb4e5`, "Document the engine's wire framing and the gates".
- `driver/*` — no driver reads the first byte; each publishes an opaque body, which is what makes
  the framing the engine's alone (ADR-0024).
