# ADR-0051 — Streaming is deferred

**Status:** Revisit when a real service needs an unbounded or long-running result and the receiving
transports are known
**Date:** 2026-08-30 · **Phase:** 0

## Context

Streaming constrains every later transport choice. A server-streaming signature is native on
Connect, becomes SSE on REST, and has no natural form at all on NATS core, MQTT or a
single-channel WebSocket — each would need a bespoke mapping, and each mapping is a contract that
has to hold for redelivery, ordering, cancellation and back-pressure. Committing to a streaming
signature before there is a real consumer means designing five mappings against a guess, and then
being unable to change any of them.

Against that, the problems that push people toward streaming — a payload larger than the broker
will carry, an ordered multi-part result, a redelivery that must not be processed twice — are
mostly framing problems, and framing is solvable once, in the engine, without a streaming
signature anywhere.

## Decision

No streaming RPC signature. `interchange` is unary only, and the plugins refuse a streaming method
rather than silently dropping it: `protoc-gen-bus` fails the build naming the RPC and the line, and
`protoc-gen-cli` refuses one that declares a `(cli.command)` — a bare streaming RPC is exempted from
`require_annotation` instead, since a method with no command form has no command to be missing.

What *is* built is the framing underneath, and it runs whenever a payload exceeds the driver's
`MaxPayload`:

- `Frame` in `api/interchange/transport/v1/envelope.proto` — `correlation_id`, `kind`, a monotonic
  `sequence`, `payload`, and a `status` set only on `KIND_END` / `KIND_ERROR`.
- Chunking in `engine/wire.go`: a body over the driver's `Caps().MaxPayload` is split into
  sequenced frames and reassembled by `engine/reassemble.go`, which holds every frame's
  acknowledgement until the whole message is handled (ADR-0025).
- Replay suppression in `engine/dedupe.go`, enabled only when a driver reports `AtLeastOnce`; a
  redelivery replays the cached response rather than re-running the handler (ADR-0014).
- A one-byte wire discriminator (`kindFrame`) so a receiver can tell a whole `Request` from a chunk
  of one (ADR-0011).

So the ordering, termination and de-duplication machinery a streaming implementation would need
already exists and is exercised. What does not exist is the signature and the five per-transport
mappings.

## Consequences

Adopters get one call shape on every road, and a generated client whose method signature is
identical whichever transport was wired behind it. The capability matrix in §04 states the position
in one row: server streaming reads `native | SSE | — | — | —`, native on Connect, SSE on REST, and
unimplemented on all three message transports. That row is the honest picture of what adding
streaming would cost — the transports would differ in what they can *express*, which is the
property the whole design is built to avoid.

The cost lands on anyone with a genuinely unbounded result. Today they page (`PageRequest` is in
`interchange/common/v1/types.proto`), or they publish results as separate messages and correlate
them themselves — the bespoke per-service protocol §00 lists as a problem this project set out to
remove. That gap is real, and it is the reason this record has a revisit condition rather than a
flat "no". §09 also records the knock-on: GraphQL subscriptions have no mapping in the SDL
frontend, because they map to streaming.

## Alternatives

**Server streaming only, on Connect and REST, unimplemented elsewhere.** The cheapest version, and
the one that breaks the guarantee: a method would exist that some transports can serve and others
cannot, so "declared once, fanned out everywhere" would acquire an exception, and the exception
would have to be reasoned about at every call site.

**Bidi over WebSocket only.** The socket already multiplexes concurrent calls and carries the
procedure in the envelope (ADR-0028), so bidi is nearest to free there. But a signature that works
on exactly one road is a road-specific contract wearing the contract layer's clothes.

**Express streaming as async fan-out.** Publish frames, correlate by `correlation_id`, terminate on
`KIND_END` — which is what the envelope already supports and what `Frame`'s comment ("streaming and
async fan-out share one shape") anticipates. This stays available to an adopter today. It is not
promoted to a generated signature because the semantics of cancellation and back-pressure are what
make streaming hard, and they are exactly what this spelling leaves to the caller.

## Evidence

- `tools/cmd/protoc-gen-bus/bus.go:88` — refuses `IsStreamingClient() || IsStreamingServer()`, with
  the error naming the RPC and the file:line to edit; `TestStreamingRefused` in
  `tools/cmd/protoc-gen-bus/bus_test.go` asserts both the refusal and the location.
- `tools/cmd/protoc-gen-cli/cli.go:94`–`112` — the refusal for a streaming RPC that declares a
  command, and the `continue` that keeps a bare one out of the coverage report.
- `api/interchange/transport/v1/envelope.proto:50` — `Frame`, `KIND_END`, `sequence`.
- `engine/wire.go` (`chunk`), `engine/reassemble.go`, `engine/dedupe.go`.
- `docs/04-bindings.md` — the per-transport capability matrix; the "Server stream" row.
- `BUILD-PLAN.md` §Deliberately out of scope, and `docs/08-decisions.md` — "None until a use case
  forces it; it constrains every later transport choice."

See ADR-0012 (chunking operates on framed bytes), ADR-0013 (a monotonic sequence per frame),
ADR-0014 (a redelivery replays the cached response) and ADR-0028 (WebSocket has one channel).
