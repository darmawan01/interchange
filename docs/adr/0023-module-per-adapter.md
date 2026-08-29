# ADR-0023 — One module per adapter; core depends on no broker

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 4

## Context

"Core depends on interfaces; adapters depend on core; nothing depends on a concrete adapter" is
easy to write in a design document and hard to keep. In a single Go module it survives exactly
until someone adds a driver-specific helper to a core package for a good local reason, and by then
the `go.mod` already lists every broker client, so the import that leaks costs nothing and nobody
notices.

The consequence lands on the adopter. A team using MQTT for a device fleet should not compile a
NATS client, a WebSocket library and a policy engine into their binary because the framework
happens to ship them.

## Decision

The dependency rule is the **repository layout**, not a convention. Each adapter is its own Go
module with its own `go.mod`: `driver/nats`, `driver/mqtt`, `driver/ws`, plus `auth`, `errors`,
`validate`, `binding/rest`, `tools`, `ix` and the frontends. Thirteen modules, joined by a `go.work`
whose comment states the rule: *core cannot import a broker client because a broker client lives in
another module.*

Core cannot import NATS because it would have to add it to its own `go.mod`, and that addition is
what `hack/depcheck.sh` fails on. The check runs `go list -deps ./...` over core and compares every
external package against an **allowlist** of two entries: `google.golang.org/protobuf` (the IR and
the codecs) and `connectrpc.com/connect` (the request/response binding's protocol). Anything else
is a failure, with the reason printed.

The allowlist is the load-bearing part of the design. **A denylist only catches the seams that have
already leaked** — you write it after an incident, it names the brokers you thought of, and the
first dependency nobody predicted (a logging framework, a YAML parser, a metrics library) passes
silently. An allowlist inverts the default: every new external import is a failure until somebody
adds it *with the reason it belongs there*, which is a diff a reviewer reads. The script keeps a
denylist too (`nats-io|eclipse/paho|gorilla|coder/websocket|open-policy-agent|casbin|/auth$`), but
only as the second line — "the same rule stated as the thing a reviewer actually greps for".

## Consequences

Importing `driver/nats` does not drag paho into your binary, and `driver/memory` (which lives in
core, ADR-0029) pulls nothing at all. Each adapter versions on its own cadence, so a paho release
does not force a core release.

The costs are the ones every multi-module repo pays. Thirteen `go.mod` files and a `go.work` to
keep in step; a change spanning core and a driver is two module versions, not one commit's worth of
`go get`; `make generate` has to walk five proto modules; and CI has to run the dependency check
before anything else, because a leak found after the test suite is a leak that already shaped the
tests. The `go.work` also makes local development easy enough that a broken cross-module version
constraint can go unnoticed until a consumer outside the workspace tries it.

## Alternatives

**One module with build tags.** Tags do not stop a `go.mod` from listing every client, and a tag
that is off in CI is a code path nobody compiles.

**A denylist of known-bad imports.** Discussed above and rejected in the script's own comment: it
catches only what has already leaked.

**Adapters in separate repositories.** Buys the same isolation and loses the ability to run every
driver against `drivertest` in one CI job, which is what keeps the conformance suite honest
(ADR-0030).

## Evidence

- `go.work` — the module list, with the rule in its comment.
- `hack/depcheck.sh` — the allowlist, the failure message, and the reviewer-facing denylist behind
  it. `make depcheck` runs it; CI runs it before anything else.
- `CONTRIBUTING.md`, "The eight gates" — "Core depends on no broker, router, policy engine, or auth
  module | `hack/depcheck.sh` in CI", and "If you find yourself adding an import to core's
  `go.mod`, stop."
- `driver/nats/go.mod`, `driver/mqtt/go.mod`, `driver/ws/go.mod` — the isolation, one file each.
- `docs/08-decisions.md`, "Adapter distribution | one module / module per adapter | **Module per
  adapter** — the core must not depend on a broker client".
