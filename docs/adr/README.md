# Architecture decision records

Every decision that would be expensive to reverse, why it was made, and what it cost. One file
each, numbered, never renumbered — a superseded ADR is marked superseded and kept, because the
reasoning that was wrong is often more useful than the reasoning that was right.

Each record carries **Evidence**: the file, check or test that enforces the decision. A decision
with no evidence is a preference, and preferences drift.

## Format

```markdown
# ADR-NNNN — Title

**Status:** Accepted | Superseded by ADR-NNNN | Revisit when …
**Date:** YYYY-MM-DD · **Phase:** N

## Context      what forced a decision
## Decision     what was decided, in one paragraph
## Consequences what this buys and what it costs — both, honestly
## Alternatives what else was considered and why it lost
## Evidence     the file, test or check that enforces it
```

## The contract and the IR

| # | Decision |
| --- | --- |
| [0001](0001-filedescriptorset-is-the-ir.md) | `FileDescriptorSet` is the IR, not a bespoke AST |
| [0002](0002-proto-is-the-substrate.md) | Proto is the substrate, not the interface |
| [0003](0003-reserve-an-annotation-band.md) | Reserve an annotation band before the second annotation exists |
| [0004](0004-wire-json-breaking-rule.md) | `WIRE_JSON`, not `FILE`, for breaking-change detection |
| [0005](0005-mechanical-naming.md) | Naming is the derivation rule, so it is load-bearing |
| [0006](0006-service-level-transport-default.md) | A service-level transport default that a per-RPC annotation replaces |

## The envelope and the wire

| # | Decision |
| --- | --- |
| [0007](0007-the-envelope-shape-is-fixed.md) | The envelope shape is not pluggable |
| [0008](0008-bytes-not-any.md) | `bytes`, not `google.protobuf.Any` |
| [0009](0009-the-connect-procedure-string.md) | The procedure string is the Connect procedure string, verbatim |
| [0010](0010-runtime-envelope-is-not-the-wire-request.md) | The runtime `Envelope` is not the wire `Request` |
| [0011](0011-one-byte-wire-discriminator.md) | A one-byte discriminator in front of every envelope |
| [0012](0012-chunking-operates-on-framed-bytes.md) | Chunking operates on framed bytes, not on the message |
| [0013](0013-sequence-numbers-for-replay.md) | A monotonic sequence per stream |
| [0014](0014-replay-the-cached-response.md) | A redelivery replays the cached response rather than being dropped |

## Dispatch, the chain, and cross-cutting concerns

| # | Decision |
| --- | --- |
| [0015](0015-chain-symmetry-is-the-one-guarantee.md) | Chain symmetry is core's only behavioural invariant |
| [0016](0016-chain-symmetry-is-structural.md) | Chain symmetry is structural: no binding holds a chain |
| [0017](0017-named-anchors-not-positions.md) | Named anchors, not positional ordering |
| [0018](0018-three-stock-interceptors.md) | Core ships three interceptors and no more |
| [0019](0019-authorization-is-a-module.md) | Authorization is a module, not a core requirement |
| [0020](0020-absent-is-not-public.md) | Absent ≠ public, and an unwired resolver denies |
| [0021](0021-core-moves-errors-without-meaning.md) | Core moves `code`, `message` and `reason` and assigns them no meaning |

## Transports

| # | Decision |
| --- | --- |
| [0022](0022-capabilities-is-data.md) | `Capabilities` is data; the engine has no transport switch |
| [0023](0023-module-per-adapter.md) | One module per adapter; core depends on no broker |
| [0024](0024-adapters-see-no-concrete-types.md) | A binding adapter may not import a concrete message type |
| [0025](0025-acknowledge-on-completion.md) | Acknowledge on completion, not on delivery |
| [0026](0026-mqtt-5-only.md) | MQTT 5 only |
| [0027](0027-two-nats-tiers.md) | Core NATS and JetStream are two drivers, not one with a flag |
| [0028](0028-websocket-is-one-channel.md) | WebSocket has one channel, so the procedure lives in the envelope |
| [0029](0029-the-memory-driver-is-real.md) | The in-process driver is a real driver, not a mock |
| [0030](0030-drivertest-is-public.md) | The conformance suite is public API |
| [0031](0031-reply-bounded-by-caller-deadline.md) | A reply is bounded by the caller's deadline |
| [0032](0032-inbound-header-is-always-merged.md) | Inbound metadata is merged whatever the transport claims |

## Codegen

| # | Decision |
| --- | --- |
| [0033](0033-generated-output-is-committed.md) | Generated output is committed and drift-gated |
| [0034](0034-no-generated-subscribers.md) | `protoc-gen-bus` emits no subscribers |
| [0035](0035-read-annotations-through-core.md) | Annotations are read through core, never off `Descriptor.Options()` |
| [0036](0036-determinism-is-a-test.md) | Plugin determinism is a test, not a convention |
| [0037](0037-bus-clients-carry-an-explicit-deadline.md) | Bus clients generate an explicit-deadline signature beside the plain one |
| [0038](0038-the-cli-reports-its-coverage.md) | The generated CLI reports its own coverage |

## Frontends

| # | Decision |
| --- | --- |
| [0039](0039-total-or-loud.md) | Total, or loud |
| [0040](0040-round-tripping-is-not-a-goal.md) | Round-tripping is not a goal |
| [0041](0041-the-emitted-proto-is-the-artifact.md) | The emitted proto is the artifact |
| [0042](0042-the-sidecar-is-the-universal-fallback.md) | Every frontend needs a home for annotations; the sidecar is the fallback |
| [0043](0043-explicit-field-numbers-in-the-dsl.md) | The DSL requires explicit field numbers |
| [0044](0044-inline-and-sidecar-conflict.md) | Inline and sidecar annotations conflict rather than take precedence |
| [0045](0045-frontends-never-read-the-filesystem.md) | A frontend never reads the filesystem |

## Tooling and distribution

| # | Decision |
| --- | --- |
| [0046](0046-ix-shells-out-to-buf.md) | `ix` shells out to buf rather than embedding it |
| [0047](0047-interchange-yaml-describes-a-project.md) | `interchange.yaml` describes a project, not a monorepo of them |
| [0048](0048-codegen-does-not-touch-the-network.md) | Codegen does not touch the network |
| [0049](0049-the-npm-channel-is-first-class.md) | The npm channel is first-class |
| [0050](0050-internal-means-public-bindings-skip-it.md) | `(internal)` means public bindings skip it, not that it is unreachable |

## Scope

| # | Decision |
| --- | --- |
| [0051](0051-streaming-is-deferred.md) | Streaming is deferred |
| [0052](0052-no-hand-written-sdk-wrapper.md) | No hand-written SDK wrapper over generated clients |
| [0053](0053-the-browser-does-not-touch-the-bus.md) | The browser does not touch the bus |
| [0054](0054-ix-does-not-host-a-registry.md) | `ix` talks to a registry; it is not one |
