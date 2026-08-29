# ADR-0028 — WebSocket has one channel, so the procedure lives in the envelope

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 5

## Context

A socket is not a broker. There are no subjects, no topics, no shared subscriptions and no
persistent addressing: there is one pipe between two peers, and both ends already know who they are
talking to. Every `Driver` method that maps a procedure onto a channel name has nothing to map it
onto.

That reads at first like a bad fit for the abstraction. It is the opposite — WebSocket is the
**degenerate** case in addressing, and it was built third precisely to test whether the seam
survives one. What it *does* demand is everything a broker was doing for you: accepting the
upgrade, authenticating a browser that cannot set an `Authorization` header on an upgrade,
demultiplexing concurrent calls over one pipe, and noticing that the pipe is gone.

## Decision

`Address(procedure)` ignores its argument and returns the constant `"ws.rpc"`; the procedure
travels entirely in `Request.procedure` and routing is the engine's, not a subject match.
`ReplyAddress()` is the package constant `"ws"` — deliberately not per-connection, because with
`NativeReply: true` a responder is never told where to answer, so both ends must name the one place
an answer can go identically. `ServiceWildcard` is `"ws.*"`, which matches the call address and not
the reply address, so one socket can carry a server and a client without either seeing the other's
traffic.

The driver declares `NativeHeaders: false` (a frame has no headers) and `NativeReply: true` (the
reply comes back on the same socket) — the pairing no broker produces.

The lifecycle is a **shim around** the driver, not extra protocol. `ws.go` is the six methods, 84
lines of code; `conn.go`, `server.go` and `client.go` are the socket. Two message types on the
wire, told apart by the WebSocket frame's own type: a **text** frame carrying a flat JSON metadata
object, valid only as the *first* message on the connection, and **binary** frames carrying the
engine's opaque body behind a uvarint-prefixed address. A late handshake closes the connection with
a policy violation, because credentials arriving mid-stream would apply to calls already dispatched
under the old ones.

The handshake metadata becomes connection-scoped: it is handed to the engine as `Inbound.Header` on
every frame, and the engine merges it beneath the envelope's own metadata (ADR-0032). The driver
also implements `interchange.Closer` and `interchange.Watcher`, so the engine closes the socket on
shutdown and fails in-flight calls the moment it dies rather than waiting out a deadline for a
reply that provably will not arrive.

## Consequences

Addressing collapses to two constants, and the annotation-driven fan-out still works: `Plan()`
produces one subscription for the socket. The connection-scoped credential arrives through the
field that exists for it rather than through a chain a driver may not touch, so an interceptor
cannot tell a handshake credential from an HTTP `Authorization` header — which is the whole claim.

The costs sit in the shim. A socket has no competing consumers, so load balancing is sticky by
connection and `CompetingGroup` is false; a server behind a load balancer gets whatever the
connection affinity gives it. Demultiplexing, backpressure, read limits and liveness are now this
package's problem, and `MaxPayload` is a chosen number (512 KiB) rather than a negotiated one,
because a socket has no protocol ceiling and an unbounded one lets a single call pin an arbitrary
buffer at both ends.

The single channel also broke two engine assumptions that no broker would have exposed:
`Server.Plan()` emitted the same subscription once per method when a service mixed queue groups —
which would have run the handler N times for one frame — and `Inbound.Header` was merged only for
transports claiming native headers. Both are fixed in core (ADR-0032), and the driver deleted 100
lines of workaround.

## Alternatives

**Invent per-procedure channels over the socket.** A private routing protocol on top of a pipe that
does not need one, and a second definition of the procedure that has to agree with the envelope's.

**Make `Address` return an error for single-channel transports.** Adds a case to every caller to
express something a constant already expresses. `drivertest` instead recognises the case and permits
a constant `Address`, while still requiring `Address != ServiceWildcard`.

## Evidence

- `driver/ws/ws.go` — `AddressCall`, `AddressReply`, `AddressWildcard`, `DefaultMaxPayload`, and
  `Caps()`; the package doc states the degenerate-addressing/demanding-lifecycle framing.
- `driver/ws/conn.go`, `driver/ws/server.go`, `driver/ws/client.go`, `driver/ws/options.go` — the
  shim.
- `driver/ws/README.md` — the wire (including the JavaScript a browser writes), "One channel, and
  what follows from it", the capability table, and the three seam findings.
- `driver/ws/ws_test.go` — `TestConformance` over a live socket; `TestOneChannel` (asserts
  `Address` is constant, `Address != ServiceWildcard`, and that `Caps()` is honest);
  `TestHandshakeCredential` and `TestRequestMetadata`; `TestHandshakeCredentialSurvivesChunking`;
  `TestConcurrentCalls` (40 calls on one pipe); `TestLargePayloadChunks` (read limit set *below* the
  message, so a round trip passing proves the chunking is real); `TestCloseFailsInFlight` (the
  `Watcher` path end to end).
- `docs/04-bindings.md` — "WebSocket is the degenerate case, not the hard one."
- `a70ee6b`, `371f623` — the phase-5 commits.
