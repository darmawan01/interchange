# 07 — Bring your own format

Proto is the **substrate**, not the interface. A frontend adapter turns your source into canonical
descriptors, and everything downstream — codegen, bindings, authz, the CLI — is format-blind,
because the IR is a real `FileDescriptorSet` and nothing downstream can tell where it came from
([ADR-0001](../adr/0001-filedescriptorset-is-the-ir.md),
[ADR-0002](../adr/0002-proto-is-the-substrate.md)).

Two frontends ship: the **YAML DSL** (for people who should not have to learn protobuf) and
**OpenAPI 3.x** (for the document you already have). TypeSpec, GraphQL SDL and JSON Schema are
documented extension points, not implementations.

## Four rules, and one command

```
ix import <file>...  [--package pkg] [--sidecar file] [--out dir] [--frontend name] [--dry-run]
```

`ix import` detects the format, converts it to canonical `.proto`, and writes it into your `api/`
tree, where it is committed and reviewed like any other contract.

**1 · Total, or loud** ([ADR-0039](../adr/0039-total-or-loud.md)). Every construct with no
canonical proto form stops the import with its exact line, column and a hint. Nothing partial is
ever written. A contract that silently drops what it could not express is a contract that *lies*,
and that is the failure this project exists to remove.

**2 · The emitted proto is the artifact** ([ADR-0041](../adr/0041-the-emitted-proto-is-the-artifact.md)).
Run `ix import` once, commit the `.proto`, edit it from then on.

**3 · Round-tripping is not a goal** ([ADR-0040](../adr/0040-round-tripping-is-not-a-goal.md)).
There is no proto → OpenAPI and no proto → DSL path, and there will not be. Taking over by hand is
a one-way door you can walk through at any time.

**4 · A frontend never reads the filesystem** ([ADR-0045](../adr/0045-frontends-never-read-the-filesystem.md)).
An external `$ref` is refused; bundle the document.

## When it refuses

Real output, run against the OpenAPI frontend's own test document:

```
$ ix import ../../frontend/openapi/testdata/payments.yaml

  detected   OpenAPI 3.0.3
  frontend   openapi

  ⚠ 1 construct(s) need a decision:

    ../../frontend/openapi/testdata/payments.yaml  no proto package for this document
                       → set Options.Package, or x-interchange-package at the document root

  nothing written — resolve the 1 item(s) above, then re-run
```

Exit status 3, and the `api/` tree untouched. The package is not a formality: the sidecar is keyed
by full procedure string (`/payments.v1.PaymentsService/ListDisputes`), so the package has to be
known before an annotation can be applied to anything — which is why this is an error rather than a
guess.

A DSL refusal looks the same, from the YAML's own positions:

```
$ ix import bad.ix.yaml

  detected   Interchange DSL
  frontend   dsl

  ⚠ 2 construct(s) need a decision:

    /tmp/bad.ix.yaml:6:7  messages.PingRequest.fields: field "nonce" has no number
                       → give the field an explicit number: `n: 1` -- the DSL will not derive one,
                         because a derived number changes when the file is reordered

    /tmp/bad.ix.yaml:10:7  rpc PingService.Ping: no authorization declared
                       → add an `auth:` block, or a sidecar entry -- an RPC with no declared
                         posture is one nobody reviewed

  nothing written — resolve the 1 item(s) above, then re-run
```

The two diagnostics are different severities. The missing field number is an **error** and nothing
is written. The missing `auth` is a **warning** in the DSL — authorization is an optional module and
its policy is that module's to set, so the frontend makes the omission visible rather than deciding
for you. With only the warning, the import succeeds and writes the file.

> **Diagnostics come from the source, never from the compiler.** Every construct is validated
> against the YAML's own positions before a byte of proto is rendered. If protocompile rejects the
> generated source, that is a defect in the frontend and it says so — a location in generated
> source would be useless to the person holding the YAML.

## When it succeeds

```
$ ix import ../../frontend/openapi/testdata/payments.yaml \
    --package payments.v1 \
    --sidecar ../../frontend/openapi/testdata/payments.interchange.yaml \
    --out /tmp/out

  detected   OpenAPI 3.0.3
  frontend   openapi

  ✓ components/schemas/Payment/allOf: 2 allOf member(s) flattened into Payment
  ✓ components/schemas/Customer/allOf: 2 allOf member(s) flattened into Customer
  ✓ components/schemas/Refund/allOf: 2 allOf member(s) flattened into Refund
  ✓ components/schemas/Invoice/allOf: 2 allOf member(s) flattened into Invoice
  ✓ components/schemas/Dispute/allOf: 2 allOf member(s) flattened into Dispute
  ✓ components/schemas/Error/properties/details: free-form object mapped to
      google.protobuf.Struct; its shape is not part of the contract
  ✓ components/schemas/Currency: named string is a type alias; proto has none,
      so it is inlined at every use
  wrote      /tmp/out/payments/v1/payments_service.proto

  commit the emitted proto: it is the contract now, and `ix verify` gates it
```

The `✓` lines are **notes**, not warnings: things that were representable but lossy, each named so
the loss is in the diff rather than in someone's head. Read them once and then read the emitted
proto, which is the thing you are actually adopting.

`--dry-run` reports what would be written without writing it.

## The sidecar

Some formats have nowhere to put an annotation. The sidecar is the universal fallback
([ADR-0042](../adr/0042-the-sidecar-is-the-universal-fallback.md)) — one file, keyed by the full
procedure string, using the same vocabulary every frontend uses:

```yaml
# payments.interchange.yaml
procedures:
  /payments.v1.PaymentsService/ListDisputes:
    auth:
      auth_types: [SESSION, WORKLOAD]
      permission: {resource: disputes, verb: READ}
    transports: [RPC, REST]

  /payments.v1.PaymentsService/ReplayWebhook:
    auth:
      auth_types: [WORKLOAD]
      permission: {resource: webhooks, verb: EDIT}
      platform: true
```

Two rules, both about not being surprised:

- **An annotation set both inline and in the sidecar is an error**, not a precedence rule. Silent
  precedence is how a security posture gets overwritten by a file nobody was reading
  ([ADR-0044](../adr/0044-inline-and-sidecar-conflict.md)).
- **A sidecar entry that matches no RPC is an error.** An annotation nobody applied is worse than no
  annotation, because it reads as though the posture is declared.

The OpenAPI frontend is the one exception to the first rule's spirit: it has *two* annotation paths,
`x-interchange-*` extensions in the document and the sidecar, and **the extension wins** — the
sidecar is for a document you cannot edit, not an override of one you can.

## The DSL

→ [`frontend/dsl/README.md`](../../frontend/dsl/README.md)

Small by design. It is the on-ramp, not a second IDL.

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

All five annotations are expressible: `transports` / `group`, `http`, `auth`, `cli`, `internal`,
plus `idempotency`. Imports are **derived** from what you actually reference — there is no import
list to maintain. The emitted file lands at `<package as path>/<file>.proto`, which is what makes
`import "catalog/v1/catalog.proto"` resolve for everyone else.

### Field numbers are required, and that is the point

A field number is part of the wire contract. The DSL will not derive one from declaration order,
because a derived number changes the moment somebody reorders the YAML — a wire-incompatible change
with **no reviewable diff**. So `n` is mandatory, missing `n` is an error with a line number, and
reordering the file can never move a number
([ADR-0043](../adr/0043-explicit-field-numbers-in-the-dsl.md)).

That is friction on the on-ramp, and it is the acknowledged cost.

### What the DSL deliberately cannot express

Every one of these is a *deliberate* omission, and every one produces an error with a line, a
column and a hint rather than being silently dropped.

| Not expressible | What to do instead |
| --- | --- |
| `oneof` | Say it in proto. There is no YAML shape for it that stays readable |
| Streaming RPCs | Deferred project-wide ([ADR-0051](../adr/0051-streaming-is-deferred.md)) |
| Field-level options — protovalidate rules, `tenant_id_field`, `project_id_field` | Say it in proto |
| `reserved` ranges and names, `allow_alias`, `deprecated` | Say it in proto |
| `google.api.http` `additional_bindings`, `response_body` | One route per RPC; split the RPC, or say it in proto |
| proto2, groups, custom options beyond the five above | Say it in proto |
| A type from a proto file neither in `Options.Deps` nor linked in | Pass its descriptors in `Options.Deps`, or say it in proto |
| Cross-references between two DSL files | Each DSL file resolves on its own. Put shared types in one file |

"Say it in proto" is cheap precisely because the emitted proto is the artifact.

## The OpenAPI frontend

→ [`frontend/openapi/README.md`](../../frontend/openapi/README.md)

Paths become RPCs, schemas become messages, and `google.api.http` is **derived** from the path and
method — the one annotation you are never asked to restate. Template variables are rewritten to the
snake_case field names they bind to (`/v1/payments/{paymentId}` → `/v1/payments/{payment_id}`).

RPC names come from `x-interchange-name`, then `operationId`, then a derivation from the method and
the path (`GET /v1/providers` → `ListProviders`, `GET /v1/providers/{id}` → `GetProvider`,
`POST /v1/providers` → `CreateProvider`). "Plural" is decided by a documented heuristic, not
English — when it guesses wrong, override it. **A collision between two derived names is an error
naming both locations**, never a silent suffix: `CreateProvider2` is a contract nobody meant to
publish.

### Two things that will bite you

**Field numbers come from document order.** Path parameters in path order, then query parameters,
then the body; within a schema, `allOf` members first and then the schema's own properties.
**Reordering properties in the source document renumbers fields, which is a breaking wire change.**
The emitted proto is committed precisely so that shows up as a reviewable diff, and `ix breaking`
catches it. `x-interchange-field: N` pins a number.

**Required and nullable together is an error.** `required` says the key must be present;
`nullable` says its value may be null. Proto3 cannot distinguish *present but null* from *absent*,
so the combination has no honest encoding. Resolve it by dropping one, or by writing
`x-interchange-nullable: optional` to say that null and absent mean the same thing here.

### What OpenAPI cannot represent

The full table is in the module README; these are the ones you will actually hit:

| Construct | Why |
| --- | --- |
| `oneOf` / `anyOf` without `x-interchange-oneof` | no canonical proto form — opt in by naming the oneof and `$ref`-ing every variant |
| an inline `oneOf` variant | nothing to name the oneof field |
| `not` | no proto form |
| an `allOf` member that is not an object, or an `allOf` cycle | only objects can be flattened; no fixed point |
| a schema with no type, properties or `additionalProperties` | proto has no `any` |
| an array of arrays, of maps, or a map of either | `repeated` cannot nest |
| a non-string `enum` | proto enum values are named constants |
| an **external** `$ref` | a frontend never reads the filesystem — bundle the document |
| `POST` onto an item path (`/things/{id}`) | no defensible derived name |
| `HEAD`, `OPTIONS`, `TRACE` | transport concerns, not contract methods |
| a `GET` or `DELETE` with a `requestBody` | cannot be transcoded |
| a `header` or `cookie` parameter | transport metadata, not a contract field |
| a query parameter that is a map or object | cannot be bound |
| two success responses carrying a body | an RPC returns one message |
| an unknown `x-interchange-*` key | a misspelled annotation is worse than a missing one |
| an operation with no `auth` annotation | see below |
| a document that is not OpenAPI 3.x | convert a 2.0 document first |

Three opt-outs: `x-interchange-skip: true` on a parameter or property drops it deliberately,
`x-interchange-nullable: optional` resolves required-and-nullable, and
`Params["on_missing_auth"] = "warn" | "ignore"` downgrades the missing-`auth` error for a team
working through an existing document.

Note the asymmetry with the DSL: **OpenAPI treats a missing `auth` annotation as an error by
default, the DSL as a warning.** The OpenAPI path is a migration path — you are importing something
that already serves traffic — and an operation with no declared security posture is exactly what the
annotation exists to prevent.

## Writing your own frontend

One interface, one registration. The registry is the same shape as the driver registry.

```go
type Frontend interface {
	// Name is the identifier used in interchange.yaml (e.g. "openapi").
	Name() string

	// Detect reports whether this frontend claims the file, so `ix import`
	// can work without being told the format. head is the first few KiB.
	Detect(path string, head []byte) bool

	// Parse transforms sources into descriptors. It MUST return an error --
	// never a partial result -- for anything it cannot represent.
	Parse(ctx context.Context, src Sources, opt Options) (*descriptorpb.FileDescriptorSet, Diagnostics, error)
}

// Optional, and required by `ix import`: rendering the .proto that gets
// committed, keyed by the path each file should be written to.
type SourceEmitter interface {
	ProtoSources(ctx context.Context, src Sources, opt Options) (map[string][]byte, Diagnostics, error)
}
```

```go
interchange.RegisterFrontend(myFrontend{})
```

`ix import` asserts the frontend to `interchange.SourceEmitter` and **refuses to write a tree
without it** — a frontend that produces descriptors but no reviewable source is a frontend whose
output nobody can read.

Three things to get right, all of them the same thing:

- **Return `Diagnostics`, and make `HasErrors()` mean it.** `Diagnostics.Err()` is what stops the
  import. A frontend that warns where it should error is a frontend that emits a contract that lies.
- **Positions come from your source.** Address them however your format allows — the OpenAPI
  frontend uses JSON pointers into the raw YAML tree, so the pointer that finds a construct is also
  the string a diagnostic names it by, and the two can never disagree.
- **Be deterministic.** Sort before emitting. Do not decode into a Go map and iterate it. The output
  is committed and sits under the drift gate, and randomised iteration is exactly the kind of
  nondeterminism a gate cannot survive.

**Do not link the optional modules.** A frontend that imported `/auth` to emit an auth annotation
would make an optional module mandatory. The annotation descriptors arrive in `Options.Deps` — `ix`
supplies the protos of every module installed — and an annotation whose descriptors nobody supplied
is refused **at the annotation**, not as a compiler error against source the author never wrote.
Both shipped frontends have an `internal/nolink` test package that proves they link neither.

## Configuring a frontend in `interchange.yaml`

```yaml
sources:
  - path: api/**/*.proto
    frontend: proto
    # sidecar: legacy/annotations.yaml
```

`proto` is the only frontend wired into the `ix` build today; the DSL and OpenAPI are reached
through `ix import`, which converts once and leaves you with proto. Any other value in `frontend:`
is an error rather than a silent skip.

Next: [08 Operating it](08-operating-it.md).
