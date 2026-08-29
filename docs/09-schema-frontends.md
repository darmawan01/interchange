# 09 — Schema frontends: bring your own format

**You should not have to learn protobuf to use this.** You define your service in whatever format
your team already uses; a *frontend adapter* transforms it into the canonical proto, and everything
downstream — codegen, bindings, authz, CLI — works identically from there.

```
  your format                    the IR                     everything else
  ───────────                    ──────                     ───────────────
  .proto          ─┐
  OpenAPI 3.x     ─┤            canonical                  Go · TS · Python · Swift
  JSON Schema     ─┼─ Frontend ─▶ proto      ─▶ generators ─▶ Kotlin · Rust · SDKs
  GraphQL SDL     ─┤   adapter   (FileDescriptorSet)        bus clients · authz table
  Interchange DSL ─┘                                        CLI · OpenAPI · docs
```

## Why proto is the IR and not just another input

The IR is a real `FileDescriptorSet` — the same structure `protoc` produces — not a bespoke AST.

**This is a deliberate constraint, and it is the highest-leverage decision in the design.** Every
downstream tool already speaks descriptors: buf's linter and breaking-change detector, every protoc
plugin ever written, gRPC reflection, schema registries, the entire generator ecosystem. Inventing
our own AST would mean reimplementing all of that, badly, and getting none of it for free.

So: **proto is the substrate, not the interface.** A user who never opens a `.proto` file still gets
every property of one.

## The `Frontend` adapter

```go
// A Frontend turns one source format into canonical descriptors.
// It is the ONLY place a source format is understood.
type Frontend interface {
    // Name is the identifier used in interchange.yaml (e.g. "openapi").
    Name() string

    // Detect reports whether this frontend claims the file, so `ix import`
    // can work without being told the format.
    Detect(path string, head []byte) bool

    // Parse transforms sources into descriptors. It MUST return an error --
    // never a partial result -- for anything it cannot represent. See
    // "Total or loud" below.
    Parse(ctx context.Context, src Sources, opt Options) (*descriptorpb.FileDescriptorSet, Diagnostics, error)
}
```

Register one and it is available to the whole toolchain:

```go
interchange.RegisterFrontend(openapi.New())
```

## The frontends

| Source | Standing | Fit and the sharp edges |
| --- | --- | --- |
| **`.proto`** | native | Identity frontend — no transform, no loss. The reference implementation. |
| **Interchange DSL** *(YAML)* | ours | The on-ramp for teams who do not want an IDL. Small by design; anything it cannot say, say in proto. |
| **OpenAPI 3.x** | lossy | Paths → RPCs needs a naming rule you must accept. `oneOf`/`allOf`/`anyOf` have no clean proto equivalent. Nullable vs optional is genuinely ambiguous. |
| **JSON Schema** | partial | **No service concept at all** — it describes messages only. Services must be declared separately (sidecar or DSL). |
| **GraphQL SDL** | partial | Queries/mutations → RPCs maps well. Interfaces, unions and fragments do not. Subscriptions map to streaming, which is [deliberately deferred](08-decisions.md). |
| **TypeSpec** | good fit | Already an IDL designed to emit multiple targets. Probably the easiest non-proto frontend to get right. |

> **`.proto`, the Interchange DSL and OpenAPI 3.x ship.** The DSL refuses 18 construct classes with
> a line and column; OpenAPI refuses 25. TypeSpec, GraphQL SDL and JSON Schema remain the extension
> point's reason to exist — adding one requires no change to core, which OpenAPI demonstrated by
> needing none.
>
> None has been proven against a real service. That is the bar for calling one *supported*, and
> neither has cleared it.

## Four rules that keep this honest

**1. Total, or loud.** A frontend that silently drops a construct it cannot represent produces a
contract that *lies* — and a lying contract is worse than three honest ones, which is the entire
problem this project exists to solve. Every frontend must either represent a construct or **fail
with the exact source location**. No best-effort, no "mostly works".

```
✗ openapi: components/schemas/Payment: 'oneOf' has no canonical proto form
    at payments.yaml:212:7
  → use a proto oneof, or flatten the variants and set x-interchange-oneof
```

**2. Round-trip is not a goal.** Making `proto → OpenAPI → proto` an identity function is a tar pit
that consumes the project. The **generated proto is the artifact**: it is emitted, committed and
reviewed. The source format is an input, not a mirror.

**3. Annotations need a home in every frontend.** The `auth` and `transports` annotations are where
most of the value is — a frontend that cannot express them produces a contract with no security
posture. Each frontend needs a path:

| Frontend | How annotations arrive |
| --- | --- |
| proto | native options |
| OpenAPI | `x-interchange-*` vendor extensions |
| GraphQL | directives (`@auth(resource: "providers", verb: READ)`) |
| JSON Schema / anything | **sidecar file** |

The **sidecar is the universal fallback**, so no frontend is ever blocked on inventing extension
syntax:

```yaml
# catalog.interchange.yaml — annotations keyed by procedure
procedures:
  /platform.catalog.v1.CatalogService/ListProviders:
    auth:
      auth_types: [SESSION, API_KEY, WORKLOAD]
      permission: {resource: providers, verb: READ}
    transports: [RPC, REST, BUS]
    cli: {path: [catalog, providers]}
```

**4. The emitted proto is committed.** Same drift gate as generated code: `ix verify` regenerates
from source and fails if the tree moved. This is what stops the IR from becoming an invisible
build artifact nobody can review.

This is enforced rather than asked for: emitting source is the optional `interchange.SourceEmitter`
interface, and `ix import` refuses to run a frontend that does not implement it. A frontend that
can only produce descriptors has nothing reviewable to write.

## What this does *not* mean

This is **schema-level** transformation, at build time, once. It is **not** a runtime translation
layer — there is no per-message mapping on the hot path, and a binding adapter still never sees a
concrete message type. See [§01](01-proposal.md).

The distinction matters because runtime translation is where these systems normally go to die:
every message paying a conversion cost, every format mismatch becoming a production incident. Here,
the transformation happens once at build time and produces a normal proto tree.
