# 02 — The contract layer

A proto tree with mechanical naming and a small set of custom annotations. Mechanical naming is
what lets the URL, the subject, the CLI command and the SDK method all be **derived** instead of
maintained.

## Layout

```
api/
  buf.yaml                       # module name, deps, lint, breaking rules
  buf.lock                       # pinned dependency digests
  platform/
    catalog/v1/catalog.proto     # a real service
    orders/v1/orders.proto
    # --- core ---
    transport/v1/envelope.proto  # the transport-neutral envelope (§03)
    # --- optional modules, present only if you install them ---
    auth/v1/auth.proto           # the (auth) annotation
    auth/v1/permissions.proto    # closed Permission enum
    errors/v1/reasons.proto      # closed ErrorReason enum
    cli/v1/cli.proto             # CLI command overrides
    common/v1/types.proto        # PageRequest, Problem, shared scalars
```

```yaml
# api/buf.yaml
version: v2
modules:
  - path: .
    name: buf.build/<org>/api
deps:
  - buf.build/googleapis/googleapis      # google.api.http, field_behavior
  - buf.build/bufbuild/protovalidate     # declarative field rules
lint:
  use: [STANDARD]
breaking:
  # WIRE_JSON, not FILE: allows refactors that keep both the binary wire
  # form and the JSON field names compatible. FILE is stricter than a
  # public JSON surface actually requires.
  use: [WIRE_JSON]
```

## Naming is the derivation rule

Because the verb determines the HTTP method and the CLI subcommand, naming stops being a style
preference and becomes **load-bearing**.

| RPC verb | HTTP | NATS subject | CLI |
| --- | --- | --- | --- |
| `GetProvider` | `GET /v1/providers/{id}` | `rpc.…CatalogService.GetProvider` | `catalog provider <id>` |
| `ListProviders` | `GET /v1/providers` | `rpc.…CatalogService.ListProviders` | `catalog providers` |
| `CreateProvider` | `POST /v1/providers` | `rpc.…CatalogService.CreateProvider` | `catalog provider create` |
| `DrainNode` | `POST /v1/nodes/{id}:drain` | `rpc.…NodeService.DrainNode` | `node drain <id>` |

- **Services** are `{Entity}Service`, plural for collections.
- **Fields** are `snake_case`; ids end `_id`, timestamps end `_at`, durations carry their unit
  (`_ms`, `_seconds`).
- **Ids are strings** — ULID or KSUID, optionally type-prefixed (`prov_01H…`). Never integers.
- **Enums** are singular PascalCase with a mandatory `{ENUM}_UNSPECIFIED = 0`, and are append-only
  forever.
- `tenant_id` and `project_id` are a suggested scope chain, not a requirement. If you use them,
  the URL templates from those fields — and an authz module, if you install one, can find them
  by reflection.

## The annotation layer

> **Note.** The `auth` annotation below belongs to the **optional** `/auth` module, not to core.
> Core never parses it. It is shown here because it is the clearest worked example of the
> annotation mechanism — see [§06](06-crosscutting.md) and [§10](10-extensibility.md).

A custom option is an ordinary message attached to a descriptor by extending `MethodOptions` or
`FieldOptions`. Once attached it travels inside the file descriptor, which means it is readable in
**two** places: inside a codegen plugin at build time, and via reflection inside a running server.
**That dual availability is the whole mechanism.**

```protobuf
// api/platform/auth/v1/auth.proto
syntax = "proto3";
package platform.auth.v1;
import "google/protobuf/descriptor.proto";

enum AuthType {
  AUTH_TYPE_UNSPECIFIED = 0;
  AUTH_TYPE_API_KEY = 1;
  AUTH_TYPE_SESSION  = 2;
  AUTH_TYPE_WORKLOAD = 3;   // service-to-service identity
}

enum Verb { VERB_UNSPECIFIED = 0; VERB_READ = 1; VERB_CREATE = 2;
            VERB_EDIT = 3; VERB_DELETE = 4; }

message Permission {
  string resource = 1;      // "providers"
  Verb   verb     = 2;      // VERB_READ  =>  atom "providers.read"
}

message AuthOptions {
  // Which credential kinds are accepted. Under this module's default
  // policy, absent OR empty is a BUILD ERROR rather than "public" --
  // configurable, see §06. Public RPCs say so explicitly.
  repeated AuthType auth_types = 1;

  // Structured, not a free-form string, so a typo cannot mint a phantom
  // permission and codegen can group the catalog by resource.
  Permission permission = 2;

  bool public   = 3;        // explicit opt-out, greppable
  bool platform = 4;        // cross-tenant RPC: request carries no tenant
}

extend google.protobuf.MethodOptions {
  AuthOptions auth = 50001;
}

// Point the interceptor at the tenant fields when a message names them
// something other than tenant_id / project_id.
extend google.protobuf.FieldOptions {
  bool tenant_id_field  = 50007;
  bool project_id_field = 50008;
}
```

### Reserve an extension band before the second annotation exists

Field numbers on `MethodOptions` are a **global namespace**, and two annotations at the same number
is a silent, undebuggable collision. Take a range — `50000–59999` is the conventional private
band — and keep the assignments in one table.

| No. | Extends | Annotation | Consumed by |
| --- | --- | --- | --- |
| 50001 | `MethodOptions` | `auth` *(module)* | authz interceptor + the permission-table plugin |
| 50002 | `MethodOptions` | `transports` | binding plugins — which roads this RPC is exposed on |
| 50003 | `MethodOptions` | `internal` | skipped by every public binding; mTLS-only |
| 50004 | `MethodOptions` | `command` | CLI generator |
| 50007–8 | `FieldOptions` | `tenant_id_field`, `project_id_field` *(module)* | authz interceptor, by reflection |

## A fully annotated RPC

Read this as **five downstream artifacts declared in one place**.

```protobuf
service CatalogService {
  // ListProviders returns all registered providers.
  rpc ListProviders(ListProvidersRequest) returns (ListProvidersResponse) {

    // 1. REST binding + the derived OpenAPI path
    option (google.api.http) = {get: "/v1/catalog/providers"};

    // 2. lets a Connect client issue a real cacheable GET
    option idempotency_level = NO_SIDE_EFFECTS;

    // 3. which roads this RPC travels. Default is RPC + REST;
    //    naming BUS here is what emits the NATS subscriber.
    option (platform.transport.v1.transports) = {
      on: [TRANSPORT_RPC, TRANSPORT_REST, TRANSPORT_BUS]
    };

    // 4. authorization -- OPTIONAL module; if installed, the chain
    //    applies it identically on all three roads
    option (platform.auth.v1.auth) = {
      auth_types: [AUTH_TYPE_SESSION, AUTH_TYPE_API_KEY, AUTH_TYPE_WORKLOAD]
      permission: {resource: "providers" verb: VERB_READ}
    };

    // 5. CLI: mounts as `platform catalog providers`
    option (platform.cli.v1.command) = {path: ["catalog", "providers"]};
  }
}
```

The `transports` annotation is the piece a plain RPC stack has no equivalent for, and it is what
makes the fan-out **reviewable**. Exposing an internal admin RPC on the public message bus becomes a
one-line diff a reviewer can see, rather than an emergent property of which adapter happened to be
wired up.
