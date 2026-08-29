# ADR-0008 — `bytes`, not `google.protobuf.Any`

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

The envelope has to carry a marshalled request message without knowing its type — the engine, the
drivers and the framing code are all written once for every service. Protobuf offers two ways to
do that. `google.protobuf.Any` wraps a message with a type URL, which makes the payload
self-describing. A plain `bytes` field carries the encoded message and nothing else.

`Any` is the idiomatic answer to "a field whose type varies", which is why it needs an explicit
reason to lose.

## Decision

`bytes`. `Request.payload` and `Response.payload` are opaque encoded messages, and the type comes
from `procedure` — which already names it, uniquely, and which generated dispatch resolves
statically through `MethodDesc.NewRequest`.

## Consequences

The payload costs what the message costs. `Any` embeds a type URL on every message — a string
repeated on every request on every road — and forces a registry lookup to unmarshal, which is a
map lookup and a dynamic construction per call, on the hot path, to recover information the
procedure string already carried. Duplicating the type in two places also creates a way for them
to disagree; `bytes` has no such state.

It also keeps the codec honest. Because the payload is opaque, `codec` ("proto" or "json") is a
single field describing the whole body, and a browser-facing binding can stay human-readable while
service-to-service traffic stays binary (ADR-0053). An `Any` carrying a JSON body is an awkward
shape.

The cost is real and lands on tooling. A driver or a proxy cannot introspect a payload without the
descriptor: an operator staring at a captured NATS message sees a procedure string and a blob, and
a generic broker-side inspector, message browser or content-based router cannot decode it. Any
tool that wants to look inside needs the contract, which for a third party means fetching
descriptors out of band. The engine itself is unaffected — it never looks inside a payload, which
is the same discipline as ADR-0024 — but the debugging experience is worse than a self-describing
wire would give.

## Alternatives

**`google.protobuf.Any`.** Lost on cost for a benefit the design does not need: the type URL is
redundant with `procedure`, and the registry lookup is per message rather than per method.

**A separate `type_url` string beside `bytes`.** All of `Any`'s redundancy with none of its
tooling. Rejected.

**Adding the type only when a debug flag is set.** Rejected: an envelope field whose presence
depends on deployment configuration is a shape that varies per deployment, which ADR-0007 exists
to prevent.

## Evidence

- `api/interchange/transport/v1/envelope.proto` — `bytes payload = 3` on `Request`, with the
  reason in a comment; `bytes payload = 6` on `Response`; `bytes payload = 4` on `Frame`.
- `dispatch.go` — `Registry.Dispatch` decodes `req.Payload` with `CodecFor(req.Codec)` into
  `b.md.NewRequest()`, resolved from the procedure. There is no type-URL path.
- `engine/wire.go` — the engine frames and chunks the payload without ever decoding it.
- `docs/03-envelope.md` and `docs/08-decisions.md` — the envelope-payload decision row.
- `CONTRIBUTING.md` — names this among the comments worth writing: "why the envelope is `bytes`
  and not `Any`".
