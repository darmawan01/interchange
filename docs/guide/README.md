# The guide

How to use Interchange. The [design docs](../) (`docs/00-problem.md` … `docs/11-cli.md`) explain
*why* everything is the way it is, and the [ADRs](../adr/) record what was decided and what it
cost. These eight pages are the task documentation: what you type, what comes back, and what to do
when it does not.

Every command on these pages was run against this repository, and every output was pasted from a
real terminal. Where a design doc's example output differs from what the tool actually prints
today, these pages show what it prints.

## What you write vs what is generated

This is the whole shape of the thing, on one screen.

```
YOU WRITE                                          INTERCHANGE GENERATES
─────────                                          ─────────────────────

api/catalog/v1/catalog.proto  ────────────────┬──▶ gen/go/catalog/v1/*.pb.go
  the contract: messages, RPCs, and six       │      message types
  annotations that decide which roads         │
  each RPC travels, who may call it, and      ├──▶ gen/go/catalog/v1/catalogv1connect/
  where it mounts in the CLI                  │      the Connect client (Go and browser)
                                              │
interchange.yaml  ────────────────────────────┤──▶ gen/go/catalog/v1/catalogv1bus/
  every generator, every transport default    │      ServiceDesc · the handler interface
                                              │      the typed bus client · Register()
server.go  ───────────────────────────────────┤
  the handler: an ordinary struct             ├──▶ gen/go/catalog/v1/catalogv1cli/
  implementing the generated interface.       │      the Cobra command tree
  No transport type in any signature.         │
                                              ├──▶ gen/go/authz/permissions.authz.go
wire.go  ─────────────────────────────────────┤      the procedure → permission table
  the composition root: ONE chain,            │
  ONE registry, one Mount per road.           ├──▶ gen/ts/**
  It does not grow when you add a road.       │      the front end's types + descriptor
                                              │
                                              └──▶ openapi.json
                                                     the partner-facing document
```

Two hand-written files per service, plus one composition root per binary. Everything under `gen/`
is **committed** and under the drift gate — `ix verify` regenerates and fails on a diff, which is
what makes "the contract is the only place the API exists" a build failure rather than a
convention.

You also never write: a subscriber, a REST handler, a router table, an SDK wrapper, a second
definition of any method on any transport.

## Reading order

Read 01 first. After that, 02–04 in order is the shortest path to a working service; 05–08 are
reference you come back to.

| # | Page | Who it is for |
| --- | --- | --- |
| **01** | [Getting started](01-getting-started.md) | **Everyone, first.** Installing `ix` (honestly — nothing is published yet), `ix init`, the scaffolded project, `ix generate`, `ix dev`, and the worked example running on three roads with real `curl` output |
| **02** | [Defining a contract](02-defining-a-contract.md) | Whoever writes the `.proto`. Layout, the naming rules that are load-bearing, and every annotation that exists with a worked example and what it produces. Plus the annotation band rules, if you are adding one of your own |
| **03** | [Serving a service](03-serving-a-service.md) | Whoever writes the composition root. The generated handler interface, one chain and one registry, chain ordering by named anchor, and mounting Connect, REST, NATS, MQTT and WebSocket |
| **04** | [Calling a service](04-calling-a-service.md) | Client authors — front end, Go peer, worker, CLI. The TypeScript client, the generated Go bus client and its explicit-deadline form, the dynamic `Invoke`, and the generated command tree. How credentials attach on each |
| **05** | [Cross-cutting concerns](05-cross-cutting.md) | Whoever owns authorization, validation, errors or telemetry. Installing `/auth`, `/validate` and `/errors`, and core's `Observer` seam. **An empty chain is valid; core works with none of them** |
| **06** | [Adding a transport](06-adding-a-transport.md) | Anyone adapting a broker Interchange does not ship. The six `Driver` methods, an honest `Capabilities`, `Inbound.Done`, and `drivertest.Run` |
| **07** | [Bring your own format](07-bring-your-own-format.md) | Teams with an existing OpenAPI document, or who would rather not write protobuf. `ix import`, the DSL, what each frontend refuses and why, and writing your own |
| **08** | [Operating it](08-operating-it.md) | Whoever owns CI and releases. The drift gate, `ix verify`/`lint`/`breaking`/`doctor`/`dev`, publishing the generated SDK, and the gotchas that are real in this repo |

## Where the reasoning lives

These pages cross-link rather than re-argue. When a page says "and this is why", it points at:

- **[`docs/00`–`docs/11`](../)** — the design. Why one operation gets defined three times
  ([§00](../00-problem.md)), the fan-out ([§01](../01-proposal.md)), the contract layer
  ([§02](../02-contract.md)), the envelope ([§03](../03-envelope.md)), bindings
  ([§04](../04-bindings.md)), chain symmetry ([§06](../06-crosscutting.md)), codegen
  ([§07](../07-codegen.md)), schema frontends ([§09](../09-schema-frontends.md)), extensibility
  ([§10](../10-extensibility.md)), the CLI ([§11](../11-cli.md)).
- **[`docs/adr/`](../adr/)** — fifty-four decisions, each with what it cost and the test that
  enforces it.
- **[`docs/annotation-band.md`](../annotation-band.md)** — the extension-number table. Claim a
  number here before you write the annotation.
- **Module READMEs** — [`auth/`](../../auth/README.md), [`errors/`](../../errors/README.md),
  [`validate/`](../../validate/README.md), [`tools/`](../../tools/README.md),
  [`ix/`](../../ix/README.md), [`driver/*/`](../../driver/), [`frontend/*/`](../../frontend/),
  [`examples/catalog/`](../../examples/catalog/README.md). These are the reference; the guide is
  the task-shaped version.
- **[`CONTRIBUTING.md`](../../CONTRIBUTING.md)** — the eight gates, and the rules for adding a
  transport, an annotation or a plugin.

## The worked example

[`examples/catalog`](../../examples/catalog) is one service, five RPCs, four roads and fourteen
acceptance tests named after the build-plan criteria they close. Its chain is asserted to run in
identical order over Connect, REST, an in-process bus and a real NATS broker. Every code sample in
this guide is copied from it, from a module README, or from the code the sample is about.

```bash
make plugins ix
cd examples/catalog
../../bin/ix describe CatalogService.ListProviders
go test ./...
```
