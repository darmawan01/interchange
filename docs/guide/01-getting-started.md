# 01 — Getting started

From nothing to a contract that dispatches, then to the worked example that serves it on four
roads. Ten minutes, and no protobuf knowledge assumed.

## Installing `ix`

```
$ go install github.com/darmawan01/interchange/ix/cmd/ix@v0.1.1
$ ix --version
ix version v0.1.1
```

That is the whole install. `ix` shells out to [`buf`](https://buf.build/docs/installation), which
is the one other thing you need on PATH — `ix doctor` checks for it and tells you if it is missing.

The design names five distribution channels ([§11](../11-cli.md)); one of them is live. Verified
2026-08-30:

| Channel | Command | Today |
| --- | --- | --- |
| **Go** | `go install github.com/darmawan01/interchange/ix/cmd/ix@v0.1.1` | **works** |
| Clone + build | `make ix` | works, and is what to use if you are changing `ix` itself |
| npm | `npx @interchange/cli` | 404 — `packages/cli` is written and verified with `npm pack`, not published |
| Homebrew | `brew install <tap>/ix` | no tap exists; `.goreleaser.yaml` has the formula, uploads are off |
| Container | `docker run ghcr.io/<org>/ix generate` | `Dockerfile` builds it; no image is pushed |

The npm channel is the one that matters most for adoption and is not live yet — see
[ADR-0049](../adr/0049-the-npm-channel-is-first-class.md) for why a front-end team should never
need a Go toolchain to get typed clients.

So the reliable path is a clone:

```bash
git clone git@github.com:darmawan01/interchange.git
cd interchange
make plugins ix        # installs pinned protoc-gen-go/connect-go into ./bin, builds the local
                       # plugins and the CLI. No network beyond the Go module proxy.
export PATH="$PWD/bin:$PATH"
```

`make plugins` and `make ix` are separate targets on purpose: `ix` alone is enough to `init`,
`describe`, `lint` and `dev`; the plugins are only needed to generate Go from this repo's own
pinned binaries.

`ix` needs [`buf`](https://buf.build/docs/installation) on `PATH`, and the Go toolchain if your
project configures local plugins. `ix doctor` checks both.

> Once the module is published, `go install` becomes the one-liner and everything below is
> unchanged. The scaffolded CI workflow already assumes it — see [§08](08-operating-it.md#the-ci-workflow-ix-init-writes-does-not-pass-today).

## `ix init`

```
$ ix init todo --name todo

  + .github/workflows/interchange.yml
  + Makefile
  + api/interchange/cli/v1/cli.proto
  + api/interchange/transport/v1/transports.proto
  + api/todo/v1/todo.proto
  + buf.yaml
  + interchange.yaml
  + buf.gen.yaml

  next:
    ix describe TodoService.ListTodos    what the contract already exposes, on every road
    ix lint         the naming rules and the annotation band
    ix generate     typed clients for every configured target
    ix dev          exercise the contract with no infrastructure
```

The positional argument is the **directory**. `--name` is the proto package and service prefix
(`todo` → package `todo.v1`, service `TodoService`); it defaults to `example`. `--go-module` sets
the import path generated Go lives under, and is read from a `go.mod` beside you when there is one
— otherwise it is a placeholder (`example.com/todo/gen/go`) you are expected to edit.

### What each file is

| File | Yours to edit | What it is |
| --- | --- | --- |
| `api/todo/v1/todo.proto` | **yes** | the contract — a starter service with two RPCs, fully annotated |
| `interchange.yaml` | **yes** | every generator, every transport default, managed mode. Every `ix` command reads it |
| `buf.yaml` | rarely | the buf workspace: `STANDARD` lint, `WIRE_JSON` breaking ([ADR-0004](../adr/0004-wire-json-breaking-rule.md)) |
| `buf.gen.yaml` | **no** | synthesized from `interchange.yaml` so `buf generate` works without `ix`. `ix verify` fails if the two disagree; `ix plugin sync` rewrites it |
| `api/interchange/**` | no | the annotation protos, **written into your tree** rather than fetched, so the project builds and lints with nothing but `buf` on `PATH` |
| `Makefile` | yes | one target per `ix` command |
| `.github/workflows/interchange.yml` | yes | lint, breaking, and the drift gate |

Nothing under `gen/` exists yet. That is the point of the next command.

## `ix describe` — before you write any Go

The contract already says what it exposes. Ask it:

```
$ ix describe TodoService.ListTodos

  procedure   /todo.v1.TodoService/ListTodos
  request     ListTodosRequest  (page_size, page_token)
  response    ListTodosResponse (todos[], next_page_token)

  TRANSPORTS
    rpc       POST /todo.v1.TodoService/ListTodos
    rest      not exposed
    bus       rpc.todo.v1.TodoService.ListTodos
                queue group: todo · at-least-once: no · max payload: 1 MiB
    mqtt      not exposed
    ws        not exposed

  AUTHORIZATION
    not declared (the (auth) annotation is an optional module)

  CLI          todo list
  idempotent   yes (NO_SIDE_EFFECTS)
```

`rest not exposed` because the scaffold's `ListTodos` names `TRANSPORT_RPC` and `TRANSPORT_BUS`
and carries no `google.api.http` rule — the starter proto has a comment telling you exactly what to
add. `AUTHORIZATION not declared` because `/auth` is optional and the scaffold installs none of it.

This is the command to run in code review. See [§11](../11-cli.md#ix-describe--the-reviewability-property-as-a-command)
for why it exists.

## `ix generate`

```
$ ix generate
$                                       # silence is success
$ find . -name '*.go' | sort
./gen/go/interchange/cli/v1/cli.pb.go
./gen/go/interchange/transport/v1/transports.pb.go
./gen/go/todo/v1/todo.pb.go
./gen/go/todo/v1/todov1connect/todo.connect.go
```

`ix generate` prints nothing when it succeeds. What ran is `buf generate` against a template
synthesized from `interchange.yaml`; `ix --verbose generate` echoes the exact buf invocation.

The scaffold configures two **remote** plugins (`buf.build/protocolbuffers/go`,
`buf.build/connectrpc/go`), so this call needs the network. That is fine for a first run and wrong
for CI — see [§08](08-operating-it.md#remote-plugins-are-rate-limited).

The scaffold does not write a `go.mod`, so `gen/go` is not yet a compilable Go module. Add one
with the module path you put in `interchange.yaml`'s `go_package_prefix`, and the generated types
and the generated Connect client are importable.

## `ix dev` — exercise the contract with no infrastructure

Before any handler exists:

```
$ ix dev

  ✓ dev             2 procedure(s) over driver/memory
      subscribed  rpc.todo.v1.TodoService.CreateTodo
      subscribed  rpc.todo.v1.TodoService.ListTodos  (queue group: todo)

      /todo.v1.TodoService/CreateTodo
      /todo.v1.TodoService/ListTodos

  listening on .interchange/dev.sock
  stub responses only — this exercises the contract, not your handlers
```

```
$ ix dev call TodoService.ListTodos '{"page_size":2}'
{"todos":[],"nextPageToken":""}
```

The response is a default-valued stub built from the descriptor by reflection. What it proves is
that the procedure resolves, the request decodes against the declared shape, and the envelope made
a real round trip through the real engine and the real chain over `driver/memory` — which is a real
driver, not a mock ([ADR-0029](../adr/0029-the-memory-driver-is-real.md)). It proves nothing about
your handler, because there isn't one.

`ix dev call` also works with no server running: it starts a loopback, makes the call and exits.

> **Gotcha.** The loopback binds a unix socket at `<project>/.interchange/dev.sock`. A deeply nested
> project path overruns the platform's ~104-byte socket-path limit and `ix dev` exits with
> `bind: invalid argument`. `ix dev call` still works, because it falls back to its own loopback —
> so the failure is easy to miss. Keep the project path short, or run `ix dev call` alone.

## The real thing: `examples/catalog`

The scaffold is a contract. `examples/catalog` is a contract **plus a handler plus a composition
root**, and it is the reference for everything in the rest of this guide: one service, five RPCs,
four roads, one handler.

```bash
cd examples/catalog
make -C ../.. ix
../../bin/ix describe CatalogService.ListProviders
```

```
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

Three roads, a permission atom, a tenant field found by convention, and a CLI path — all read off
one `.proto` file. Nothing here was configured in Go.

### Run it

```bash
go run ./cmd/catalogd
```

```
time=2026-08-30T08:11:10.558+10:00 level=INFO msg=serving
  connect=:8080/catalog.v1.CatalogService/*
  rest=:8080/v1/catalog/providers
  bus=memory
  procedures="[/catalog.v1.CatalogService/CreateProvider /catalog.v1.CatalogService/GetProvider
               /catalog.v1.CatalogService/ListProviders /catalog.v1.CatalogService/Reconcile
               /catalog.v1.CatalogService/SyncProvider]"
```

Three roads out of one process, and `--nats nats://localhost:4222` swaps the in-process bus for a
real broker without touching anything above the driver.

**The partner surface**, snake_case, transcoded:

```
$ curl -s -H "Authorization: Bearer reader-token" \
    "localhost:8080/v1/catalog/providers?tenant_id=acme"
{"providers":[{"provider_id":"prov_00000001", "tenant_id":"acme", "display_name":"stripe",
  "status":"PROVIDER_STATUS_ACTIVE", "created_at":"2026-08-29T22:11:10.557449Z"}, ...],
  "next_page_token":""}
```

**The Connect road**, camelCase, same handler, same registry:

```
$ curl -s -X POST -H "Content-Type: application/json" \
    -H "Authorization: Bearer reader-token" -d '{"tenantId":"acme"}' \
    localhost:8080/catalog.v1.CatalogService/ListProviders
{"providers":[{"providerId":"prov_00000001", "tenantId":"acme", "displayName":"stripe",
  "status":"PROVIDER_STATUS_ACTIVE", "createdAt":"2026-08-29T22:11:10.557449Z"}, ...]}
```

Per-surface casing is deliberate, not an accident: partners already parse `snake_case` and browsers
already expect `camelCase`. [§04 Bindings](../04-bindings.md) has the reasoning; the acceptance
test `TestPerSurfaceJSONCasingCamelCaseOnRPCSnakeCaseOnREST` is what keeps it true.

**Drop the credential**, and the chain answers before the handler does:

```
$ curl -s -i "localhost:8080/v1/catalog/providers?tenant_id=acme"
HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json
Ix-Reason: UNAUTHENTICATED

{"type":"about:blank", "title":"Unauthorized", "status":401,
 "detail":"/catalog.v1.CatalogService/ListProviders: no credential",
 "instance":"/v1/catalog/providers", "reason":"UNAUTHENTICATED"}
```

That `Ix-Reason` header is set identically on the Connect road, and the same string travels in the
envelope on the bus. One reason, four surfaces — [§05](05-cross-cutting.md#one-error-four-surfaces).

**The generated CLI**, against the same running server:

```
$ go run ./cmd/catalogctl catalog providers --tenant-id acme --token reader-token
{
  "providers": [
    {
      "providerId": "prov_00000001",
      "tenantId": "acme",
      "displayName": "stripe",
      "status": "PROVIDER_STATUS_ACTIVE",
      "createdAt": "2026-08-29T22:11:10.557449Z"
    },
    ...
  ]
}
```

No subcommand, flag or argument in `cmd/catalogctl/main.go` was hand-written — they all come from
`(interchange.cli.v1.command)` in the proto. [§04](04-calling-a-service.md#the-generated-cli).

## Where to go next

- **Writing the contract** — every annotation, with what it produces →
  [02 Defining a contract](02-defining-a-contract.md)
- **Serving it** — the composition root, the chain, the five bindings →
  [03 Serving a service](03-serving-a-service.md)
- **Calling it** — browser, Go, bus, CLI → [04 Calling a service](04-calling-a-service.md)
