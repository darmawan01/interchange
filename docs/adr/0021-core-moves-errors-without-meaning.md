# ADR-0021 — Core moves `code`, `message` and `reason` and assigns them no meaning

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

A client has to branch on something. If that something is the message, then rewording `"no such
provider"` to `"provider not found"` is a breaking change for every caller and nobody notices until
the pager goes off. `code` alone is too coarse: `not_found` cannot distinguish "no such provider"
from "no such region", and those are different recoveries.

So the branch point has to be a third field — short, stable, machine-readable. But the *contents*
of that field are a product's business, not a dispatch layer's. Core cannot know that
`PROVIDER_NOT_FOUND` exists, and it must not be the place a new reason gets added.

At the same time, the field has to exist in the envelope from day one. Adding an error field to a
wire format later means every driver and every non-Go client has to be taught about it.

## Decision

The envelope reserves `code`, `message` and `reason`, and core carries them: `interchange.Error{Code,
Message, Reason, Meta}`, `CodeOf`, `MessageOf`, `ReasonOf`, `MetaOf`, and the projection into each
binding's error form. **Core assigns `Reason` no meaning — it moves it.** There is no enum in core,
no registry of valid reasons, no validation.

The taxonomy is `/errors`, an optional module: a closed, append-only reason enum in the contract;
an interceptor that enforces membership; a pluggable `Mapper` from your own error types; and the
RFC 9457 `problem+json` projection. A service can install it, bring its own, or use none and put
raw strings in `Reason`.

Core's `Code` space is the gRPC/Connect numbering, so projecting it onto an HTTP status is a table
lookup rather than a translation — and that table, in `/errors`, is **lifted from connect-go's
source** rather than written from memory, so the REST binding and the Connect binding cannot
disagree about what `failed_precondition` is.

## Consequences

An adopter with an existing error taxonomy keeps it and still gets one reason string on every road.
`/errors` can add reasons, change its enforcement policy or ship a new mapper without touching core
or the wire format.

The costs: core cannot validate a reason, so a typo travels. `/errors` exists partly to close that
— an unknown reason panics under `go test` and is rewritten to the code's stock reason at runtime,
because an unregistered reason on the wire breaks the only promise the module makes. Without the
module, nothing checks. The closed-enum discipline also means adding a reason is a contract change
with a regeneration, which is friction, and deliberately so: a reason a client cannot enumerate from
the proto is a string it discovers in production.

`/errors`'s `Stage` also has a position requirement — inside `telemetry`, **outside** `recover` —
because everything it normalises has to be below it, including the internal error `recover` makes
out of a panic. That is a real footgun that named anchors (ADR-0017) make expressible but do not
make automatic.

## Alternatives

**Put the enum in core.** Then every service shares one taxonomy and core owns the append. It also
makes core the place a product-specific reason is added, which is where this decision started.

**Branch on the message.** Turns copy edits into breaking changes.

**Code only.** Too coarse to distinguish the recoveries a client actually has.

**Write our own code → HTTP status table.** It would drift from connect-go's, and then two roads
out of one dispatch would answer the same handler error with two different statuses.

## Evidence

- `error.go` — `Error`, the canonical code space, and the comment that says it plainly: "Core
  assigns Reason no meaning -- it moves it."
- `api/interchange/transport/v1/envelope.proto` — `code`, `message`, `reason` reserved on
  `Response`; `api/interchange/common/v1/types.proto` — the `Problem` projection.
- `errors/README.md` — "Why the reason and not the message", the enforcement policies, and the
  code → status table with its provenance.
- `errors/problem.go` — the table, with the comment recording that it is connect-go's.
- `errors/foursurfaces_test.go` `TestOneErrorFourSurfaces` — the four surfaces as an executable
  claim rather than a table, from **one** registry and one chain: the handler's
  `NotFound("PROVIDER_NOT_FOUND", …)`; the canonical `Response{code: 5, reason: …}` over `engine` +
  `driver/memory`; a 404 carrying the reason in the `Ix-Reason` header, read with plain `net/http`
  rather than a Connect client; and the `problem+json` projection carrying the same reason, title
  and status. It also asserts that the
  status connect chose equals `errors.HTTPStatus(CodeNotFound)`, which is the "two roads cannot
  disagree" claim itself.
- `internal/conformance/symmetry_test.go` `TestErrorIsTheSameOnBothRoads`.
- `binding/rpc/rpc.go` `ErrorReasonHeader` / `toConnectError`, and `engine/server.go`
  `errorResponse` — core moving the three fields, and interpreting none of them.
