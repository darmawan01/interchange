# ADR-0016 — Chain symmetry is structural: no binding holds a chain

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

ADR-0015 states the guarantee. Stating it is the easy half. Every framework that has ever promised
"the same middleware everywhere" promised it in a paragraph, and then a transport adapter grew a
fast path, or a driver added one stage of its own for a good local reason, and the promise became a
convention that a code review has to catch.

A rule that adapters are *asked* to respect is a rule that breaks — usually in the adapter written
by somebody who never read the paragraph, six months after it was written. The question this ADR
answers is not whether the chain is the same everywhere but **what makes it impossible for it not
to be.**

## Decision

The chain is unreachable from the transport layer. `*ChainSpec` is accepted in exactly one place —
`Registry.Register` — and folded there, once, into the bound method's `call`. `ChainSpec.Wrap` is
called nowhere else in a serving path. Every binding and every driver reaches a handler through
`Registry.Dispatch`, which is the only place an interceptor runs.

Concretely:

- **No driver signature carries a chain.** The `Driver` interface is six methods over procedure
  strings, bytes and metadata (`driver.go`). There is no parameter, field or callback through which
  a driver could be handed one, so a driver cannot add, skip or reorder a stage — not because it is
  forbidden to, but because it has nothing to do it with.
- **The engine never sees one.** `engine/` contains no reference to `ChainSpec` at all. It
  reassembles frames, dedupes replays, builds an `interchange.Envelope` and calls
  `s.reg.Dispatch(ctx, env)`.
- **A binding forwards, and stores nothing.** `rpc.Binding.Register` and `rest.Binding.Register`
  take a `*ChainSpec` only to pass it to `Registry.Register`; neither `Binding` struct has a field
  for it, and neither ever calls `Wrap`. `Mount` — the method used when two bindings serve one
  registry — takes no chain at all, because by then the chain is already inside the registry.

## Consequences

Symmetry survives an adapter its authors never coordinated with. A third party writing an MQTT or
Kafka driver cannot break the invariant even deliberately without editing core, which is a diff a
reviewer sees.

It also means the arrangement is fixed at `Register` time and is per service, not per road: there
is no supported way to run one extra stage on the public HTTP road and not on the internal bus
road. That is a genuine cost — it is occasionally what somebody wants — and the answer is a stage
that inspects the envelope's metadata, not a second chain.

`Registry.Register` rejects a duplicate service or a duplicate procedure rather than overwriting,
so "register once, mount on each" is the only way to serve one service over two bindings. The
awkwardness is deliberate: a shadowed handler is a bug that only shows up in production.

## Alternatives

**Give each binding its own chain and test that they match.** A test asserts what is true today;
the type system asserts what stays true. And the test can only compare the chains it knows about.

**A documented rule plus review.** This is what the ADR exists to reject. See the `driver/ws`
README's account of the metadata problem: the driver's first attempt rewrote envelopes below
dispatch precisely because touching the chain was — rightly — not available to it. The seam bent
the driver into a workaround instead of letting it break the invariant, which is the mechanism
working (ADR-0032).

## Evidence

- `dispatch.go` — `Registry.Register` is the only caller of `ChainSpec.Wrap`; `boundMethod.call` is
  private; `Dispatch` is the single entry point. The doc comment on `Registry` states the rule.
- `driver.go` — the `Driver` interface: no chain, no interceptor, no handler.
- `engine/server.go` — `grep ChainSpec engine/` returns nothing; the server's path to a handler is
  `s.reg.Dispatch`.
- `binding/rpc/rpc.go` (package doc: "Nothing here holds a chain") and `binding/rest/rest.go` —
  `Register` forwards, `Mount` takes no chain.
- `internal/conformance/symmetry_test.go` `TestChainSymmetry`; `binding/rest/rest_test.go`
  `TestChainSymmetry`; `auth/e2e_test.go` `TestAuthorizationFiresOnBothRoads`.
