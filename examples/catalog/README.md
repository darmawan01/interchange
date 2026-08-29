# `examples/catalog` — the worked example

One service, five RPCs, four roads, one handler. Everything else in this repository exists to make
this directory small.

```
go test ./...                       # the acceptance suite, including a real in-process NATS broker
go run ./cmd/catalogd --nats nats://localhost:4222
go run ./cmd/catalogctl catalog providers --tenant-id acme --token reader-token
npm --prefix . ci && npm --prefix . run typecheck    # the front end's types come from the contract
```

## What an adopter writes

Two files. That is the claim, and it is checkable by `ls`:

| File | What |
| --- | --- |
| [`api/catalog/v1/catalog.proto`](api/catalog/v1/catalog.proto) | the contract: five RPCs carrying `google.api.http`, `idempotency_level`, `(transports)`, `(internal)`, `(auth)` and `(cli)` |
| [`server.go`](server.go) | the handler: an ordinary struct implementing the generated interface, over an in-memory store |

Plus [`wire.go`](wire.go), the composition root — about forty lines of it are the chain, and the
rest is a struct holding a registry.

`server.go` names no transport type, no envelope, no metadata key and no broker. It could not tell
you which road a call arrived on, which is the point: `TestTheInterceptorChainCameAlongUnchanged`
moves a call from HTTP to NATS and the handler does not change, recompile or notice.

## What is generated

`buf generate --template buf.gen.catalog.yaml` (from the repo root, after `make plugins`) writes
everything under `gen/`. **It is committed and under the drift gate** — `ix verify` regenerates and
fails on a diff.

| Output | Plugin | What it is |
| --- | --- | --- |
| `gen/go/catalog/v1/*.pb.go` | `buf.build/protocolbuffers/go` | message types |
| `gen/go/catalog/v1/catalogv1connect/` | `buf.build/connectrpc/go` | the Connect client the acceptance tests and any browser-facing SDK call |
| `gen/go/catalog/v1/catalogv1bus/` | `tools/cmd/protoc-gen-bus` | the `*interchange.ServiceDesc`, the `CatalogServiceHandler` interface, the typed bus client |
| `gen/go/catalog/v1/catalogv1cli/` | `tools/cmd/protoc-gen-cli` | the Cobra tree, built with `require_annotation=true` |
| `gen/go/authz/permissions.authz.go` | `auth/cmd/protoc-gen-authz` | the sorted procedure → permission table, built with `known_atoms` |
| `gen/ts/**` | `buf.build/bufbuild/es` | the front end's types and the `CatalogService` descriptor |

**There is no `connectrpc/es` plugin in the template.** Connect-ES v2 removed its code generator:
protobuf-es v2 emits the service descriptor into `catalog_pb.ts` and `createClient(CatalogService,
transport)` consumes it directly, exactly as [`docs/05`](../../docs/05-one-call-two-transports.md)
shows. `@connectrpc/protoc-gen-connect-es` stops at 1.7.0 and is protobuf-es v1 era; using it would
mean pinning the whole TypeScript surface a major version back.

The template's managed-mode block is the interesting part. Each `override` carries a `module:`
selector, so the example's own files land in
`github.com/darmawan01/interchange/examples/catalog/gen/go` while `interchange/auth/v1/auth.proto`
still resolves to the `/auth` module's committed package — and googleapis is `disable`d entirely,
because its generated Go is a published module and rewriting its prefix would point at import paths
nobody publishes. `include_imports` is set on the TypeScript plugin **only**: a protobuf-es file
names its imports' descriptors at runtime, so they have to be emitted alongside, while doing the
same for Go would compile a second copy of `transports.proto` into the process and panic at init.

## The chain, and why it is in that order

Configured once in `wire.go`, handed to `Register` once, and never handed to a binding:

```
telemetry   core. One observation per call, labelled by procedure.
errors      inside telemetry and OUTSIDE recover: everything it normalises must be below it,
            including the internal error recover makes out of a panic.
recover     core. On a bus a panic takes the subscriber with it.
deadline    core. The one thing HTTP gives free and a bus does not.
authn       who is calling. Before validate, so an anonymous caller is turned away without
            spending CPU on their payload.
validate    before authz, because authz reads tenant_id off the message and a permission
            decision should not run against a request nobody checked. The example contract
            declares no protovalidate rules yet, so today this stage is a no-op.
authz       may they do this, to this tenant.
```

Note the two deviations from the task sketch, both deliberate: `errors` goes `After(telemetry)`
rather than being appended innermost (per `errors/README.md`; appended innermost it never sees a
panic), and `validate` sits before `authz` rather than after (per `validate/README.md`).

## The acceptance tests

Every test in [`acceptance_test.go`](acceptance_test.go) is named after the BUILD-PLAN exit
criterion it closes, with the criterion's exact wording quoted above it. Three roads share one
registry throughout: the Connect binding over `httptest` driven by the **generated** Connect client,
the in-process memory bus, and a **real NATS broker started inside the test binary** — no docker.

| Test | BUILD-PLAN criterion | Phase |
| --- | --- | --- |
| `TestChainConfiguredOnceRunsInTheSameOrderOnEveryRegisteredBinding` | "A chain configured once demonstrably runs in the same order on every registered binding." | 2 |
| `TestAServiceWithAnEmptyChainWorks` | "Core builds and passes its tests with the `/auth` module absent. A service with an empty chain works." | 2 |
| `TestGeneratedPermissionTableMatchesTheRuntimeInterceptor` | *(module)* "CI can be configured to fail on an RPC with no `(auth)` annotation." — the build-time gate and the runtime check agree, procedure for procedure | 2 |
| `TestTheTableIsEnforcedAtRuntime` | the same criterion, behaviourally: one holder and one non-holder per verb | 2 |
| `TestTheGeneratedCLICoversTheContract` | "`ix init` produces a working project with a generated typed client." — the CLI half; also drives the generated tree against the running service | 1 |
| `TestAFrontEndImportsItsTypesFromGeneratedOutput` | "A front end imports its types from generated output, not a hand-written file." | 1 |
| `TestTheInterceptorChainCameAlongUnchanged` | "One low-risk service-to-service call, already on HTTP, is moved to the bus." + "**The interceptor chain came along unchanged.**" | 4 |
| `TestSameProcedureStringInAuthzCheckMetricsLabelsAndTraceSpanOnBothRoads` | "The same procedure string appears in the authz check, the metrics labels and the trace span on both roads." | 4 |
| `TestAuthorizationFiresOnTheBusCall` | "Authorization demonstrably fires on the bus call." | 4 |
| `TestTheTransportsAnnotationIsLoadBearing` | cross-phase: the `(transports)` and `(internal)` annotations decide reachability, or they are decoration | — |
| `TestEveryReasonThisServiceRaisesIsDeclared` | cross-phase: one closed taxonomy, one reason string on every road | — |

Three of those need a note.

**The chain-order test does not use the tracer pattern from `internal/conformance` unchanged.**
That one *replaces* each real stage with a probe, which is right for core (it has nothing to
protect) and wrong here: replacing `authz` would mean the authorization this suite is about never
fires. Instead a probe is inserted *in front of* every real stage by anchor, plus one innermost, and
the recorded probe sequence is compared across all three roads. The test also asserts the chain is
the seven-stage one `wire.go` composes, so it cannot pass vacuously the day someone empties it — an
empty chain is trivially identical everywhere.

**"A service with an empty chain works" needs its own `Registry`**, not the same one.
`Registry.Register` rejects a second registration of the same service, deliberately, because a
shadowed handler is a production-only bug. So "serve the same registry with `interchange.Chain()`"
is not expressible — and should not be: a chain is bound at registration, which is precisely what
makes it impossible for a binding to vary one. The test registers the same generated `ServiceDesc`
and the same handler in a fresh registry and serves it over HTTP and NATS with no credential
anywhere.

**`Reconcile` is reachable on the bus, by design.** `(internal) = true` keeps it off every *public*
binding — `rpc.Binding.Mount` skips it — while the engine still subscribes it, because that is what
"internal" means and not one word more. The test asserts it is refused over HTTP and served over
NATS to a platform workload.

## Credentials in the example

`wire.go` exports a static token table. A real deployment verifies a JWT or calls an identity
service behind the same `auth.Authenticator`; swapping it changes nothing else.

| Credential | Kind | Tenant | Grants |
| --- | --- | --- | --- |
| `reader-token` (bearer) | session | acme | `providers.read` |
| `writer-token` (bearer) | session | acme | `providers.read`, `.create`, `.edit` |
| `globex-token` (bearer) | session | globex | — denied in acme by the tenant scoper |
| `api-key-reader-acme` (`x-api-key`) | api key | acme | `providers.read` |
| `workload-read-key` (`x-api-key`) | workload | acme | `providers.read` |
| `workload-sync-key` (`x-api-key`) | workload | acme | `providers.read`, `.edit` |
| `workload-plat-key` (`x-api-key`) | workload | cross-tenant | `providers.*` |

## Not here yet

- **REST.** `binding/rest` is a `go.mod` and a test fixture with no Go files as of this commit, so
  there is nothing to mount. `wire.go` marks the spot; four of the five RPCs already declare
  `TRANSPORT_REST` and carry `google.api.http`, so landing it is a mount and not a contract change.
- **A reason enum in the contract.** `catalog.Reasons()` uses `errors.SetOf`, the escape hatch. A
  real service declares `enum CatalogReason` in its own `.proto` and passes
  `errors.EnumSet(...)`, which is what lets the TypeScript client enumerate the same list.
- **`protovalidate` rules.** The `validate` stage is wired and inert. It is wired anyway, because
  the day a rule is added is not the day anyone should be editing the chain.
