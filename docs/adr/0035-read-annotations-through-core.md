# ADR-0035 — Annotations are read through core, never off `Descriptor.Options()`

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

This one was discovered, not designed. The obvious way to read a custom option in a protoc plugin
is the way every tutorial writes it:

```go
opts := m.Desc.Options().(*descriptorpb.MethodOptions)
a, _ := proto.GetExtension(opts, authv1.E_Auth).(*authv1.AuthOptions)
```

That works when the descriptor came from generated Go that is linked into the binary, because the
extension fields hold concrete values of the exact type `GetExtension` is asked for. It stops
working the moment the descriptor came from anywhere else. A descriptor built by protocompile, by
`protodesc` with a dynamic resolver, or — by construction — by *every schema frontend* carries its
custom options as `dynamicpb` values, or leaves them sitting in unknown fields. Against those,
`proto.GetExtension` with a concrete extension type either panics or returns the zero value.

The zero value is the dangerous half. A panic is a build that fails loudly. A zero value is an
annotation that reads as absent: the permission table quietly omits an RPC, or the CLI quietly
drops a command, in a build that compiles and a test suite that passes. Since phase 6 exists
specifically to produce descriptors this way, every plugin in the repository was one frontend away
from silently losing security posture.

## Decision

Core owns the safe read. `interchange.ResolveOptions(d, out)` marshals a descriptor's options and
unmarshals them again against `protoregistry.GlobalTypes`, which re-binds the extension fields to
the concrete types the caller's `GetExtension` expects. There is no cheaper way — the values in the
source message are of the wrong Go type by construction. `MethodOptions`, `ServiceOptions` and
`FieldOptions` wrap it with a per-descriptor cache. Every plugin and every module that reads an
annotation goes through this, and `Descriptor.Options()` is never cast directly. Core still does not
know what any annotation means; it knows how to hand one back intact.

## Consequences

An annotation reads the same whether the descriptor came from linked generated Go, from
protocompile inside the DSL frontend, or from a `FileDescriptorSet` on disk. That is what makes the
§09 claim — a DSL user gets the same contract a proto user gets — true rather than hopeful, and
`frontend/dsl/roundtrip_test.go`'s `TestRoundTripThroughTheToolchain` asserts exactly that on the
emitted descriptors.

Costs: a marshal and a re-parse per descriptor, which is why the result is cached and documented as
"must not be mutated"; a global-registry dependency, so an extension type that is not linked into
the reading binary still resolves to nothing (which is why frontends take `Options.Deps`, ADR-0045);
and a rule that has to be taught, because the wrong way is the one every example on the internet
shows.

One honest wrinkle: the rule is implemented twice. `tools/cmd/protoc-gen-bus` and
`tools/cmd/protoc-gen-cli` call `interchange.MethodOptions`/`ServiceOptions`. The `/auth` module
reads through its own `auth.AnnotationOf`, which hand-rolls the identical marshal-and-re-parse
rather than calling core's helper, and is what `protoc-gen-authz` goes through. Same discipline,
same reasoning in the comment, two copies of the code.

`cachedOptions` deliberately does not cache a descriptor whose options fail to round-trip. Returning
empty options *and* caching them would be the same silent-absence failure this exists to prevent, so
the failure stays visible on the next read.

## Alternatives

**Cast `Descriptor.Options()` directly.** The status quo it replaced, and the thing the mutation
test reverts to. It loses because its failure mode is silence.

**Pre-resolve options in the plugin test harness.** Explicitly rejected: a harness that resolved
options before handing them to the plugin would hide a plugin reading them the fragile way, which
is the one thing the test needs to catch. `tools/internal/plugintest` does not pre-resolve.

**Give each module its own resolver.** What the `/auth` module actually did first. The same ten
lines appearing in a third consumer is what moved it into core — the same argument that later moved
`DepFiles` there (ADR-0045).

## Evidence

`options.go` — `ResolveOptions`, `MethodOptions`,
`ServiceOptions`, `FieldOptions`, with the "zero value is the dangerous half" reasoning in the doc
comment.

The strongest evidence in the repository is a mutation test, carried by both plugins under the same
name: **`TestOptionsSurviveAnUnresolvedDescriptor`**.

- `tools/cmd/protoc-gen-bus/bus_test.go` — it takes the fixture
  request, and for every file being generated strips each service's and each method's options back
  through `proto.UnmarshalOptions{Resolver: &protoregistry.Types{}}` — a resolver that knows no
  extensions, which is exactly how a descriptor built anywhere but here arrives. It then runs the
  plugin and asserts the generated bytes are identical to the unmutated run, failing with
  *"an unresolved descriptor produced different code; an annotation was read as absent"*.
- `tools/cmd/protoc-gen-cli/cli_test.go` — the same mutation
  against method options, with the note that for the CLI a missing `(command)` would not fail the
  build: it would quietly drop the command, and under `require_annotation=true` it would fail a
  build that is correct.

Commit `2ecd92e` records that reverting either plugin to the direct cast fails these tests — the
silent failure caught by a test rather than by an incident. `docs/07-codegen.md` carries it as the
sixth rule for plugin authors.
