# ADR-0048 — Codegen does not touch the network

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

This one was not foreseen. `interchange.yaml` and the buf templates originally named the stock
generators as remote plugins — `buf.build/protocolbuffers/go:v1.36.12` and
`buf.build/connectrpc/go:v1.20.0` — which is the ergonomic default and needs no `make plugins`
step. Remote plugins are rate limited per IP, and shared CI runners exhaust that quota routinely.
While the frontends were being wired into `ix import`, the registry rate-limited this repository's
own test suite into flakiness.

That matters more here than it would elsewhere, because generation is a *gate*. Generated output is
committed and `make verify` regenerates and fails on a diff (ADR-0033). A gate that goes red
because a registry was busy is a gate people learn to ignore, and a gate people ignore is worse
than no gate at all: it still costs the build time, and it no longer catches the thing it exists to
catch.

## Decision

No generator that this repository's build depends on reaches the network. Every Go generator is a
pinned local binary installed into `./bin` by `make plugins`:
`protoc-gen-go@v1.36.12` and `protoc-gen-connect-go@v1.20.0` via `go install`, and
`protoc-gen-bus`, `protoc-gen-cli` and `protoc-gen-authz` built from the modules that own them.
The TypeScript generator comes from the example's own `node_modules/.bin/protoc-gen-es`, installed
by `npm ci` and pinned by the example's lockfile. `interchange.yaml` and every `buf.gen.*.yaml`
name paths, not registry references.

The rate-limit retry stays. `bufx.Runner.Output` backs off and retries up to four times on
`resource_exhausted` or `too many requests`, and `hack/bufgen.sh` does the same for the buf
templates — a second line for anyone who does point a config at a remote plugin, not a load-bearing
part of this build.

## Consequences

`make generate` and `make verify` produce the same output on a laptop, on a CI runner and inside
the container image, and they produce it whether or not a registry is reachable. A red drift gate
now means the committed output is stale, which is the only thing it should ever mean. CI passes
`secrets.BUF_TOKEN` to `buf-action` when one exists, but the build does not need it.

The cost is version drift by hand, in two places. The Makefile pins `@v1.36.12` and `@v1.20.0`;
the root `go.mod` requires `google.golang.org/protobuf v1.36.12` and `connectrpc.com/connect
v1.20.0`. Those pairs must move together — a generator emitting code for a protobuf runtime the
binary does not link is two descriptor runtimes in one process, and the failure shows up as a
registration panic at `init`, not as a build error. Nothing enforces the pairing; a comment in
`interchange.yaml` says it, and `make verify` catches the diff but not the reason. The TypeScript
pin is a third place, in `examples/catalog/package.json`, though it is caret-ranged and tracks
protobuf-es rather than the Go runtime.

The other cost is a prerequisite step: `make plugins` must run before `make generate`, and a
contributor who runs `buf generate` directly gets a confusing failure. `generate-framework` and
`generate-example` both depend on `plugins`, `ix generate` builds local plugins before running, and
`ix doctor` reports which local plugins are missing — but the sharp edge is real for anyone
stepping outside those commands.

## Alternatives

**Keep remote plugins and rely on the retry.** The retry is deliberately short — four attempts,
doubling from two seconds — so that a genuinely exhausted quota fails while somebody is still
watching. That is the right shape for a retry and the wrong shape for a dependency: it converts a
hard failure into a slow one, and the gate still fails when the quota is gone.

**Require a `BUF_TOKEN` in CI.** Makes the build depend on a secret, which breaks it for forks and
for a contributor's first clone. CI now passes a token when one exists and works without it.

**Vendor the plugin binaries into the repository.** Removes the install step and adds
platform-specific binaries to source control, which is a worse trade than one `go install` line
per generator.

## Evidence

- `Makefile` — the `plugins` target, and its comment: "the one command CI depends on most does not
  touch the network".
- `examples/catalog/interchange.yaml` and `buf.gen.catalog.yaml` — every `plugin:` / `local:` entry
  is a path under `bin/` or the example's `node_modules`.
- `ix/internal/bufx/bufx.go` — `rateLimitRetries = 4`, `rateLimited()`, `waitBeforeRetry()`.
- `hack/bufgen.sh` — the same retry for the buf templates.
- `go.mod` — `google.golang.org/protobuf v1.36.12`, `connectrpc.com/connect v1.20.0`, the versions
  the Makefile pins must match.
- Commit `2f73677` — "it made ix's own suite flaky while I was fixing it".

See ADR-0033 (generated output is committed and drift-gated) and ADR-0036 (plugin determinism is a
test, not a convention).
