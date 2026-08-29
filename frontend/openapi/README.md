# `frontend/openapi` — the OpenAPI 3.x frontend

Turns an OpenAPI 3.0 or 3.1 document into canonical protobuf: a
`FileDescriptorSet` plus the `.proto` source it was compiled from. Paths become
RPCs, schemas become messages, and the `google.api.http` annotation is derived
from the path and method — which is the whole point of importing an OpenAPI
document, and the one annotation you are never asked to restate.

```go
import _ "github.com/darmawan01/interchange/frontend/openapi" // registers itself

f, _ := interchange.FrontendFor("openapi")
set, diags, err := f.Parse(ctx, sources, opts)
```

`ix import` uses the richer entry point, `(*openapi.Frontend).Import`, which
also returns the emitted source and a structured `Report`.

## The two rules this frontend is built around

**Total, or loud.** Every construct with no canonical proto form stops the
import with its exact line and column and a hint. Nothing partial is ever
written — a contract that silently drops what it could not express is a
contract that lies, and that is the failure this project exists to remove.

**Deterministic.** The emitted `.proto` is committed and sits under the drift
gate, so the same document always produces the same bytes: declarations sorted
by name, field numbers from document order, no timestamps, no version strings.

Round-tripping is not a goal. There is no proto → OpenAPI path here.

## How the document is read

The object model comes from [`libopenapi`](https://github.com/pb33f/libopenapi),
which normalises the differences between 3.0 and 3.1 (`nullable: true` versus
`type: [string, "null"]`) and resolves `$ref`s. **Positions come from the raw
YAML tree, addressed by JSON pointer** — one source of truth for every
location, and the pointer that finds it is also the string a diagnostic names
the construct by, so the two can never disagree.

JSON documents take the same path: YAML 1.2 is a superset of JSON, so one
parser reads both and the line and column still land.

## Naming

### RPCs

In order of precedence:

1. `x-interchange-name` on the operation.
2. `operationId`, converted to `UpperCamelCase`.
3. Derived from the method and the path.

The derivation, given the last path segment:

| Path shape | Method | Name | Example |
| --- | --- | --- | --- |
| ends in a segment | `GET` | `List` + segment | `GET /v1/providers` → `ListProviders` |
| ends in a **plural** segment | `POST` | `Create` + singular(segment) | `POST /v1/providers` → `CreateProvider` |
| ends in a **singular** segment | `POST` | segment + singular(owning segment) | `POST /v1/payments/{id}/refund` → `RefundPayment` |
| ends in a segment | `PUT` | `Replace` + segment | `PUT /v1/providers` → `ReplaceProviders` |
| ends in a segment | `PATCH` | `Update` + segment | `PATCH /v1/providers` → `UpdateProviders` |
| ends in a segment | `DELETE` | `Delete` + segment | `DELETE /v1/providers` → `DeleteProviders` |
| ends in `{var}` | `GET` | `Get` + singular(resource) | `GET /v1/providers/{id}` → `GetProvider` |
| ends in `{var}` | `PUT` | `Replace` + singular(resource) | `ReplaceProvider` |
| ends in `{var}` | `PATCH` | `Update` + singular(resource) | `UpdateProvider` |
| ends in `{var}` | `DELETE` | `Delete` + singular(resource) | `DeleteProvider` |

"Plural" is decided by the singulariser, which is a documented heuristic, not
English: `ies` → `y`; `ches`/`shes`/`ses`/`xes`/`zes` lose the `es`; a trailing
`s` is dropped unless the word ends `ss`, `us` or `is`. When it guesses wrong,
`operationId` or `x-interchange-name` overrides it.

**A collision between two derived names is an error naming both locations**,
never a silent suffix: `CreateProvider2` is a contract nobody meant to publish.

### Messages, fields, enums

| Source | Proto |
| --- | --- |
| `components/schemas/Foo` | message or enum `Foo` |
| an operation | `<Rpc>Request` and `<Rpc>Response` |
| an inline object property | a nested message named after the property |
| an inline enum property | a nested enum named after the property |
| a property | `lower_snake_case` field |
| an enum value | `<ENUM_NAME>_<VALUE>`, with a synthesised `_UNSPECIFIED = 0` |

### Field numbers

Numbers come from **document order**: path parameters in path order, then query
parameters, then the body; within a schema, `allOf` members first (in order,
flattened) and then the schema's own properties. `x-interchange-field: N` pins
a number; automatic assignment skips pinned numbers, and two pins on the same
number is an error.

**Reordering properties in the source document renumbers fields, which is a
breaking wire change.** The emitted proto is committed precisely so that shows
up as a reviewable diff — and `ix breaking` catches it.

## Type mapping

| OpenAPI | Proto |
| --- | --- |
| `string` | `string` |
| `string` / `date-time` | `google.protobuf.Timestamp` |
| `string` / `byte`, `binary` | `bytes` |
| `string` / any other format | `string` |
| `integer` / `int32` | `int32` |
| `integer` / `int64` or no format | `int64` |
| `number` / `float` | `float` |
| `number` / `double` or no format | `double` |
| `boolean` | `bool` |
| `array` | `repeated <item>` |
| object with `properties` | a message |
| object with only `additionalProperties: S` | `map<string, S>` |
| object with neither | `google.protobuf.Struct`, with a note |
| string `enum` | a proto enum |
| `$ref` to an object or enum schema | that message or enum |
| `$ref` to a scalar or array schema | inlined at the use site, with a note |
| `allOf` of object schemas | flattened into one message, with a note |
| `oneOf` / `anyOf` | **refused** unless `x-interchange-oneof` (below) |

An unrecognised `format` on a known type falls back to the base type and says
so in a note. Proto has no type alias, so a named scalar (`Currency: {type:
string}`) produces no message; it is inlined wherever it is referenced, which
is lossless on the wire.

### Requests and responses

A request message carries the path parameters (in path order), then the query
parameters, then the body:

- a `$ref` body becomes **one typed field** named after the schema, and the
  annotation says `body: "<field>"`;
- an inline object body is **flattened** into the request message, and the
  annotation says `body: "*"`.

Either way the path variables stay bindable, which a `body: "*"` over a nested
message would not be.

A response is the one success code that carries a body: a `$ref` becomes one
typed field, an inline object is flattened, an array becomes `repeated … items`,
and no body at all produces an empty message. `GET` and `HEAD` derivations also
get `idempotency_level = NO_SIDE_EFFECTS`.

### Nullable versus optional

Genuinely ambiguous, so the rule is stated rather than guessed:

| `required` | nullable | Result |
| --- | --- | --- |
| yes | no | a plain field |
| no | no | `optional` (scalars and enums; message and repeated fields already carry presence) |
| no | yes | `optional` — absent and null collapse, and a note says so |
| **yes** | **yes** | **error** |

`required` says the key must be present; `nullable` says its value may be null.
Proto3 cannot distinguish *present but null* from *absent*, so the combination
has no honest encoding. Resolve it by dropping `nullable`, dropping the
property from `required`, or writing `x-interchange-nullable: optional` to say
that null and absent mean the same thing here.

`nullable` on an array or a map is also an error: `repeated` has no presence in
proto3.

## `oneOf` and `anyOf`

Refused by default, with the location and this hint:

```
✗ openapi: components/schemas/Payment: 'oneOf' has no canonical proto form
    at payments.yaml:212:7
  → use a proto oneof, or flatten the variants and set x-interchange-oneof
```

To opt in, name the proto `oneof` to emit and give every variant a `$ref` to a
named schema — an inline variant has no name to give the field:

```yaml
PaymentMethod:
  type: object
  x-interchange-oneof: instrument
  required: [id]
  properties:
    id: {type: string}
  oneOf:
    - $ref: '#/components/schemas/Card'
    - $ref: '#/components/schemas/BankAccount'
```

```proto
message PaymentMethod {
  string id = 1;
  oneof instrument {
    Card card = 2;
    BankAccount bank_account = 3;
  }
}
```

`anyOf` opts in the same way, and adds a note: proto cannot express more than
one variant being set at a time, which `anyOf` allows.

## Everything this frontend refuses

Each is an error with the exact line, column and a hint.

| Construct | Why |
| --- | --- |
| `oneOf` / `anyOf` without `x-interchange-oneof` | no canonical proto form |
| an inline `oneOf` variant | nothing to name the oneof field |
| `not` | no proto form |
| an `allOf` member that is not an object | only objects can be flattened |
| an `allOf` cycle | no fixed point |
| a required **and** nullable property | proto3 cannot encode present-but-null |
| `nullable` on an array or map | `repeated` has no presence |
| a schema with no type, properties or `additionalProperties` | proto has no `any` |
| an array of arrays, of maps, or a map of either | `repeated` cannot nest |
| a non-string `enum` | proto enum values are named constants |
| an enum value that collides once uppercased | two fields, one name |
| a `$ref` that does not resolve | a dangling contract |
| an **external** `$ref` | a frontend never reads the filesystem — bundle the document |
| a derived name colliding with another | a silent suffix is a contract nobody meant |
| `POST` onto an item path (`/things/{id}`) | no defensible derived name |
| `HEAD`, `OPTIONS`, `TRACE` | transport concerns, not contract methods |
| a `GET` or `DELETE` with a `requestBody` | cannot be transcoded |
| a `header` or `cookie` parameter | transport metadata, not a contract field |
| a query parameter that is a map or object | cannot be bound |
| a path variable with no parameter declaration, or vice versa | a binding that never fires |
| two success responses carrying a body | an RPC returns one message |
| a response or body with no JSON media type | a schema-less field says nothing |
| an unknown `x-interchange-*` key | a misspelled annotation is worse than a missing one |
| duplicate `x-interchange-field` numbers | one of them would be lost |
| a sidecar key matching no procedure | an annotation the author believes is in force |
| an operation with no `auth` annotation | see below |
| a document that is not OpenAPI 3.x | convert a 2.0 document first |
| no proto package | the sidecar is keyed by procedure, which needs one |

Some are opt-out rather than fatal:

- `x-interchange-skip: true` on a parameter or property drops it deliberately.
- `x-interchange-nullable: optional` resolves the required-and-nullable case.
- `on_missing_auth` (below) downgrades the missing-`auth` error.

## Annotations

Two paths, per [§09](../../docs/09-schema-frontends.md) rule 3: `x-interchange-*`
vendor extensions in the document, and the sidecar as the universal fallback.
**The extension wins** — the sidecar is for a document you cannot edit, not an
override of one you can — and a document-root extension is the default for
operations that declare neither.

| Extension | Where | Emits |
| --- | --- | --- |
| `x-interchange-auth` | operation, document | `(interchange.auth.v1.auth)` |
| `x-interchange-transports` | operation, document | `(interchange.transport.v1.transports)` |
| `x-interchange-service-transports` | document | `(interchange.transport.v1.service_transports)` |
| `x-interchange-internal` | operation, document | `(interchange.transport.v1.internal)` |
| `x-interchange-cli` | operation, document | `(interchange.cli.v1.command)` |
| `x-interchange-oneof` | schema | a proto `oneof` |
| `x-interchange-field` | property | a pinned field number |
| `x-interchange-nullable` | property | the nullable opt-out |
| `x-interchange-skip` | property, parameter | omits it |
| `x-interchange-name` | operation | the RPC name |
| `x-interchange-service` | document | the service name |
| `x-interchange-package` | document | the proto package |

```yaml
paths:
  /v1/payments:
    get:
      x-interchange-auth:
        auth_types: [SESSION, API_KEY]        # or the full AUTH_TYPE_SESSION
        permission: {resource: payments, verb: READ}
      x-interchange-transports: {on: [RPC, REST, BUS], group: payments}
      x-interchange-cli: {path: [payments, list]}
```

`transports` also accepts the shorthand list form, `[RPC, REST]`.

`google.api.http` is **never** written by hand: it is derived from the path and
method, with the template variables rewritten to the snake_case field names
they bind to (`/v1/payments/{paymentId}` → `/v1/payments/{payment_id}`).

### The sidecar

`Sources.Sidecar`, keyed by full procedure string:

```yaml
# payments.interchange.yaml
procedures:
  /payments.v1.PaymentsService/ListDisputes:
    auth:
      auth_types: [SESSION, WORKLOAD]
      permission: {resource: disputes, verb: READ}
    transports: [RPC, REST]
    cli: {path: [disputes, list]}
  /payments.v1.PaymentsService/ReplayWebhook:
    internal: true
    auth:
      auth_types: [WORKLOAD]
      permission: {resource: webhooks, verb: EDIT}
      platform: true
```

The keys are `/<package>.<Service>/<Method>`, so the package must be known
before the sidecar can be applied — which is why an empty `Options.Package`
with no `x-interchange-package` is an error rather than a guess.

### Missing `auth`

An operation with no `auth` annotation from either path is **an error by
default**: an RPC with no declared security posture is exactly what the
annotation exists to prevent, and it is one of the items `ix import` lists as
needing a decision. A team working through an existing document can downgrade
it with `Options.Params["on_missing_auth"] = "warn"` or `"ignore"`.

### Where the annotation protos come from

This module does **not** import the optional modules' generated Go — a
frontend that linked `/auth` in to emit an auth annotation would make the
optional module mandatory. Their descriptors arrive in
`Options.Deps`; `ix` supplies the annotation protos of every module installed.
Core's own `transports` and upstream's `google.api.http` need no `Deps`.

An annotation whose descriptors nobody supplied is refused **at the
annotation**, not as a compiler error against source the author never wrote.
`internal/nolink` is a test package that links neither module and proves it.

## Options

| Option | Effect |
| --- | --- |
| `Options.Package` | the proto package; `x-interchange-package` overrides it |
| `Options.GoPackagePrefix` | prefix for the emitted `go_package` |
| `Options.Deps` | descriptors the emitted file may import |
| `Params["service"]` | the service name, used verbatim |
| `Params["on_missing_auth"]` | `error` (default), `warn`, `ignore` |
| `Sources.SidecarPath` | the name the sidecar's diagnostics carry |

With no service name given, it is derived from `info.title` with `Service`
appended. The emitted file lands at `<package path>/<snake service>.proto`.

## The report

`Import` returns a `Report` per document — the counts and the unresolved items,
so `ix import` renders the block from a structured value rather than
re-deriving it from descriptors. `Result.Summary()` renders that block:

```
  detected   OpenAPI 3.0.3
  frontend   openapi

  ✓ 11 paths      → 16 RPCs
  ✓ 29 schemas    → 28 messages
```

Schemas and messages differ when a schema is a type alias, which proto has no
form for and which is inlined instead.

## Tests

- `testdata/payments.yaml` + `testdata/payments.interchange.yaml` → the
  committed golden `testdata/payments.golden.proto`. Regenerate with
  `go test -run TestGolden -update ./...` **after reviewing the diff**.
- `TestDeterminism` imports four times and compares bytes and descriptors.
- `TestRefusals` is the total-or-loud table: every refusal above, asserting the
  error, the exact line and column, and the hint.
- `TestDerivedHTTPRules` asserts the `google.api.http` derivation against a
  table of paths, read back off the compiled descriptors.
- `TestDescriptorExtensions` reads every annotation back with
  `proto.GetExtension`, which is the call the authz interceptor and the binding
  plugins make.
- `internal/nolink` proves the optional modules are not linked.
