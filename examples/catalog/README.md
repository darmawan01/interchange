# `examples/catalog` — the worked example

One service, five RPCs, four roads, one handler. Everything else in this repository exists to make
this directory small.

```
go test ./...                       # the acceptance suite, including a real in-process NATS broker
ix generate && ix verify            # the drift gate, driven from interchange.yaml
go run ./cmd/catalogd --nats nats://localhost:4222
go run ./cmd/catalogctl catalog providers --tenant-id acme --token reader-token
curl -H "Authorization: Bearer reader-token" \
  localhost:8080/v1/catalog/providers?tenant_id=acme          # the partner surface
npm --prefix . ci && npm --prefix . run typecheck    # the front end's types come from the contract
```

Four roads out of one registry: Connect over HTTP, REST over the same listener under `/v1/`, and
the message engine over an in-process bus or over NATS. `wire.go` registers the service **once**.

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

`openapi.json` is emitted separately, by `catalog.OpenAPI()` over `binding/rest/openapi`, and is
committed with a drift gate of its own in `TestTheEmittedOpenAPIMatchesWhatPartnersCall`. It is a
partner-facing artifact: a path that changes should be visible in a diff before it is visible in
someone else's logs.

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
criterion it closes, with the criterion's exact wording quoted above it. Four roads share one
registry throughout: the Connect binding over `httptest` driven by the **generated** Connect client,
the REST binding transcoded off the same registry, the in-process memory bus, and a **real NATS
broker started inside the test binary** — no docker.

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
| `TestAnExistingRESTConsumerIsServedByTheTranscoder` | "An existing REST consumer is served by the transcoder." + "Old hand-written handlers are **deleted** as each path is covered" | 3 |
| `TestPerSurfaceJSONCasingCamelCaseOnRPCSnakeCaseOnREST` | "Per-surface JSON casing, written down: camelCase on RPC, snake_case on REST." | 3 |
| `TestTheEmittedOpenAPIMatchesWhatPartnersCall` | "The emitted OpenAPI matches what partners already call, or the migration is explicitly versioned." | 3 |
| `TestTheTransportsAnnotationIsLoadBearing` | cross-phase: the `(transports)` and `(internal)` annotations decide reachability, or they are decoration | — |
| `TestEveryReasonThisServiceRaisesIsDeclared` | cross-phase: one closed taxonomy, one reason string on every road | — |

Three of those need a note.

**The chain-order test does not use the tracer pattern from `internal/conformance` unchanged.**
That one *replaces* each real stage with a probe, which is right for core (it has nothing to
protect) and wrong here: replacing `authz` would mean the authorization this suite is about never
fires. Instead a probe is inserted *in front of* every real stage by anchor, plus one innermost, and
the recorded probe sequence is compared across all four roads. The test also asserts the chain is
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
binding — `rpc.Binding.Mount` and `rest.Binding.Mount` both skip it, and it is absent from
`openapi.json` — while the engine still subscribes it, because that is what "internal" means and
not one word more. The test asserts it is refused on both HTTP roads and served over NATS to a
platform workload. **`ix lint` disagrees**; see below.

**The REST road gets its own listener in one subtest.** The transcoder also answers the Connect
protocol on the procedure path, so `TestTheTransportsAnnotationIsLoadBearing` mounts `svc.REST`
alone and POSTs `SyncProvider` and `Reconcile` at it: both `404`, while `ListProviders` at the same
listener answers `200`. That is the method-set filter being load-bearing rather than the listener
being broken.

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

## `interchange.yaml` and `ix`

`interchange.yaml` declares every generator the project runs, and `ix` synthesizes its own buf
template from it. It is the source of truth; `buf.gen.catalog.yaml` at the repo root is the
repo-wide convention (`buf.gen.core.yaml`, `buf.gen.auth.yaml`, …) and says the same thing plus the
TypeScript target.

Verified end to end against this directory:

| Command | Result |
| --- | --- |
| `ix generate` | reproduces the committed tree **byte for byte**, Go and TypeScript |
| `ix describe CatalogService.ListProviders` | prints the four roads, the REST URI, the queue group, the permission atom, the tenant field and the CLI path |
| `ix verify` | ✓ on a clean tree, over six targets; mutate one byte of any generated file and it names that file and exits 1. A real gate. |
| `ix lint` | ✓ — 5 RPCs, 8 extensions, band checked |
| `ix doctor` | ✓ |

## What building this found in `ix`

Every one of these is fixed. They are recorded because the *kind* of defect is worth knowing, not
because any is open — and because the example existing is what found them.

| What was wrong | Where it went |
| --- | --- |
| `ix lint` errored on `(internal)` combined with any transport, which would have failed CI on this example's `Reconcile` | The rule flags only the public roads — rpc, rest and ws. A bus is not a public binding: `rpc.Binding.Mount` and `rest.Binding.Mount` skip `Internal` while `engine.Server.Plan` deliberately does not, so `internal` + `bus` is how an RPC is made reachable service-to-service and nowhere else. [ADR-0050](../../docs/adr/0050-internal-means-public-bindings-skip-it.md) |
| `interchange.yaml` could not express buf's per-plugin `include_imports`, so the TypeScript generator lived only in the buf template and `gen/ts` sat outside the drift gate | `include_imports` is a field on `Generate`. `ix verify` covers six targets here and fails on a one-byte change to a `.ts` file — generated output outside the gate is the one place it must never sit |
| `ix` read any plugin path containing a slash as a *remote* reference, so `node_modules/.bin/protoc-gen-es` produced "the server hosted at that remote is unavailable" | A remote is host-qualified: its first path element contains a dot |
| `ix doctor` looked for `buf.yaml` beside `interchange.yaml`, reporting a broken setup for a project nested inside a workspace | It walks up, the way buf does |
| `ix verify` listed a differing file once per containing output directory, so one stale file read as two problems | Reported once |

## Not here yet

- **A reason enum in the contract.** `catalog.Reasons()` uses `errors.SetOf`, the escape hatch. A
  real service declares `enum CatalogReason` in its own `.proto` and passes
  `errors.EnumSet(...)`, which is what lets the TypeScript client enumerate the same list.
- **`protovalidate` rules.** The `validate` stage is wired and inert. It is wired anyway, because
  the day a rule is added is not the day anyone should be editing the chain.
