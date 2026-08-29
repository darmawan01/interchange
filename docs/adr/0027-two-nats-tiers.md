# ADR-0027 — Core NATS and JetStream are two drivers, not one with a flag

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

One durability tier for everything is wrong in both directions. Persisting a request/reply call
buys nothing — a reply is worthless after the caller's deadline, so writing it to disk is cost with
no benefit. Not persisting a call that has to survive a restart loses it. A framework that picks
one is wrong for half its traffic.

So both tiers are needed. The question is how they are exposed. The tempting shape is one
constructor with a `durable bool`, because from the outside it is "the same NATS". From the inside
it is not: JetStream **cannot preserve the publisher's reply subject** — a delivered message
carries the consumer's ack subject in that field instead — so the broker cannot route the response.
That is not a configuration difference. It changes what the transport can do.

## Decision

Two constructors, `New(conn, opts...)` and `NewJetStream(ctx, conn, opts...)`, returning drivers
with different `Capabilities`:

| | core | JetStream |
| --- | --- | --- |
| `NativeReply` | yes | **no** |
| `AtLeastOnce` | no | **yes** |
| everything else | \- | the same |

The durable tier declares `NativeReply: false`, and the engine's `MetaReplyTo` fallback carries the
return address in the envelope, exactly as it does on a transport that never had one. The reply
itself goes back over core NATS. It declares `AtLeastOnce: true`, so the engine turns on replay
suppression, and it acknowledges through `Inbound.Done` on completion (ADR-0025) — acked once the
call is handled and its reply sent, naked with a one-second delay otherwise, with consumers capped
at five deliveries because a message the engine can never handle is a busy loop, not durability.

The difference between the two tiers, in the driver, is two boolean expressions:
`NativeReply: d.stream == nil` and `AtLeastOnce: d.stream != nil`.

## Consequences

**No engine code changed.** JetStream's inability to route a reply was absorbed by a capability
that already existed for transports with no reply path at all, and nothing above the driver
noticed. That is the strongest evidence in the repository that `Capabilities` is carrying its
weight (ADR-0022).

Two constructors also mean the choice is visible in the composition root rather than buried in a
config value. A reader can see which tier a service is on without tracing a boolean.

The costs: two code paths in one module to keep in step, and a capability matrix that has to be
read per tier rather than per broker — `docs/04-bindings.md` gained a JetStream caveat because its
NATS column was true of only one tier. The durable tier also brings its own operational surface
that the core tier does not have: a stream to create, a `MaxAge` (five minutes, because a request
nobody answered within it has a caller that gave up long ago), durable consumer names derived from
the queue group, and a redelivery cap that will drop a poison message rather than retry it forever.
Each of those is a number somebody eventually has to tune.

`interchange.yaml` still exposes the choice as a `jetstream: true` config key, because config files
are strings; the *Go* API is two constructors, which is where the type difference matters.

## Alternatives

**One constructor with a durability flag.** The flag would change `Caps()` anyway, so it is the
same two drivers with the difference hidden behind a boolean and a shared struct. It also invites
`if durable` branches inside the driver, which is the transport switch this design refuses at every
other level.

**JetStream only.** Persists replies nobody will read, and adds a stream to every deployment that
wanted request/reply.

**Persist the reply too, so the durable tier is durable end to end.** A reply is worth nothing past
the caller's deadline (ADR-0031); paying storage for it buys a message no one will read.

## Evidence

- `driver/nats/jetstream.go` — `NewJetStream`'s doc comment states why `NativeReply` is false and
  that it is not an oversight; `maxAge`, `nakDelay`, `maxDeliver`; the consumer's
  `AckExplicitPolicy` and `DeliverNewPolicy`; `Done` wired to ack/nak.
- `driver/nats/nats.go` — `Caps()`: `NativeReply: d.stream == nil`, `AtLeastOnce: d.stream != nil`.
- `driver/nats/README.md` — "Durability tier" and the two-column capability table.
- `driver/nats/nats_test.go` — both tiers run the full `drivertest` suite against an in-process
  broker, plus `TestMaxPayloadChunking` against a real 8 KiB ceiling, `TestQueueGroupCompetes`, and
  `TestJetStreamRedeliversUnhandled`, which observes at-least-once processing end to end rather
  than trusting the flag.
- `docs/08-decisions.md`, "Resolved by building it" — "Bus durability — two drivers, not one with a
  flag", and `395534f`, the phase-4 commit.
