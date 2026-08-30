# Interchange

**One contract, every road.** Define your service once — in protobuf, or in whatever format your
team already uses — and Interchange generates typed clients and server bindings for HTTP, REST,
NATS, MQTT and WebSocket. The browser, the mobile app, the peer service and the async worker all
call the same declared method, through the same middleware, into the same handler.

> **Status: built, and honest about what that means.** Working name. All six phases are
> implemented, with 229 tests across thirteen modules; `make verify` regenerates every contract and
> fails on drift. What it has not had is production traffic. See [Maturity](#maturity) before
> planning against any part of it.

---

## What it is for

Teams end up defining the same operation three or four times: an OpenAPI path for the website, a
`.proto` for peer services, a hand-agreed JSON shape for the worker, and something bespoke for
mobile. Nothing mechanical connects them, so a renamed field is caught by a **runtime failure
rather than a build failure** — and authorization gets enforced on one road and forgotten on
another.

Interchange makes the transport a **deployment decision** rather than an **API decision**.

| You have | You get |
| --- | --- |
| A website | Typed TS client over HTTP, no hand-written interfaces |
| A mobile app | Generated SDK from the same contract |
| Internal services | NATS request/reply through the same chain |
| A device fleet | MQTT 5, same contract, same chain |
| Partners | REST + OpenAPI, transcoded — not maintained separately |
| A CLI | Generated command tree from the same annotations |

## Three principles

**1 · Bring your own format.** Proto is the *substrate*, not the *interface*. A frontend adapter
transforms your source — proto, OpenAPI, GraphQL SDL, TypeSpec, or a small YAML DSL — into canonical
descriptors, and everything downstream is format-blind.
→ [§09 Schema frontends](docs/09-schema-frontends.md)

**2 · Adapters all the way down.** Five extension points — frontends, generators, transport drivers,
codecs, interceptors — each an interface with a registry. Chains are composable and ordered by
name. Core ships three dispatch-level interceptors and takes no position on the rest: authz,
validation and error taxonomy are optional modules, and an empty chain is valid.
→ [§10 Extensibility](docs/10-extensibility.md)

**3 · One CLI.** `ix` is the only thing you install. Init, import, generate, format, lint,
breaking-change check, drift gate, and `ix describe` to see every road a method travels.
→ [§11 The CLI](docs/11-cli.md)

### The one guarantee

Core promises exactly one behavioural thing: **whatever interceptor chain you configure runs
identically on every transport.** That kills the classic multi-transport failure — a check enforced
on HTTP and silently absent on the bus — without core needing an opinion about what the check *is*.

Authorization, validation and error taxonomy are optional modules. An empty chain is valid.

```
  your format                the IR                    what you get
  ───────────                ──────                    ────────────
  .proto        ─┐
  OpenAPI       ─┤ frontend                          ┌─ Go · TS · Python · Kotlin · Swift
  GraphQL SDL   ─┼─ adapter ─▶  descriptors  ─▶ gen ─┼─ bus clients + subscribers
  TypeSpec      ─┤   §09         (canonical)         ├─ permission table (optional)
  YAML DSL      ─┘                                   └─ CLI tree · OpenAPI · docs
                                     │
                                     ▼
        HTTP · REST · NATS · MQTT · WebSocket  ──▶  ONE dispatch
                                                     ONE interceptor chain
                                                     ONE handler
```

---

## What you actually build

Most of the surface is generated. The irreducible new code is one engine, three thin drivers and
a couple of codegen plugins.

| Component | Who writes it | Size |
| --- | --- | --- |
| HTTP / RPC binding | generated | nothing to write |
| REST binding | generated + a transcoder library | nothing to write |
| **Message engine** | **once, in core** | **879 lines — the bulk of the work** |
| NATS / MQTT / WebSocket drivers | one each | 277 / 261 / 417 lines *(measured)* |
| `protoc-gen-bus` · `-cli` *(· `-authz`, optional)* | plugins | a few hundred lines each |
| Per-service binding code | generated | nothing to write, **ever** |

### It is not a runtime translation layer

Schema transformation happens **once, at build time**. There is no per-message mapping on the hot
path. The envelope's payload is opaque `bytes`, and generated dispatch resolves the type from the
procedure string. The rule that keeps it that way:

> A binding adapter may not import a single concrete message type. If the NATS adapter imports
> `catalogv1`, it has stopped being an adapter and become a second implementation of the API.

Adapters see **procedure strings, bytes, and metadata**. Never `Provider`, never
`ListProvidersRequest`.

---

## Build plan

Six phases, each independently useful — **stopping after any one leaves you better off than you
started.** The message engine is not needed until phase 4; non-proto frontends come last, on
purpose.

| Phase | Deliverable | Engine? |
| --- | --- | --- |
| 1 | Core + contract + `ix` skeleton — IR, drift gate, Go + TS codegen | no |
| 2 | RPC binding + **pluggable** chain + chain symmetry | no |
| 3 | REST binding via transcoding | no |
| 4 | **Message engine** + envelope + NATS driver | **yes** |
| 5 | MQTT 5 + WebSocket drivers — proving the seam | reuses |
| 6 | Schema frontends — OpenAPI, DSL, TypeSpec | reuses |

Full detail, exit criteria and de-risking notes → **[BUILD-PLAN.md](BUILD-PLAN.md)**

---

## Documentation

Three sets, with different jobs.

| Set | What it is for | Start at |
| --- | --- | --- |
| **[The guide](docs/guide/)** | **How to use this.** Eight task pages: install `ix`, write a contract, serve it, call it, install the optional modules, add a transport, import a non-proto format, run it in CI. Every command was run and every output pasted from a real terminal | [`docs/guide/01-getting-started.md`](docs/guide/01-getting-started.md) |
| **[The design docs](#documents)** | **Why it is the way it is.** The problem, the proposal, and the design of each layer — the argument, not the instructions | [`docs/00-problem.md`](docs/00-problem.md) |
| **[The ADRs](docs/adr/)** | **What was decided, and what it cost.** Fifty-four records, each with the alternatives that lost and the file or test that enforces it | [`docs/adr/README.md`](docs/adr/README.md) |

Plus [`docs/annotation-band.md`](docs/annotation-band.md) — the extension-number table, which a new
annotation must claim a row in *before* it exists — and a README in every module
(`auth/`, `errors/`, `validate/`, `driver/*/`, `binding/rest/`, `frontend/*/`, `tools/`, `ix/`,
`examples/catalog/`), which is the reference the guide is the task-shaped version of.

## Documents

| # | Document | Covers |
| --- | --- | --- |
| 00 | [The three-contract problem](docs/00-problem.md) | Why one operation gets defined three times; the seven gaps a minimal bus binding leaves |
| 01 | [The proposal](docs/01-proposal.md) | The fan-out, the four layers, the portable/bound seam |
| 02 | [The contract layer](docs/02-contract.md) | Proto layout, mechanical naming, the annotation band |
| 03 | [The envelope](docs/03-envelope.md) | `Request` / `Response` / `Frame` |
| 04 | [Bindings](docs/04-bindings.md) | Two families not five builds; the `Driver` interface; what the engine owns |
| 05 | [One call, two transports](docs/05-one-call-two-transports.md) | The claim the design must earn; what ships to clients |
| 06 | [Cross-cutting concerns](docs/06-crosscutting.md) | Chain symmetry; stock interceptors; authz as an optional module |
| 07 | [Codegen and plugins](docs/07-codegen.md) | The pipeline, writing a plugin, the drift gate |
| 08 | [Decisions and open questions](docs/08-decisions.md) | Fifteen decisions to make once; what this is worth |
| **09** | [**Schema frontends**](docs/09-schema-frontends.md) | **Bring your own format; why proto is the IR; total-or-loud** |
| **10** | [**Extensibility**](docs/10-extensibility.md) | **Five extension points, the dependency rule, named chains** |
| **11** | [**The CLI**](docs/11-cli.md) | **`ix` commands, `describe`, `import`, `verify`, distribution** |

---

## Maturity

Not all of this carries the same evidential weight. Stated plainly so nobody plans against the
weakest part.

| Part | Standing |
| --- | --- |
| Contract layer, RPC + REST bindings, codegen, annotation-driven authz | Patterns **proven in production systems**, implemented here |
| The envelope | **Built and exercised** by four drivers, including one with neither native headers nor a broker |
| NATS · MQTT 5 · WebSocket · in-process | **Built**, each passing the same conformance suite against a real in-process broker |
| Schema frontends, adapter registries, the CLI | **Built.** The DSL and OpenAPI frontends ship; TypeSpec, GraphQL and JSON Schema remain documented extension points |
| **Production traffic** | **None.** Nothing here has served a real user |
| **API stability** | **None yet.** Every extension point is public API and some of it will move |

**What the drivers taught us.** The estimate of ~150 lines per driver held for the driver proper —
NATS is 172 lines of code, WebSocket 84 — but the honest number for a whole module is 277 to 417,
and the difference is connection handshakes, capability negotiation and config parsing that no
design document accounts for.

**The engine seam was wrong six times, and each driver found a different one**: acknowledging on
delivery rather than on completion; a correlation id a driver could not reach; inbound metadata
dropped on exactly the transports that needed it; replies acknowledged only on the chunked path; a
subscription plan that ran a handler N times on a single-channel transport; and a conformance
suite that only one transport could run. That is the argument for building four drivers before
calling the seam right — and for treating the next one as likely to find a seventh.

## Try it

```bash
go install github.com/darmawan01/interchange/ix/cmd/ix@v0.1.1   # or, from a checkout:
make plugins ix          # build the plugins and the CLI, no network needed
make test                # 229 tests across thirteen modules
make verify              # regenerate every contract and fail on drift
```

The worked example is [`examples/catalog`](examples/catalog): one service, five RPCs, and fourteen
acceptance tests named after the build-plan criteria they close. Its chain is asserted to run in
identical order over Connect, REST, an in-process bus and a real NATS broker.

```bash
cd examples/catalog
../../bin/ix describe CatalogService.ListProviders    # every road this method travels
../../bin/ix verify                                   # the drift gate
```

## Repository layout

| Path | What it is |
| --- | --- |
| `.` | Core: the envelope, the chain, dispatch, the five extension-point interfaces. Depends on protobuf and connect, and nothing else — `hack/depcheck.sh` asserts it |
| `engine/` | The message engine: correlation, deadlines, chunking, replay suppression, metadata fallback |
| `binding/rpc`, `binding/rest` | Connect over HTTP; REST by in-process transcoding |
| `driver/{memory,nats,mqtt,ws}` | One per transport, each passing `drivertest` |
| `drivertest/` | The conformance suite a third-party driver runs |
| `auth/`, `errors/`, `validate/` | Optional modules. Core works without them |
| `frontend/{dsl,openapi}` | Schema frontends |
| `tools/`, `ix/` | The codegen plugins and the CLI |
| `examples/catalog/` | The worked example and the acceptance tests |

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) has the eight gates and the rules for adding a transport, an
annotation or a plugin. The most useful contributions right now:

- **Production traffic.** Nothing here has served a real user, and that is the gap between "tested"
  and "trustworthy".
- **A fifth driver.** Four drivers found six engine bugs between them; the seam is better for it and
  is probably still wrong somewhere.
- **Frontend experience reports** — which format do you actually want to write contracts in?
- **Naming.** "Interchange" is a working name.
