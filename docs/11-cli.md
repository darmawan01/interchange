# 11 — The CLI

One binary, `ix`. It is the only thing a user installs, and it wraps the whole pipeline — import,
generate, format, lint, breaking-change check, drift gate, and inspection.

**Design goal: `ix init` to a generated, typed client in under a minute, with no protobuf knowledge
required.** Every other command exists to keep that true as the project grows.

## Commands

| Command | Does |
| --- | --- |
| `ix init` | Scaffold a project — `interchange.yaml`, `api/`, a starter service, CI config |
| `ix import <file>` | Bring a non-proto source into the canonical tree; auto-detects the format |
| `ix generate` | Run the full pipeline: frontends → IR → every configured generator |
| `ix fmt` | Format sources in place |
| `ix lint` | Lint the contract *and* the annotations — the naming rules, the extension band, missing `auth` |
| `ix breaking --against <ref>` | Breaking-change detection against a git ref or a registry |
| `ix verify` | The drift gate: regenerate and fail if the tree moved. **This is what CI runs.** |
| `ix describe <rpc>` | Show what an RPC resolves to on **every** transport |
| `ix plugin` | List, add, pin generators and adapters |
| `ix dev` | Local loopback server — exercise a contract with no infrastructure |
| `ix doctor` | Diagnose a broken setup: version skew, missing plugins, stale lockfile |

## `ix describe` — the reviewability property, as a command

The proposal's claim is that the fan-out is *reviewable*. This is what makes that concrete: one
command answers "what does this method actually expose, and who can reach it?"

```
$ cd examples/catalog
$ ix describe CatalogService.ListProviders

  procedure   /catalog.v1.CatalogService/ListProviders
  request     ListProvidersRequest  (tenant_id, page)
  response    ListProvidersResponse (providers[], next_page_token)

  TRANSPORTS
    rpc       POST /catalog.v1.CatalogService/ListProviders
    rest      GET  /v1/catalog/providers
    bus       rpc.catalog.v1.CatalogService.ListProviders
                queue group: catalog · at-least-once: no · max payload: 1 MiB
    mqtt      not exposed
    ws        not exposed

  AUTHORIZATION
    permission   providers.read
    accepts      SESSION, API_KEY, WORKLOAD
    public       no
    tenant field tenant_id (convention)

  CLI          catalog providers
  idempotent   yes (NO_SIDE_EFFECTS)
```

Run it in review and "should this be on the public bus?" stops being a question nobody thought to
ask.

That is real output from `examples/catalog`, not a sketch. `(convention)` after the tenant field
means the interceptor found `tenant_id` by name rather than by an explicit `(tenant_id_field)`
annotation — the command distinguishes the two, because "the framework guessed" and "somebody
declared it" are different amounts of confidence.

## `ix import` — the on-ramp

```
$ cd examples/catalog
$ ix import ../../frontend/openapi/testdata/payments.yaml

  detected   OpenAPI 3.0.3
  frontend   openapi

  ⚠ 1 construct(s) need a decision:

    payments.yaml  no proto package for this document
                   → set Options.Package, or x-interchange-package at the document root

  nothing written — resolve the 1 item(s) above, then re-run
```

Give it the package and it converts, reporting what it had to decide along the way:

```
$ ix import ../../frontend/openapi/testdata/payments.yaml \
    --sidecar ../../frontend/openapi/testdata/payments.interchange.yaml \
    --package payments.v1 --dry-run

  detected   OpenAPI 3.0.3
  frontend   openapi

  ✓ components/schemas/Payment/allOf: 2 allOf member(s) flattened into Payment
  ✓ components/schemas/Error/properties/details: free-form object mapped to
    google.protobuf.Struct; its shape is not part of the contract
  ✓ components/schemas/Currency: named string is a type alias; proto has none,
    so it is inlined at every use
  would write api/payments/v1/payments_service.proto (16279 bytes)
```

**It refuses to emit a partial contract.** A frontend that silently drops what it cannot represent
produces a contract that lies, which is the exact failure this project exists to remove
([§09](09-schema-frontends.md)).

## `ix verify` — the gate

```
$ cd examples/catalog
$ ix verify
  ✓ frontends       1 sources → 6 descriptors
  ✓ annotations     5 RPCs, 5 annotated, 0 public (reviewed)
  ✓ generators      6 targets
  ✓ drift           generated output matches the contract
```

And when something has moved:

```
  ✗ drift           gen/ts/catalog/v1/catalog_pb.ts differs

  generated output is stale — run `ix generate`
```

One command in CI. It is the whole reason the contract cannot drift.

## Configuration

`interchange.yaml` at the repo root — see [§10](10-extensibility.md) for the full schema. Every
command reads it; no command needs flags for anything the file already says.

## Distribution

| Channel | Form |
| --- | --- |
| Go | `go install .../ix@latest` |
| Homebrew | `brew install <tap>/ix` |
| npm | `npx @interchange/cli` — so a front-end team never installs a Go toolchain |
| Container | `docker run ghcr.io/<org>/ix generate` — for CI without a local install |
| Binaries | Static per-platform builds on each release |

The npm channel matters more than it looks: **the front end is the consumer with the most to gain
and the least tolerance for a Go toolchain.** If getting typed clients requires installing Go, the
adoption story breaks at exactly the team the project is most useful to.

## What the CLI does not do

- **It does not run your services.** `ix dev` is a loopback for exercising a contract, not a service
  mesh or a process manager.
- **It does not host a registry.** It can *talk* to one; it is not one.
- **It does not hide the generated output.** Generated code is committed and reviewable. A CLI that
  makes generation invisible makes drift invisible with it.
