# Interchange

**One contract, every road.** Define your service once — in protobuf, or in whatever format your
team already uses — and Interchange generates typed clients and server bindings for HTTP, REST,
NATS, MQTT and WebSocket. The browser, the mobile app, the peer service and the async worker all
call the same declared method, through the same middleware, into the same handler.

> **Status: design proposal.** Working name. Nothing here is built yet — this repository is the
> design and the build plan. See [Maturity](#maturity) before planning against any part of it.

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
| **Message engine** | **once, in core** | **the bulk of the work** |
| NATS / MQTT / WebSocket drivers | one each | ~150 lines each *(estimated)* |
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
| Contract layer, RPC + REST bindings, codegen, annotation-driven authz | Patterns **proven in production systems** |
| NATS binding | A pattern **in production**, adapted here to the envelope |
| **The envelope** | **Proposed design** |
| **MQTT and WebSocket bindings** | **Proposed design** |
| **Schema frontends, adapter registries, the CLI** | **Proposed design — no implementation** |
| The `~150 lines` driver figures | **Estimates**, not measurements |

The envelope and the MQTT/WebSocket bindings should be **prototyped against one real service before
phase 4 is committed to**.

## Contributing

The design is the artifact right now. The most useful contributions are:

- **A real service to prototype the envelope against** — this is the biggest open risk.
- **Frontend experience reports** — which format do you actually want to write contracts in?
- **Naming.** "Interchange" is a working name.

Open an issue rather than a PR while the design is still moving.
