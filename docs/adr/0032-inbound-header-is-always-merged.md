# ADR-0032 — Inbound metadata is merged whatever the transport claims

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 5

## Context

**A design inversion, found by building the WebSocket driver. It was not foreseen; it was a guard
in the engine that read a capability backwards for four months and looked correct the whole time.**

`engine/server.go serve()` merged `Inbound.Header` into the envelope's metadata **only when
`Caps().NativeHeaders` was true**. Stated that way it sounds obviously right: a transport with
native headers has headers, so read them; one without has none, so skip.

It is exactly inverted. A transport *with* native headers has already put its metadata somewhere
the engine can find it. The transport that actually needs `Inbound.Header` is the one *without* —
because that is the transport with something to say that the envelope does not carry.

A WebSocket is that transport. It correctly declares `NativeHeaders: false`: a frame has no
headers. And a browser cannot set an `Authorization` header on an upgrade, so the credential
arrives in a handshake frame and belongs to the **connection**, not to any one call. Under the old
guard the driver had no way to contribute it at all — not through `Header` (skipped), not through
the context (dispatch builds a fresh `context.Background()`), and not through the chain (rightly
forbidden, ADR-0016). It worked around this by rewriting request envelopes below dispatch: 38 lines
of parsing the body an adapter may not parse (ADR-0024), and it could not cover a chunked request,
because a credential injected into one frame is not in the reassembled message.

## Decision

The guard is gone. `Inbound.Header` is merged **unconditionally**, and the envelope's own metadata
is merged **second**, so a per-call value beats a per-connection one. A transport without headers
hands over an empty map and nothing changes for it.

`Inbound.Header` is redefined accordingly: not "the transport's headers" but *whatever the driver
knows that the envelope does not*. On NATS and MQTT 5 that is the native headers; on a socket it is
the handshake credential.

This is not a chain modification, and the distinction is exact: nothing is added to, removed from,
skipped in or reordered around the chain. `Registry.Dispatch` runs the one chain the registry holds,
over an envelope whose metadata now contains a credential — precisely as it would if that credential
had arrived in an HTTP header on a Connect request. Interceptors cannot tell the two apart, which is
the claim.

## Consequences

Connection-scoped metadata becomes a first-class concept without a new capability, a new interface
or a per-transport branch — and it works for chunked messages, because the credential rides beside
the envelope rather than inside it. The WebSocket driver deleted its workaround: 81 lines out of
`conn.go`.

The costs. Every driver now has one more way to influence a call, and a driver that populates
`Header` carelessly can inject metadata a handler will trust — the merge order limits the blast
radius (a per-call value always wins) but does not eliminate it. The precedence rule is a rule
somebody has to know: two sources of metadata, envelope-wins, and it is only visible by reading
`serve()`. And "whatever the driver knows that the envelope does not" is a looser contract than
"the transport's headers"; it is the right looseness, but it is looseness.

The general lesson is worth more than the fix. A capability flag describes what a transport *has*,
and using it to gate what a driver is *allowed to say* conflates the two. The guard was written
when every driver was a broker, and no broker would ever have exposed it.

## Alternatives

**Keep the guard, add a `ConnectionMetadata` capability.** A second mechanism for the same thing,
and every driver has to declare which of the two it uses.

**Let the driver rewrite the envelope.** What the workaround did. It requires the driver to parse a
body it may not parse, and it cannot cover chunking.

**Pass connection metadata through the context.** Dispatch builds a fresh `context.Background()`
for a bus call — there is no caller context to attach to — so there is nothing to pass it through.

## Evidence

- `engine/server.go` `serve()` — the merge, with the reasoning in the comment: "The header is merged
  whatever the transport claims about native headers… The envelope is merged second, so a per-call
  value beats a per-connection one."
- `driver.go` — `Inbound.Header`'s doc comment carries the redefinition and the browser case.
- **`driver/ws/ws_test.go` `TestHandshakeCredentialSurvivesChunking`** pins it: a credential
  supplied only via `ws.WithHandshakeMetadata`, a request of 64 KiB against a 4 KiB frame ceiling,
  and a capture stage asserting `req.Metadata.Get("authorization") == "Bearer tok"`. This is the
  case the workaround could not cover at all.
- `driver/ws/ws_test.go` `TestHandshakeCredential` (a credential in the handshake frame alone
  reaches an interceptor, and a per-call value overrides it) and `TestRequestMetadata` (the same via
  the upgrade URL).
- `driver/ws/README.md`, "Where the handshake merge happens, and why it is not a chain
  modification", and seam finding #1.
- `371f623` — "Three more seam fixes, from building a driver that holds a connection": the commit
  that inverted the guard, alongside `Watcher` and the `Plan()` deduplication.
