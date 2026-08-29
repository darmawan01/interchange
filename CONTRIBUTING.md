# Contributing

The rules below are not style preferences. Each one exists because breaking it reintroduces the
failure class the project was built to remove.

## The eight gates

Held from phase 1, never relaxed. `make all` runs the ones a machine can check.

| Gate | Enforced by |
| --- | --- |
| The contract is edited first, always | convention + the drift gate |
| Generated output is committed and current | `ix verify` in CI |
| The configured chain runs identically on every transport | structure: nothing but `Registry.Dispatch` runs a chain |
| Every entry point sits behind the chain | review; there is no third category |
| A binding adapter imports no concrete message type | review, and `drivertest` — a driver that needs one cannot pass it |
| Core depends on no broker, router, policy engine, or auth module | `hack/depcheck.sh` in CI |
| Plugins sort before emitting | golden + determinism tests per plugin |
| A frontend is total or loud | never emit a partial contract |

## Where code goes

Core is the root module. It holds the IR, the envelope, the chain, dispatch, the message engine,
and the five extension-point interfaces — and nothing else. Everything with an opinion is a
module beside it:

```
.                interfaces, engine, chain, dispatch          protobuf + connect, nothing else
auth/            the (auth) annotation, authn, authz, RBAC     optional
errors/          the reason taxonomy                           optional
validate/        protovalidate field rules                     optional
driver/{memory,nats,mqtt,ws}/                                  one per broker
binding/{rpc,rest}/                                            one per protocol family
frontend/{dsl,openapi}/                                        one per source format
tools/           protoc-gen-bus, protoc-gen-cli
ix/              the CLI
examples/catalog/  the worked example, and the acceptance tests
```

If you find yourself adding an import to core's `go.mod`, stop. `hack/depcheck.sh` is an
allowlist, and adding a line to it is a design decision, not a build fix.

## Adding a transport

Implement six methods and an honest `Capabilities`, then run the suite:

```go
func TestConformance(t *testing.T) {
    drivertest.Run(t, func(t *testing.T) drivertest.Pair { /* your driver */ })
}
```

Two things to get right, both about honesty rather than code:

- **`Capabilities` is a claim the engine is entitled to believe.** A driver that reports
  `NativeReply: true` and does not route a reply is broken, not merely degraded.
- **If your driver needs a change to the engine, that is a finding.** Say so. Every later driver
  inherits a seam that was moved to accommodate one broker, and the fix belongs in the engine
  where every driver gets it — not in a special case in yours. A driver much bigger than ~150
  lines is usually this problem wearing a disguise.

## Adding an annotation

1. Claim the number in `docs/annotation-band.md` **first**. A PR without a row does not merge.
2. Put the proto in the module that consumes it. Core does not reserve space for an annotation it
   will never parse.
3. Never renumber. A renumbered option is a silently dropped annotation on every descriptor built
   before the change — and for an authorization option, that is a check that stops firing.

## Writing a plugin

| Rule | Why |
| --- | --- |
| Guard on `file.Generate` | Without it you emit code for `google/api/annotations.proto` |
| `strategy: all` | buf invokes a plugin once per directory by default; a cross-cutting registry needs the whole tree |
| Sort before emitting | Go map iteration is random, and a shuffled file fails the drift check every other run until nobody trusts the gate |
| Fail, don't warn | A warning in a build log is a warning nobody reads |
| Deterministic headers | No timestamps, no per-build version strings — same input, same bytes |

Every plugin ships a golden test and a determinism test (generate twice, compare bytes).

## Prose

Comments explain **why**, not what. If a comment restates the line under it, delete it. The
comments worth writing are the ones recording a decision someone will otherwise undo: why the
envelope is `bytes` and not `Any`, why replay suppression replays the cached response instead of
dropping the message, why the auth annotation lives in a module and not in core.

## Running everything

```bash
make plugins ix     # build the local plugins and the CLI
make depcheck       # the dependency rule
make vet test       # every module
make verify         # the drift gate — this is what CI runs
```
