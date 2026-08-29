# ADR-0010 — The runtime `Envelope` is not the wire `Request`

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

There is an obvious economy available: `interchange.transport.v1.Request` already has a procedure,
metadata, a payload, a codec, a correlation id and a deadline. Make it the type the interceptor
chain sees and there is one shape instead of two and no conversion anywhere.

It does not survive contact with either end of the system.

Over HTTP the wire envelope is **never serialised**. The Connect binding fills the same fields from
the URL path, the headers and the body; there is no `Request` message on the network, and
constructing one so the chain has something to hold would be marshalling work done purely to
satisfy a type.

At the other end, the chain needs the *decoded* message. An authorizer's signature takes a
`proto.Message` so it can make a resource-aware decision — read the tenant field, read the id being
fetched — and a validator applies field rules to the same decoded message. The wire `Request`
carries only `bytes` (ADR-0008). If the chain held the wire type, every interceptor that needs the
message would have to decode it itself, which means knowing the concrete type, which breaks the
rule that no adapter or module may import one (ADR-0024).

## Decision

Core's `Envelope` is a plain Go struct, deliberately distinct from the wire message. It carries the
same vocabulary plus a `Msg proto.Message` slot, and `Registry.Dispatch` populates `Msg` from
`Payload` before the chain runs. Drivers and the message engine speak the wire proto; dispatch
converts between the two, and it is the only place that does.

`Payload` may be nil when `Msg` is set: a binding that already holds a decoded message need not
encode it just so dispatch can decode it again.

## Consequences

The chain gets the decoded message for free, once, on every road. An interceptor is written against
`*Envelope` and never learns which transport it is on — the auth module reads its annotation off
`MethodDesc.Desc` via `MethodFromContext` and the message off `req.Msg`, with no privileged access
to core and no concrete type in sight. Decoding happens exactly once per call: the Connect binding
hands connect-go a decoded message and dispatch reuses it; the engine hands over bytes and dispatch
decodes them.

Conversion lives in one function, so there is one place where a field can be dropped in
translation, and one place to look when it is.

The costs: two shapes with overlapping fields have to be kept in step, and adding a field to the
envelope means adding it in both and in the conversion. `Envelope` also carries a response's
`Code`, `Message` and `Reason` alongside a request's fields, so it is a request-or-response union
rather than two types — economical, and it means some fields are meaningless in some directions.
And `Msg` being nullable makes "has this been decoded yet" a runtime property rather than a type
distinction; dispatch's `if req.Msg == nil` is the only guard.

## Alternatives

**One type: use the wire `Request` everywhere.** Rejected. It forces a serialisation on the HTTP
road that no wire ever sees, and it leaves the auth module unable to reach its message without
importing a concrete type — which would break the no-concrete-types rule that the whole adapter
layer rests on.

**Keep the wire type and put the decoded message in the context.** Rejected as the same coupling
with worse ergonomics: a `context.Value` of unstated type, and the decode still has to happen
somewhere that knows the type.

**Decode in each binding rather than in dispatch.** Rejected: it is the same work in N places, and
the first binding to skip it produces a chain that behaves differently on one road, which is the
failure class the project exists to remove.

## Evidence

- `envelope.go` — the `Envelope` doc comment states the decision and both reasons; the `Msg` field
  comment names the resource-aware interceptor case (§06).
- `dispatch.go` — `Registry.Dispatch` is the conversion: `CodecFor(req.Codec).Unmarshal` into
  `b.md.NewRequest()`, guarded by `if req.Msg == nil`. `boundMethod.invoke` calls the handler with
  `req.Msg` and returns an envelope carrying `Msg`, not `Payload`.
- `engine/server.go` — `Server.dispatch` builds an `interchange.Envelope` from the wire
  `transportv1.Request`; the engine never constructs a decoded message.
- `auth/` — the authorizer signature takes a `proto.Message`
  (`Authorize(ctx, procedure, ann, md, msg)`), which is the constraint that decided this.
