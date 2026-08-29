# The Interchange DSL — a YAML frontend

**You should not have to learn protobuf to use this.** Write a small YAML file; the DSL frontend
turns it into a canonical `FileDescriptorSet` and a formatted `.proto` tree. Everything downstream
— codegen, bindings, authz, the CLI — works identically from there, because the IR is a real
descriptor set and nothing downstream can tell where it came from ([§09](../../docs/09-schema-frontends.md)).

```
catalog.ix.yaml  ──▶  dsl frontend  ──▶  catalog/v1/catalog.proto   (committed, reviewed)
                                    └─▶  FileDescriptorSet          (the IR)
```

The DSL is **small by design**. It is the on-ramp, not a second IDL. Anything it cannot say, you
say in proto — and since the emitted `.proto` is the artifact, taking over by hand is a one-way
door you can walk through at any time.

## Quick start

```yaml
# ping.ix.yaml
interchange: v1
package: acme.ping.v1

messages:
  PingRequest:
    fields:
      nonce: {type: string, n: 1}
  PingResponse:
    fields:
      nonce: {type: string, n: 1}

services:
  PingService:
    transports: [RPC, REST]
    rpcs:
      Ping:
        request: PingRequest
        response: PingResponse
        http: {get: /v1/ping}
        auth:
          auth_types: [SESSION]
          permission: {resource: ping, verb: READ}
```

```
$ ix import ping.ix.yaml
  detected   Interchange DSL
  frontend   dsl
  ✓ wrote acme/ping/v1/ping.proto
```

## The schema

### Document

| Key | Meaning |
| --- | --- |
| `interchange` | DSL version. `v1`. Optional; when present it also lets `Detect` claim a plain `.yaml` file. |
| `package` | The proto package. Required, unless the caller supplies one in `Options.Package`. |
| `file` | The emitted file's base name. Defaults to the source's, so `catalog.ix.yaml` emits `catalog.proto`. |
| `go_package` | Overrides the `go_package` option. Otherwise derived from `Options.GoPackagePrefix` + the package path. |
| `enums` | Top-level enums, keyed by name. |
| `messages` | Top-level messages, keyed by name. |
| `services` | Services, keyed by name. |

The emitted file lands at `<package with dots as slashes>/<file>.proto`, which is what makes
`import "catalog/v1/catalog.proto"` resolve for everyone else. Several sources can be parsed in one
run; two of them emitting the same path, or declaring the same type name in the same package, is an
error against the second one -- otherwise one contract would silently replace the other.

### Messages and fields

```yaml
messages:
  # A YAML comment above a message, field, enum, service or RPC becomes its
  # proto doc comment.
  Provider:
    fields:
      provider_id: {type: string, n: 1}
      status:      {type: ProviderStatus, n: 4}
      created_at:  {type: google.protobuf.Timestamp, n: 5}
      labels:      {type: "map<string, string>", n: 6}
      description: {type: string, n: 7, optional: true}
      tags:        {type: string, n: 8, repeated: true}
    messages:      # nested messages
      Endpoint:
        fields:
          url: {type: string, n: 1}
    enums:         # nested enums
      Scheme:
        values: {SCHEME_UNSPECIFIED: 0, SCHEME_HTTPS: 1}
```

| Field key | Meaning |
| --- | --- |
| `type` | A scalar, a type declared in this file, or a fully-qualified type from a known proto file. Required. |
| `n` | The field number. **Required** — see below. |
| `repeated` | A list. |
| `optional` | Explicit presence: absent and zero are different. |

**Scalars:** `double` `float` `int32` `int64` `uint32` `uint64` `sint32` `sint64` `fixed32`
`fixed64` `sfixed32` `sfixed64` `bool` `string` `bytes`.

**Maps:** `map<k, v>` — quote it in YAML flow style, because the comma would otherwise end the
entry. Keys are the integral, bool and string scalars.

**References** resolve the way protobuf's own do: innermost scope outward for names declared in
this file, then by full name against the proto files linked into the toolchain
(`google.protobuf.Timestamp`, `interchange.common.v1.PageRequest`). Imports are *derived* from
what you actually reference — there is no import list to maintain.

#### Field numbers are required, and that is the point

A field number is part of the wire contract. The DSL will not derive one from declaration order,
because a derived number changes the moment somebody reorders the YAML — a wire-incompatible
change with no reviewable diff. So `n` is mandatory, missing `n` is an error with a line number,
and reordering the file can never move a number.

Fields and enum values are emitted in **numeric** order; messages, enums, services and RPCs keep
**declaration** order. The output is byte-identical for identical input, because it is committed
and sits under the drift gate.

### Enums

```yaml
enums:
  ProviderStatus:
    values:
      PROVIDER_STATUS_UNSPECIFIED: 0
      PROVIDER_STATUS_ACTIVE: 1
```

Every enum needs a zero value whose name ends in `_UNSPECIFIED`. proto3 has no presence for enums,
so the zero value is what an unset field reads as; an enum whose zero means something real cannot
tell "not set" from that meaning. Aliases (`allow_alias`) are not supported.

### Services, RPCs and annotations

```yaml
services:
  CatalogService:
    transports: [RPC, REST]     # the service-level default
    group: catalog              # optional
    rpcs:
      ListProviders:
        request: ListProvidersRequest
        response: ListProvidersResponse
        http: {get: /v1/catalog/providers}
        idempotency: no_side_effects
        transports: [RPC, REST, BUS]
        group: catalog
        auth:
          auth_types: [SESSION, API_KEY, WORKLOAD]
          permission: {resource: providers, verb: READ}
        cli: {path: [catalog, providers]}
        internal: false
```

| Annotation | Emits | Values |
| --- | --- | --- |
| `transports` | `(interchange.transport.v1.transports)`, or `service_transports` on a service | `RPC` `REST` `BUS` `MQTT` `WS` |
| `group` | the competing-consumer group on the same option | any string |
| `http` | `(google.api.http)` | one of `get` `post` `put` `patch` `delete`, plus `body` (defaults to `"*"` for the writing methods) |
| `auth` | `(interchange.auth.v1.auth)` | `auth_types` from `SESSION` `API_KEY` `WORKLOAD`; `permission: {resource, verb}` with a verb from `READ` `CREATE` `EDIT` `DELETE`; `public`; `platform` |
| `cli` | `(interchange.cli.v1.command)` | `path`, `args`, `short`, `long`, `skip` |
| `internal` | `(interchange.transport.v1.internal)` | `true` / `false` |
| `idempotency` | `idempotency_level` | `no_side_effects` `idempotent` `unknown` |

A per-RPC `transports` **replaces** the service-level default; it is not merged, so a reviewer
reads one annotation rather than composing two ([§08](../../docs/08-decisions.md)).

An RPC with no `auth` block gets a **warning** with its location. Authorization is an optional
module and its policy is that module's to set, so the frontend makes the omission visible rather
than deciding for you.

## The sidecar

Annotations can live in a file of their own — the universal fallback of §09, keyed by the full
procedure string. Same vocabulary, same values.

```yaml
# catalog.annotations.yaml
procedures:
  /catalog.v1.CatalogService/ListProviders:
    transports: [RPC, REST, BUS]
    group: catalog
    http: {get: /v1/catalog/providers}
    auth:
      auth_types: [SESSION, API_KEY, WORKLOAD]
      permission: {resource: providers, verb: READ}
    cli: {path: [catalog, providers]}
```

It arrives as `Sources.Sidecar`. Two rules:

- **An annotation set both inline and in the sidecar is an error**, not a precedence rule. Silent
  precedence is how a security posture gets overwritten by a file nobody was reading.
- **A sidecar entry that matches no RPC is an error.** An annotation nobody applied is worse than
  no annotation, because it reads as though the posture is declared.

## Worked example

`testdata/catalog.ix.yaml` is the catalog service from
[`examples/catalog`](../../examples/catalog/api/catalog/v1/catalog.proto) written in the DSL, and
`testdata/catalog.golden.proto` is what it emits — five RPCs, all five annotations, a nested
message with a nested enum, a map field, an optional field, and an external message reference. It
is a golden test: if the emitted bytes move, the test fails.

## What the DSL deliberately cannot express

Everything here is a *deliberate* omission, not a missing feature. The DSL is the smallest surface
with the best onboarding story, not the most expressive one — and every one of these produces an
error with a line, a column and a hint rather than being silently dropped.

| Not expressible | What to do instead |
| --- | --- |
| `oneof` | Say it in proto. There is no YAML shape for it that stays readable. |
| Streaming RPCs | Deferred project-wide ([§08](../../docs/08-decisions.md)); when it lands, it lands in proto first. |
| Field-level options — protovalidate rules, `tenant_id_field`, `project_id_field` (50007/50008) | Say it in proto. |
| `reserved` ranges and names, `allow_alias`, `deprecated` | Say it in proto. |
| `google.api.http` `additional_bindings`, `response_body` | One route per RPC; split the RPC, or say it in proto. |
| proto2, groups, custom options beyond the five above | Say it in proto. |
| A type from a proto file the toolchain has not linked in | Declare it in the DSL, or say it in proto. |
| Cross-references between two DSL files | Each DSL file resolves on its own. Put the shared types in one file, or say it in proto. |

"Say it in proto" is cheap here precisely because the emitted proto is the artifact: run
`ix import` once, commit the `.proto`, and edit it from then on. There is no round trip to
preserve — `proto → DSL` is not a goal and will not be built.

## Using it

```go
import (
    interchange "github.com/darmawan01/interchange"
    "github.com/darmawan01/interchange/frontend/dsl"
)

dsl.Register() // also done by the package's init

f := dsl.New()
set, diags, err := f.Parse(ctx, interchange.Sources{
    Paths:   []string{"catalog.ix.yaml"},
    Content: map[string][]byte{"catalog.ix.yaml": b},
    Sidecar: sidecarBytes,
}, interchange.Options{GoPackagePrefix: "example.com/gen/go"})
```

`Parse` returns descriptors. To get the `.proto` source that `ix import` writes, assert the
frontend to `dsl.SourceEmitter` and call `ProtoSources` with the same arguments.

Two notes on the emitted set: it includes the transitive imports (dependencies first, the way
`protoc --include_imports` orders them) so a consumer can build a registry in one pass, and it
carries source info, so the doc comments survive into the IR. `protoc`'s `descriptor_set_out`
omits both by default.

## Notes on the implementation

- **Diagnostics come from the YAML, never from the compiler.** Every construct is validated
  against `yaml.Node` positions before a byte of proto is rendered. If protocompile rejects the
  generated source, that is a defect in this frontend and it says so — a location in generated
  source would be useless to the person holding the YAML.
- **Nothing is decoded into a Go map.** Randomised map iteration between the source and a
  committed artifact is exactly the kind of nondeterminism a drift gate cannot survive.
- **The annotation descriptors are linked in.** A frontend is handed its sources, never a
  filesystem, so the definition of `(interchange.auth.v1.auth)` can only come from the descriptor
  registry of the process doing the import.
