# 10 — Extensibility: adapters all the way down

**Nothing in the pipeline is fixed.** Every stage is an interface with a registry, a set of
in-the-box implementations, and a documented contract for replacing them. If a stage cannot be
swapped, that is a bug in the design, not a feature of it.

## The five extension points

```
  source ──▶ FRONTEND ──▶ IR ──▶ GENERATOR ──▶ code
                                                 │
                          ┌──────────────────────┘
                          ▼
   wire ──▶ DRIVER ──▶ engine ──▶ CODEC ──▶ INTERCEPTOR chain ──▶ your handler
```

| # | Point | Interface | In the box | Why it must be pluggable |
| --- | --- | --- | --- | --- |
| 1 | Source frontend | `Frontend` | proto, DSL | New IDLs keep appearing ([§09](09-schema-frontends.md)) |
| 2 | Generator | protoc plugin | go, ts, py, bus, cli — *authz table optional* | Languages, and every org's SDK taste |
| 3 | Transport driver | `Driver` | nats, mqtt, ws | Kafka, AMQP, Redis, in-process, your broker |
| 4 | Codec | `Codec` | proto, json | msgpack, cbor, avro, encryption-at-rest |
| 5 | Interceptor | `Interceptor` | telemetry, recover, deadline *(core)* — authn/authz, validate, errors *(modules)* | Core ships only dispatch-level stages; the rest are opinions |

## The dependency rule

> **The core depends on interfaces. Adapters depend on the core. Nothing depends on a concrete
> adapter.**

Enforced by module layout, not by discipline — so that pulling in NATS does not drag an MQTT client
and a WebSocket stack into your binary:

```
github.com/<org>/interchange              core: IR, engine, chain, interfaces. No brokers.
github.com/<org>/interchange/driver/nats  + nats.go
github.com/<org>/interchange/driver/mqtt  + paho
github.com/<org>/interchange/driver/ws    + gorilla or nhooyr
github.com/<org>/interchange/frontend/openapi
github.com/<org>/interchange/auth         optional: annotation + authn/authz
github.com/<org>/interchange/auth/opa     + OPA
```

A CI check asserts the core module's dependency graph contains **no broker client, no HTTP router,
and no policy engine**. If it does, a seam has leaked.

## Interceptors: composable, ordered, replaceable

The chain is the part most people will customise first, so it gets the strongest guarantees.

```go
type UnaryFunc func(ctx context.Context, req *Envelope) (*Envelope, error)
type Interceptor func(next UnaryFunc) UnaryFunc
```

That signature is **stable API**. Three levels of customisation, in increasing order of commitment:

```go
// 1. Take the defaults.
chain := interchange.DefaultChain(cfg)

// 2. Extend them -- insert relative to a named stage, so ordering
//    survives a version bump that adds a stage in the middle.
chain := interchange.DefaultChain(cfg).
    After("deadline", tenantResolver()).
    Before("validate", idempotency(store)).
    Replace("telemetry", myOtelInterceptor())

// 3. Build your own. Nothing is mandatory.
chain := interchange.Chain(
    reason(), metrics(m), myAuth(), validate(v),
)
```

**Ordering is explicit and named** rather than positional. A positional chain breaks silently the
day a stage is inserted upstream; a named one fails loudly if the anchor disappears.

### Authorization is a module, not a requirement

**Core does not know what a permission is.** Authorization ships as an optional module (`/auth`),
owns its own annotation proto, and has no privileged access — it is an `Interceptor` like any
other. An adopter who authorizes at a gateway, runs mTLS-only internal services, or builds a public
data API imports none of it.

When you do install it, the decider is still yours:

```go
type Authorizer interface {
    Authorize(ctx context.Context, procedure string, ann Annotation,
        md map[string]string, msg proto.Message) error
}
```

Ships with RBAC; OPA, Cedar and Casbin are the obvious next adapters. A bespoke permission service
is an afternoon's work — without touching core, the contract, or any binding.

Strictness ("a missing annotation fails the build") is a **policy of that module**, configured by
you, not a property of the framework. See [§06](06-crosscutting.md).

## Codecs

```go
type Codec interface {
    Name() string                              // the envelope's `codec` field
    Marshal(proto.Message) ([]byte, error)
    Unmarshal([]byte, proto.Message) error
}
```

`proto` and `json` ship. The envelope carries the codec name per message, so a browser-facing
binding can stay human-readable while service-to-service traffic stays binary — on the same
service, at the same time.

## Drivers

See [§04](04-bindings.md) for the full `Driver` interface. The relevant property here: a driver
declares what it can do via `Capabilities`, and **the engine adapts** — no `switch` on transport
type anywhere in the core. Adding a broker is implementing six methods and a capability struct.

## Generators

Any protoc plugin works, including ones that predate this project. Ours are ordinary plugins with
no privileged access — which is the test of whether the extension point is real.

```yaml
# interchange.yaml
generate:
  - plugin: buf.build/protocolbuffers/go
    out: gen/go
  - plugin: ./bin/protoc-gen-mysdk        # yours, same standing as ours
    out: gen/sdk
    strategy: all
```

## Configuration

One file at the repo root, declaring which adapters are in play:

```yaml
# interchange.yaml
version: 1

sources:
  - path: api/**/*.proto
    frontend: proto
  - path: legacy/openapi/*.yaml           # optional, once §09 lands
    frontend: openapi
    sidecar: legacy/openapi/annotations.yaml

transports:
  default: [rpc, rest]                    # per-RPC (transports) overrides this
  drivers:
    - nats

generate:
  - {plugin: buf.build/protocolbuffers/go, out: gen/go}
  - {plugin: buf.build/bufbuild/es,        out: gen/ts}
  - {plugin: ./bin/protoc-gen-bus,         out: gen/go/bus, strategy: all}

# Optional module. Omit this block entirely and core never learns
# what a permission is.
auth:
  provider: rbac                          # or opa | cedar | custom
  on_missing_annotation: error            # error | warn | ignore
```

## What "customisable" must not become

An honest limit, stated up front so the project does not drift into it.

Configurability has a failure mode: **so many knobs that no two deployments behave alike, and no
bug report is reproducible.** The guard is that extension points are *interfaces with contracts*,
not free-form hooks — a `Driver` that violates its `Capabilities` is a broken driver, and the engine
is entitled to assume the contract holds.

Two things stay fixed on purpose, because making them pluggable would dissolve the guarantee the
project is for:

- **The envelope shape.** Bindings populate it; nobody redefines it. It is the convergence point —
  a per-deployment envelope means no shared dispatch and no shared chain.
- **Chain symmetry.** Whatever chain you configure runs identically on every transport. A driver may
  not add, skip or reorder a stage. This is the guarantee the project exists to provide, and the
  *only* behavioural invariant core imposes.

Note what is **not** on that list: authorization, validation, error taxonomy, codecs, transports and
source formats are all yours. Core guarantees your chain runs everywhere — not what is in it.

Everything else is yours.
