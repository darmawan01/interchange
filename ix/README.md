# `ix`

**One binary. One contract, every road.**

`ix` is the only thing a user installs. It wraps the whole pipeline — scaffold, import, generate,
format, lint, breaking-change detection, the drift gate, and inspection — around a proto tree that
declares a service once and fans it out onto HTTP/Connect, REST, NATS, MQTT 5 and WebSocket.

**Design goal: `ix init` to a generated, typed client in under a minute, with no protobuf knowledge
required.** Every other command exists to keep that true as the project grows.

```
$ ix init --name catalog
$ ix describe CatalogService.ListCatalogs
$ ix generate
```

## How it works

`ix` **shells out to `buf`** for build, lint, breaking and generate. buf's CLI is its supported
interface, the version a project pins is the version that must run, and anything `ix` did is
reproducible by hand from the command `ix --verbose` printed.

Everything that reads a contract reads one `FileDescriptorSet`, obtained with `buf build -o -`. The
annotations are then resolved **by extension number and field name off the descriptor** — not by
importing a generated Go type. That is why `ix describe` can print an `(auth)` annotation without
`ix` depending on the optional `/auth` module, and it is the same dual availability a running
server relies on: one descriptor, read at build time here and by reflection there.

## Commands

| Command | Does |
| --- | --- |
| `ix init` | Scaffold a project — `interchange.yaml`, `buf.yaml`, `buf.gen.yaml`, `api/` with a starter service, a Makefile and a CI workflow |
| `ix import <file>` | Detect a non-proto source's format and name the frontend that reads it |
| `ix generate` | Build local plugins, then run every configured generator through buf |
| `ix fmt` | `buf format -w` (`--check` reports instead of rewriting) |
| `ix lint` | `buf lint`, **plus** the naming rules and the annotation-band check |
| `ix breaking --against <ref>` | `buf breaking` with the project's rule (`WIRE_JSON`) |
| `ix verify` | **The drift gate.** Regenerate into a temp tree and fail if the committed output moved. This is what CI runs |
| `ix describe <rpc>` | What an RPC resolves to on **every** transport, plus authorization and CLI |
| `ix plugin` | `list` · `add` · `pin` · `remove` · `sync` generators in `interchange.yaml` |
| `ix dev` | Local loopback — exercise a contract with no infrastructure |
| `ix doctor` | Diagnose: buf and Go versions, config, plugin binaries, lockfile and generated-output staleness |

Global flags: `-C, --dir` (run as if started elsewhere), `--buf` (path to the buf binary),
`-v, --verbose` (echo every buf invocation).

### `ix describe` — the reviewability property, as a command

Real output, from `examples/catalog`:

```
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

Three ways to name the RPC, whichever you have in front of you:

```
ix describe CatalogService.ListProviders
ix describe catalog.v1.CatalogService.ListProviders
ix describe /catalog.v1.CatalogService/ListProviders
```

The `AUTHORIZATION` block reads whatever `(auth)` annotation the descriptor carries. Core takes no
position on authorization, so when the annotation is absent the section says `not declared` rather
than treating it as a finding.

### `ix lint` — two passes

`buf lint` applies the STANDARD proto rules. `ix` then applies the rules buf cannot know about,
because they are load-bearing rather than stylistic: the URL, the bus subject, the CLI command and
the SDK method are all *derived* from the names.

| Rule | Severity | Why |
| --- | --- | --- |
| `SERVICE_SUFFIX` | error | The CLI command and bus subject derive from the service name |
| `FIELD_SNAKE_CASE` | error | The JSON name, URL template and CLI flag derive from the field name |
| `ID_IS_STRING` | error | Ids are ULID/KSUID strings, never integers |
| `ENUM_ZERO_UNSPECIFIED` | error | proto3 reads an unset field as zero; without `_UNSPECIFIED` there is no "unset" |
| `ENUM_APPEND_ONLY` | error / warn | `allow_alias`, or an unreserved gap the next append would reuse |
| `BAND_UNREGISTERED` | error | An extension in 50000–59999 with no row in `docs/annotation-band.md` |
| `BAND_MISMATCH` | error | A band number claimed by a different annotation than the table says |
| `BAND_COLLISION` | error | Two annotations at one number on one extendee — one is silently dropped |
| `INTERNAL_EXPOSED` | error | `(internal)` and a public road contradict each other |
| `REST_NO_HTTP_RULE` | warn | A REST road with no `(google.api.http)` rule has no derivable address |
| `GROUP_WITHOUT_BUS` | warn | A competing-consumer `group` on a method that travels no broker is a setting nothing reads |
| `TIMESTAMP_SUFFIX`, `DURATION_UNIT` | warn | A `Timestamp` should end `_at`; a scalar duration should carry its unit |
| `AUTH_MISSING` | configured | Only when `auth.on_missing_annotation` is set — that is the module's policy, not a framework rule |

The band table is read from `docs/annotation-band.md` under the project root when present, and from
`ix`'s embedded copy otherwise. `--band <file>` overrides both.

### `ix verify` — the gate

```
$ ix verify

  ✓ frontends       1 sources → 3 descriptors
  ✓ annotations     2 RPCs, 2 annotated, 0 public (reviewed)
  ✓ generators      2 targets
  ✗ drift           gen/go/todo/v1/todo.pb.go differs

  generated output is stale — run `ix generate`
```

It regenerates into a temporary tree and compares byte for byte, so it never touches the working
copy and is safe on a dirty checkout. It also checks that the committed `buf.gen.yaml` still matches
`interchange.yaml` — that file exists so `buf generate` works for someone without `ix`, and two
files that disagree about what CI generates is the same failure the gate exists to stop.
`ix plugin sync` rewrites it.

### `ix dev` — the loopback

`ix dev` exercises the **contract**, not your business logic. There are no compiled handlers, so
every RPC is answered by a stub returning a default-valued response built from the descriptor by
reflection. What that proves is that the contract dispatches: the procedure resolves, the request
decodes against the declared shape, the envelope makes a real round trip through the real engine and
the real interceptor chain, and the response is the shape the descriptor says it is.

It runs over `driver/memory`, which is a real driver rather than a mock — the same six methods and
the same `Capabilities` every broker driver declares.

```
$ ix dev &                                              # starts a loopback on .interchange/dev.sock
$ ix dev call CatalogService.ListProviders '{"pageSize":10}'
{
  "providers": [],
  "nextPageToken": ""
}
```

`ix dev call` also works alone: with no server listening it starts a loopback, makes the call and
exits.

## `interchange.yaml`

One file at the repo root. Every command reads it; no command needs a flag for anything the file
already says. The schema is [`docs/10-extensibility.md`](../docs/10-extensibility.md).

```yaml
version: 1

sources:
  - path: api/**/*.proto            # a glob; ix hands buf the directory above the wildcard
    frontend: proto                 # proto is the only frontend wired into this build
    # sidecar: legacy/annotations.yaml

transports:
  default: [rpc, rest]              # the roads an RPC with no (transports) annotation takes
  drivers: [nats]                   # which broker adapters are in play

# buf's managed mode, passed through. Modelled field for field so a typo is
# rejected here with a line number rather than by buf three steps later.
managed:
  enabled: true
  disable:
    - {file_option: go_package, module: buf.build/googleapis/googleapis}
  override:
    - {file_option: go_package_prefix, value: github.com/you/project/gen/go}

generate:
  - {plugin: buf.build/protocolbuffers/go:v1.36.12, out: gen/go, opt: [paths=source_relative]}
  - {plugin: buf.build/connectrpc/go:v1.18.1,       out: gen/go, opt: [paths=source_relative]}
  - {plugin: ./bin/protoc-gen-bus,                  out: gen/go/bus, strategy: all}

# Optional module. Omit this block entirely and nothing in the stack learns
# what a permission is.
auth:
  provider: rbac                    # or opa | cedar | custom
  on_missing_annotation: error      # error | warn | ignore
```

| Key | Meaning |
| --- | --- |
| `version` | Must be `1` |
| `sources[].path` | Glob; the directory above the first wildcard is what buf is given |
| `sources[].frontend` | `proto`. Any other value is an error rather than a silent skip |
| `transports.default` | `rpc` · `rest` · `bus` · `mqtt` · `ws`. A per-RPC `(transports)` annotation **replaces** this rather than merging |
| `transports.drivers` | The broker adapters in play |
| `managed` | buf managed mode, passed through to the synthesized template |
| `generate[].plugin` | A remote reference (`buf.build/org/name[:version]`) or a local path (`./bin/protoc-gen-x`) |
| `generate[].out` | Output directory. `ix verify` diffs exactly these |
| `generate[].opt` | Plugin options |
| `generate[].strategy` | `directory` (default) or `all`. Anything cross-cutting needs `all` |
| `auth` | An optional module's block. `ix` reports it; `ix` enforces nothing of its own |

Unknown keys are rejected: a typo'd key that is silently ignored is a setting the user believes is
in effect.

### Local plugins

A generator whose `plugin:` is a path is local. If `./cmd/<basename>` exists in the project, `ix
generate` builds it first with `go build -o <plugin> ./cmd/<basename>` — a generator that is not
rebuilt before it runs is a generator that emits yesterday's output. Otherwise the binary must
already exist, and `ix doctor` says so when it does not.

## Installation

| Channel | Form |
| --- | --- |
| Go | `go install github.com/darmawan01/interchange/ix/cmd/ix@latest` |
| Homebrew | `brew install <tap>/ix` |
| npm | `npx @interchange/cli` — so a front-end team never installs a Go toolchain |
| Container | `docker run ghcr.io/<org>/ix generate` — for CI without a local install |
| Binaries | Static per-platform builds on each release |

The npm channel matters more than it looks: the front end is the consumer with the most to gain from
typed clients and the least tolerance for a Go toolchain. **The npm and container packaging is not
built here** — it wraps the binaries this module produces, and belongs to the packaging work.

`interchange` is an alias for `ix` ([docs/08](../docs/08-decisions.md)). Both are real binaries:

```
go install github.com/darmawan01/interchange/ix/cmd/interchange@latest
```

`ix` requires [`buf`](https://buf.build/docs/installation) on `PATH`, and the Go toolchain if the
project has local plugins. `ix doctor` checks both.

## What `ix` does not do

- **It does not run your services.** `ix dev` is a loopback for exercising a contract, not a service
  mesh or a process manager.
- **It does not host a registry.** It can talk to one; it is not one.
- **It does not hide the generated output.** Generated code is committed and reviewable. A CLI that
  makes generation invisible makes drift invisible with it.
