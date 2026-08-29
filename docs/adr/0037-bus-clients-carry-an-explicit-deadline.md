# ADR-0037 — Bus clients generate an explicit-deadline signature beside the plain one

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

§08 left this open: *do generated bus clients block?* The generated client for a bus RPC is a
synchronous Go method — `ListProviders(ctx, req) (*ListProvidersResponse, error)` — and it looks
exactly like a local call. It is not one. It publishes an envelope, waits for a correlated reply
over a broker, and can wait forever if nothing subscribes to that procedure. A caller who reads the
signature and does not read the documentation has no way to see that a timeout is the thing they
most need to think about.

The obvious alternatives both cost something. Return a future and the mental model people actually
want disappears. Require a `time.Duration` argument on every call and the generated client is
awkward everywhere, including in tests and CLIs where `ctx` already carries a deadline.

## Decision

Both signatures are generated. `protoc-gen-bus` emits `Method(ctx, req)`, which blocks until the
reply arrives or the context is done, and `MethodWithin(ctx, timeout, req)`, which puts the network
deadline in the signature where a caller cannot miss it. A per-client `With<Service>Timeout` option
sets a default for the plain form. The generated doc comment names the trap directly: "A plain
method blocks until the reply arrives or the context is done, and that is the trap: the signature
looks local and the call is not."

## Consequences

The mental model people want is available, and the honest signature is one keystroke away and
impossible to miss when reading the generated file. A team that wants the plain form everywhere can
set a default timeout once at construction, so the choice is made deliberately in one place rather
than forgotten in every call site.

The cost is two methods per bus RPC — the generated file is roughly twice as long in its client
half, and every RPC's documentation appears twice. A reviewer scanning the generated client sees
duplication that is intentional but does not look intentional at a glance.

There is also a soft cost in guidance: three ways to bound a call (the context, the client default,
the `Within` argument) means a team has to pick one. The generated comment says so — "Pick one
deliberately: the engine's own client timeout is the last line of defence, not the first."

Only methods that actually travel a broker get either form. A generated call to a procedure nothing
subscribes to is a timeout with a confident-looking signature, so `emitBusClient` skips methods that
are not on `TRANSPORT_BUS`, `_MQTT` or `_WS`, and services with no such method get no client at all.

## Alternatives

**Plain signature only.** Rejected: it is the trap §08 named, and no amount of documentation fixes a
signature that lies about its cost.

**`Within` only.** Rejected: it makes every call site noisier, including the ones where `ctx`
already carries a deadline set by an inbound request, and it makes the generated client unpleasant
to use from a CLI or a test.

**Return a future or a channel.** Rejected: it changes the calling convention for bus RPCs only, so
the same handler reached over Connect and over the bus would have two different client shapes —
against the project's central claim that the transport is a deployment decision.

## Evidence

- `tools/cmd/protoc-gen-bus/bus.go` — `emitBusClient` emits the
  plain method, the `…Within` method, the `With<Service>Timeout` option, and the client doc comment
  quoted above.
- `tools/testdata/gen/interchange/fixture/v1/fixturev1bus/` —
  the committed golden output, where both forms are visible per RPC.
- `tools/internal/gentest/generated_test.go` — `TestBusClient`
  drives the generated client over the in-process driver.
- `tools/README.md` §"Do generated bus clients block? (docs/08)".
- `docs/08-decisions.md` §"Resolved by building it" records the
  open question and its answer.

See ADR-0031: a reply is bounded by the caller's deadline, which is the server-side half of the same
concern.
