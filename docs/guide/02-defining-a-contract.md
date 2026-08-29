# 02 — Defining a contract

Everything downstream — the URL, the bus subject, the CLI command, the permission atom, the SDK
method — is **derived** from this file. That is what makes naming load-bearing rather than
stylistic, and it is why the annotations are worth reading carefully.

The running example is [`examples/catalog/api/catalog/v1/catalog.proto`](../../examples/catalog/api/catalog/v1/catalog.proto):
one service, five RPCs, every annotation that exists. Design rationale lives in
[§02 The contract layer](../02-contract.md); this page is how to write one.

## Layout

```
api/
  buf.yaml                       # module, deps, lint and breaking rules
  buf.lock                       # pinned dependency digests
  catalog/v1/catalog.proto       # yours
  orders/v1/orders.proto

  interchange/transport/v1/transports.proto   # core: (transports), (internal)
  interchange/cli/v1/cli.proto                # /tools: (command)
  interchange/auth/v1/auth.proto              # /auth, optional: (auth)
  interchange/errors/v1/reasons.proto         # /errors, optional
```

`ix init` writes the core and `/tools` annotation protos **into your `api/` tree** rather than
adding a buf dependency on them, so a fresh project builds and lints with nothing but `buf` on
`PATH`. The optional modules' protos arrive when you install those modules.

The file path is not free: proto's own import mechanism means `catalog/v1/catalog.proto` is what
everyone else writes in `import`, so the directory must match the package.

```yaml
# buf.yaml
version: v2
modules:
  - path: api
lint:
  use: [STANDARD]
breaking:
  # WIRE_JSON, not FILE: it allows refactors that keep both the binary wire
  # form and the JSON field names compatible, which is what a public JSON
  # surface actually requires.
  use: [WIRE_JSON]
```

`WIRE_JSON` over `FILE` is a decision, not a default — see
[ADR-0004](../adr/0004-wire-json-breaking-rule.md).

## The naming rules that are load-bearing

`ix lint` enforces these as **errors**, because each one is a derivation input. Breaking them does
not produce ugly code; it produces a wrong URL or a bus subject that no subscriber matches.

| Rule | `ix lint` id | Why it is an error |
| --- | --- | --- |
| Services end in `Service` | `SERVICE_SUFFIX` | the CLI command and the bus subject derive from the service name |
| Fields are `snake_case` | `FIELD_SNAKE_CASE` | the JSON name, the URL template variable and the CLI flag all derive from it |
| Ids are strings, never integers | `ID_IS_STRING` | an integer id leaks row counts and cannot be minted by a client |
| Every enum has `{ENUM}_UNSPECIFIED = 0` | `ENUM_ZERO_UNSPECIFIED` | proto3 reads an unset field as zero; without it there is no way to tell "not set" from the first real choice |
| Enums are append-only | `ENUM_APPEND_ONLY` | renumbering is a silent wire break |

And as warnings: a `Timestamp` field should end `_at` (`TIMESTAMP_SUFFIX`), a scalar duration should
carry its unit (`DURATION_UNIT`), and an RPC on the REST road with no `google.api.http` rule has no
derivable address (`REST_NO_HTTP_RULE`).

The verb in the RPC name is what drives the derivation:

| RPC verb | HTTP | Bus subject | CLI |
| --- | --- | --- | --- |
| `GetProvider` | `GET /v1/providers/{id}` | `rpc.catalog.v1.CatalogService.GetProvider` | `catalog provider <id>` |
| `ListProviders` | `GET /v1/providers` | `rpc.catalog.v1.CatalogService.ListProviders` | `catalog providers` |
| `CreateProvider` | `POST /v1/providers` | `rpc.catalog.v1.CatalogService.CreateProvider` | `catalog provider create` |

`tenant_id` and `project_id` are a *suggested* scope chain, not a requirement — but if you use
them, the `/auth` module finds them by reflection with no annotation at all. See
[ADR-0005](../adr/0005-mechanical-naming.md).

## The annotations

Six things you can attach. Two are core, one belongs to `/tools`, one to the optional `/auth`
module, and two are upstream protobuf/googleapis features that Interchange reads.

| Annotation | Owner | Attaches to | Produces |
| --- | --- | --- | --- |
| `(interchange.transport.v1.service_transports)` | core | service | the default road set for every RPC in it |
| `(interchange.transport.v1.transports)` | core | method | which roads this RPC travels, and its competing-consumer group |
| `(interchange.transport.v1.internal)` | core | method | every *public* binding skips it |
| `(google.api.http)` | googleapis | method | the REST route and the OpenAPI path |
| `idempotency_level` | protobuf | method | a cacheable `GET` on Connect |
| `(interchange.auth.v1.auth)` | `/auth` *(optional)* | method | the permission check, and the generated permission table |
| `(interchange.cli.v1.command)` | `/tools` | method | where this RPC mounts in the generated command tree |

### `(transports)` and the service-level default

Declare the default once on the service, so a per-method annotation is a deliberate, reviewable
exception rather than boilerplate on every RPC:

```protobuf
service CatalogService {
  option (interchange.transport.v1.service_transports) = {
    on: [TRANSPORT_RPC, TRANSPORT_REST]
  };

  rpc ListProviders(ListProvidersRequest) returns (ListProvidersResponse) {
    option (interchange.transport.v1.transports) = {
      on: [TRANSPORT_RPC, TRANSPORT_REST, TRANSPORT_BUS]
      group: "catalog"
    };
  }
}
```

Roads: `TRANSPORT_RPC` `TRANSPORT_REST` `TRANSPORT_BUS` `TRANSPORT_MQTT` `TRANSPORT_WS`.

**A per-method annotation replaces the service default entirely — group included.** It does not
merge. A reviewer reads one annotation instead of composing two ([ADR-0006](../adr/0006-service-level-transport-default.md)).
With neither annotation, an RPC gets `TRANSPORT_RPC, TRANSPORT_REST`.

`group` is the competing-consumer group: a NATS queue group, an MQTT `$share`. Without it, every
subscriber on the bus receives every message.

**What it produces.** `protoc-gen-bus` writes the resolved list into the `MethodDesc`, sorted by
enum value and deduped — it is a set, and generated bytes must not depend on typing order:

```go
Transports: []transportv1.Transport{
    transportv1.Transport_TRANSPORT_RPC,
    transportv1.Transport_TRANSPORT_REST,
    transportv1.Transport_TRANSPORT_BUS,
},
Group: "catalog",
```

`rpc.Binding.Mount` and `rest.Binding.Mount` skip a method that does not name their road, and
`engine.Server.Plan` subscribes only the ones that name a bus road. The annotation is not
documentation; it is the reachability decision.

**Refused, with the source location:** a streaming RPC (Interchange is unary only,
[ADR-0051](../adr/0051-streaming-is-deferred.md)); an annotation that names no road at all
(`{group: "x"}` reads both as "only change the group" and "expose on nothing", and guessing is how
an internal RPC reaches a public bus); and `TRANSPORT_UNSPECIFIED`.

### `(internal)`

```protobuf
rpc Reconcile(ReconcileRequest) returns (ReconcileResponse) {
  option (interchange.transport.v1.internal) = true;
  option (interchange.transport.v1.transports) = {on: [TRANSPORT_BUS]};
}
```

```
$ ix describe CatalogService.Reconcile

  TRANSPORTS
    rpc       not exposed
    rest      not exposed
    bus       rpc.catalog.v1.CatalogService.Reconcile
                queue group: none · at-least-once: no · max payload: 1 MiB
    (internal) is set: every public binding skips this RPC; mTLS-only
```

`internal` means **public bindings skip it**, not that it is unreachable
([ADR-0050](../adr/0050-internal-means-public-bindings-skip-it.md)). `rpc.Binding.Mount` and
`rest.Binding.Mount` both skip it and it is absent from the emitted OpenAPI; `engine.Server.Plan`
deliberately does not, because `internal` + `bus` is precisely how an RPC is made reachable
service-to-service and unreachable everywhere else. The acceptance test
`TestTheTransportsAnnotationIsLoadBearing` asserts both halves against a real NATS broker.

### `google.api.http`

The REST route, and the path in the emitted OpenAPI document:

```protobuf
rpc ListProviders(...) returns (...) {
  option (google.api.http) = {get: "/v1/catalog/providers"};
}

rpc GetProvider(...) returns (...) {
  option (google.api.http) = {get: "/v1/catalog/providers/{provider_id}"};
}

rpc CreateProvider(...) returns (...) {
  option (google.api.http) = {
    post: "/v1/catalog/providers"
    body: "*"
  };
}
```

Template variables bind to request fields by name, so `{provider_id}` requires a `provider_id`
field. `body: "*"` maps the whole JSON body onto the request message.

Needs `buf dep add buf.build/googleapis/googleapis` and `import "google/api/annotations.proto"`.
It is a route declaration only — there is no second handler and no second contract behind it. The
REST binding transcodes off the same registry ([03](03-serving-a-service.md#the-rest-road)).

### `idempotency_level`

Upstream protobuf, no import needed:

```protobuf
option idempotency_level = NO_SIDE_EFFECTS;
```

`protoc-gen-bus` turns it into `Idempotent: true` on the `MethodDesc`, and the Connect binding lets
a client issue a real cacheable `GET` rather than a `POST`. `ix describe` reports it as
`idempotent   yes (NO_SIDE_EFFECTS)`.

### `(auth)` — the optional module

Core never parses this. `hack/depcheck.sh` asserts core does not import `/auth` at all. If the
module is installed, the chain applies it identically on every road; if it is not, the annotation
is inert data on the descriptor ([ADR-0019](../adr/0019-authorization-is-a-module.md)).

```protobuf
rpc ListProviders(...) returns (...) {
  option (interchange.auth.v1.auth) = {
    auth_types: [AUTH_TYPE_SESSION, AUTH_TYPE_API_KEY, AUTH_TYPE_WORKLOAD]
    permission: {resource: "providers" verb: VERB_READ}   // atom: providers.read
  };
}

rpc Reconcile(...) returns (...) {
  option (interchange.auth.v1.auth) = {
    auth_types: [AUTH_TYPE_WORKLOAD]
    permission: {resource: "providers" verb: VERB_EDIT}
    platform: true          // cross-tenant: the request carries no tenant
  };
}

rpc Health(...) returns (...) {
  option (interchange.auth.v1.auth) = {public: true};     // explicit, greppable
}
```

`AuthType`: `AUTH_TYPE_API_KEY` `AUTH_TYPE_SESSION` `AUTH_TYPE_WORKLOAD`.
`Verb`: `VERB_READ` `VERB_CREATE` `VERB_EDIT` `VERB_DELETE`. The permission is structured rather
than a free-form string so a typo cannot mint a phantom permission, and `{resource, verb}`
flattens to the atom `providers.read`.

**Absent is not public** ([ADR-0020](../adr/0020-absent-is-not-public.md)). Under the default
policy an unannotated method is denied at runtime *and* fails the build in `protoc-gen-authz`. A
public RPC says so.

**What it produces**, two artifacts from one annotation — the build-time gate and the runtime
check, which is the point.

The gate, from `protoc-gen-authz`, sorted by procedure so the file is byte-identical for
byte-identical input:

```go
// examples/catalog/gen/go/authz/permissions.authz.go
//
// The build-time half of "enforce twice": this table and the authz
// interceptor read the same (interchange.auth.v1.auth) annotation. [...]
// A procedure with no row here has no declaration -- treat it as denied.

var rules = []Rule{
	{
		Procedure:  "/catalog.v1.CatalogService/CreateProvider",
		Permission: "providers.create",
		AuthTypes:  []string{"AUTH_TYPE_SESSION", "AUTH_TYPE_WORKLOAD"},
	},
	{
		Procedure:  "/catalog.v1.CatalogService/GetProvider",
		Permission: "providers.read",
		AuthTypes:  []string{"AUTH_TYPE_SESSION", "AUTH_TYPE_API_KEY", "AUTH_TYPE_WORKLOAD"},
	},
	// ...
}
```

`Rule` also carries `Public` (the explicit opt-out — absent is not public, so it is only ever true
because the contract said so) and `Platform`. The table can be handed to an edge gateway; the
interceptor decides at runtime with the message in hand.

And the runtime view of the same annotation:

```
$ ix describe CatalogService.ListProviders
  AUTHORIZATION
    permission   providers.read
    accepts      SESSION, API_KEY, WORKLOAD
    public       no
    tenant field tenant_id (convention)
```

`tenant field ... (convention)` is the reflective lookup: the interceptor found a `tenant_id`
string field with no annotation. If your message calls it something else, say so on the field:

```protobuf
message ListProvidersRequest {
  string org_id = 1 [(interchange.auth.v1.tenant_id_field) = true];
}
```

Then `ix describe` reports `(declared)` instead. Details in [05](05-cross-cutting.md#auth).

### `(cli)`

```protobuf
option (interchange.cli.v1.command) = {path: ["catalog", "providers"]};

option (interchange.cli.v1.command) = {
  path: ["catalog", "provider"]
  args: ["provider_id"]                 // positional, must name a scalar field
};

option (interchange.cli.v1.command) = {skip: true};   // deliberately not on the CLI
```

Fields: `path`, `args`, `short`, `long`, `skip`. `protoc-gen-cli` emits a Cobra tree with a flag
per scalar top-level request field (kebab-cased), the positionals from `args`, and
`--request-json` for anything no single token can carry — repeated, map, or message fields.

**What it produces:**

```
$ go run ./cmd/catalogctl coverage
catalog.v1.CatalogService: 4/5 covered, 1 skipped, 0 unannotated
```

Coverage is printable on purpose. `docs/08` names the risk — *"a generated CLI that covers only 80%
of RPCs may be worse than none"* — and the failure mode is not the gap but the gap being invisible.
Two answers, pick per repo: `Coverage()` makes it printable, and the plugin option
`require_annotation=true` makes an unannotated RPC a build failure
([ADR-0038](../adr/0038-the-cli-reports-its-coverage.md)). The catalog example sets it.

## The annotation band

Field numbers on `google.protobuf.MethodOptions` are a **global namespace**. Two annotations at the
same number is a silent, undebuggable collision: the descriptor still parses and one option is
simply gone. Reserved band: **50000–59999**.

| No. | Extends | Annotation | Module |
| --- | --- | --- | --- |
| 50001 | `MethodOptions` | `interchange.auth.v1.auth` | `/auth` *(optional)* |
| 50002 | `MethodOptions` | `interchange.transport.v1.transports` | core |
| 50002 | `ServiceOptions` | `interchange.transport.v1.service_transports` | core |
| 50003 | `MethodOptions` | `interchange.transport.v1.internal` | core |
| 50004 | `MethodOptions` | `interchange.cli.v1.command` | `/tools` |
| 50007 | `FieldOptions` | `interchange.auth.v1.tenant_id_field` | `/auth` *(optional)* |
| 50008 | `FieldOptions` | `interchange.auth.v1.project_id_field` | `/auth` *(optional)* |

Free: `50005`, `50006`, `50009–59999`. The authoritative table is
[`docs/annotation-band.md`](../annotation-band.md), and `ix lint` reads it from the project root
when present (`--band <file>` overrides).

**If you are adding your own annotation:**

1. **Claim the number in `docs/annotation-band.md` first.** A PR without a row does not merge, and
   `ix lint` fails with `BAND_UNREGISTERED` on an extension in the band with no table row.
2. **Put the proto in the module that consumes it.** Core does not reserve space for an annotation
   it will never parse.
3. **Never renumber.** A renumbered option is a silently dropped annotation on every descriptor
   built before the change — and for an authorization option, that is a check that stops firing.
4. Different extendees may share a number: `50002` is `transports` on `MethodOptions` and
   `service_transports` on `ServiceOptions`, and they cannot collide because the extendee is part
   of the identity. Both still get a row.

`ix lint` checks all three: `BAND_UNREGISTERED`, `BAND_MISMATCH` (a number claimed by a different
annotation than the table says) and `BAND_COLLISION` (two annotations at one number on one
extendee). Rationale: [ADR-0003](../adr/0003-reserve-an-annotation-band.md).

**Reading your annotation at runtime** — never off `Descriptor.Options()` directly. Use
`interchange.MethodOptions(md)` / `ServiceOptions` / `FieldOptions`. A descriptor built by
protocompile or a schema frontend carries custom options as `dynamicpb` values or unknown bytes,
and `proto.GetExtension` reads a *present* annotation as **absent** against those, without an
error. Core owns `ResolveOptions` for exactly this, and it does not need to know what your
annotation means ([ADR-0035](../adr/0035-read-annotations-through-core.md)).

## Checking your work

```bash
ix lint                                  # buf STANDARD, plus the naming rules and the band
ix describe CatalogService.ListProviders # what the contract exposes, on every road
ix breaking --against '.git#branch=main' # WIRE_JSON compatibility
```

```
$ ix lint
  ✓ lint            5 RPCs, 8 extensions, band ix builtin annotation band
```

Next: [03 Serving a service](03-serving-a-service.md).
