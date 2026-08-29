# ADR-0031 — A reply is bounded by the caller's deadline

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

**A decision made late — in phase 6, long after the engine was written and four drivers were
serving traffic against it.**

Sending a reply on a bus can block: a broker that has stopped acknowledging, a socket whose peer has
gone quiet, a QoS 1 publish waiting for a PUBACK that never comes. So the write needs a bound, and
the engine originally used a fixed one — a single reply budget, configured once, applied to every
reply.

One number is wrong for both ends of the range. A one-frame reply that needs 30 seconds is a broker
that is gone. A five-hundred-frame chunked reply is five hundred round trips, and a budget sized for
the small case expires somewhere in the middle — which is the worst possible outcome, because every
frame already sent is wasted work and the caller gets nothing rather than something. Sizing the
budget for the large case makes the small case hang.

The observation that resolves it is that the right number was already on the wire. Past the
caller's deadline, **nobody is reading**. Continuing to write frames toward a caller who has given
up is work with no consumer at either end.

## Decision

`Server.reply` derives its context from the caller's deadline. `replyContext(deadline, fallback)`
returns a context with the request's deadline when the request carried one, a context with the
configured `WithReplyTimeout` fallback (default 30s) when it did not, and a plain cancellable
context if that fallback is zero. The same context bounds every frame of a chunked reply, whether it
goes back through `Inbound.Reply` or through the `MetaReplyTo` publish fallback.

The fixed budget is not deleted; it is demoted to what it should always have been — the answer for a
caller who declared no deadline, which is the only case where the engine has nothing better to go
on.

## Consequences

The bound now scales with the message for free: a big reply gets as long as the caller was willing
to wait, and a small one to a dead broker stops when the caller stops. A chunked reply cannot expire
mid-message while the caller is still waiting, so the "every frame already sent is wasted" case is
gone unless the caller genuinely timed out.

The costs. A caller with a very long deadline now holds a reply write open for that long — the
deadline is trusted, and a client that sets an hour gets an hour. A caller with a very short one may
have its reply abandoned mid-chunk, which is correct (it had given up) but means the server does
work it never delivers; the dedupe cache holds the completed response, so a redelivery replays it
rather than re-running the handler (ADR-0014). And `WithReplyTimeout` still has a second job — it
bounds the wait for an in-flight original when a redelivery arrives — so the option is not purely
vestigial, and its name is now slightly narrower than its behaviour.

This is also the shape of a decision found rather than designed: nothing about it required a new
capability or a driver change. It is one function, added because writing a chunked reply against a
real transport made the fixed number obviously wrong.

## Alternatives

**A fixed reply budget.** What was there. Wrong for a one-frame reply and a five-hundred-frame one
simultaneously.

**A per-frame budget.** Bounds each write, and lets a slow-but-progressing reply run unboundedly
long in aggregate — the pathological case a bound exists for.

**A budget proportional to the frame count.** Invents a bytes-per-second constant the engine has no
way to know, per transport, per deployment.

**No bound at all.** A blocked publish holds a goroutine, an acknowledgement and a dedupe entry
open, and on MQTT it stalls the acks behind it.

## Evidence

- `engine/server.go` — `replyContext(deadline, fallback)`, and `reply()` which uses it around the
  whole chunk loop, with the reasoning in a comment: "The caller's deadline is the honest bound on a
  reply: past it there is nobody left to read one."
- `engine/server.go` — `serve()` reads `req.GetDeadlineUnixMs()` once and passes it to both
  `dispatch` and `reply`, so the handler and the write share one bound.
- `engine/server.go` — `WithReplyTimeout`'s doc comment states the demotion: "A request that carried
  one is bounded by that instead: the caller said when it gives up."
- `74404af` — the commit that made the change, and says why: "one number is wrong for both a
  one-frame reply and a five-hundred-frame one -- past the caller's deadline there is nobody reading
  either way."
- `drivertest/drivertest.go` `testDeadline` and `testLarge` — the two cases the fixed budget could
  not serve at once.
