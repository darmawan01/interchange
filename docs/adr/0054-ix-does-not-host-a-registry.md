# ADR-0054 — `ix` talks to a registry; it is not one

**Status:** Revisit when an adopter needs a schema registry no existing one can be — which is a
different product, not a subcommand
**Date:** 2026-08-30 · **Phase:** 0

## Context

A tool that already builds descriptor sets, lints them, detects breaking changes and generates
clients is one step away from storing versioned contracts and serving them. The step is short and
the pull is strong: a registry is where "which version of the contract is production on?" gets an
answer, and every adopter eventually asks.

It is also a hosted service with storage, authentication, availability and a migration story — a
product, not a subcommand. And the Buf Schema Registry already exists, speaks the format `ix`
already uses, and is what `buf.yaml`'s `deps` and `buf.lock` already resolve against.

The same pull exists in two adjacent directions: running the services (`ix` already has a working
engine and a memory driver, so a process manager is *right there*), and hiding generated output
(the tool that generates it could just as easily keep it out of the way).

## Decision

`ix` talks to a registry and does not host one. `ix breaking --against` takes a git ref, a
directory, or a registry reference — buf resolves whichever it is, because `ix` shells out to buf
(ADR-0046) and inherits its input syntax. Dependencies come from `buf.yaml`'s `deps` and are pinned
by `buf.lock`, which `ix doctor` checks for staleness against the `buf.yaml` that declares them.

Two more things `ix` does not do, for the same reason:

**It does not run your services.** `ix dev` is a loopback for exercising a *contract*. There are no
compiled handlers in it, so every RPC is answered by a stub that builds a default-valued response
from the descriptor by reflection. What that proves is that the contract dispatches: the procedure
resolves, the request decodes against the declared shape, the envelope makes a real round trip
through the real engine and the real interceptor chain, and the response is the shape the
descriptor says it is. It proves nothing about a handler, because there isn't one. It runs over
`driver/memory`, which is a real driver rather than a mock (ADR-0029) — the production machinery
with the broker removed, not a simulation of it.

**It does not hide the generated output.** Generated code is committed and reviewable. A CLI that
makes generation invisible makes drift invisible with it.

## Consequences

`ix` stays a build-time tool with no state, no service to operate and no availability story. It
runs in CI, in a container, from `npx`, and offline. Whichever registry a team already uses keeps
working, because the integration point is buf's input syntax rather than an `ix`-specific protocol.

The cost: `ix` has no answer to "what contract is production running?" That question is real, and
an adopter without a registry answers it with git tags and discipline. `ix breaking --against` is
the closest thing on offer, and it compares against a ref you name — it will not tell you which ref
that should be.

`ix dev`'s honesty has a cost too. A stub that echoes descriptor-derived responses is useless for
testing behaviour, and a user who wanted "run my service locally" gets something that deliberately
is not that. The help text spends a paragraph saying so, because the failure mode of a convincing
fake is worse than the failure mode of an obvious one: someone would trust it.

Not hiding generated output means large diffs in review — the standing cost of ADR-0033, paid again
here. A reviewer scrolls past `catalog.pb.go`. The alternative is a repository where the only copy
of the API surface lives in a tool's cache.

## Alternatives

**Ship a minimal registry — push, pull, list.** Minimal registries do not stay minimal: the next
requests are authentication, retention, per-environment tags and a promotion flow, and each is
correct. It would also duplicate the BSR badly rather than integrating with it well.

**`ix serve` running the user's compiled service**, or `ix dev` loading handlers over a plugin
protocol. The first duplicates whatever process manager the team has and adds a second definition of
how a service starts; the second turns a contract check into a runtime, with the version-skew
problems of any plugin ABI, to replace what an adopter already does in twenty lines with the real
engine and the real driver.

**Generate into a cache directory and resolve it at build time.** Removes the review diff and the
drift gate together. `ix verify` only means something because there is committed output to compare
against.

## Evidence

- `docs/11-cli.md` §"What the CLI does not do" — the three statements this record expands.
- `ix/internal/cmd/fmt.go:38`–`59` — `ix breaking`'s `--against` flag: "a git ref
  (\".git#branch=main\"), a directory, or a registry reference"; the run is
  `p.Buf.Run("breaking", "--against", against)`.
- `ix/internal/cmd/doctor.go` — the `buf.lock` staleness check, walking up for `buf.yaml` the way
  buf itself does.
- `ix/internal/cmd/dev.go` (`devHelp`) and `ix/internal/devsrv/devsrv.go` — the stub, the
  reflection-built response, and `driver/memory` behind it.
- `ix/internal/cmd/root.go` — the command tree: `init`, `import`, `generate`, `fmt`, `lint`,
  `breaking`, `verify`, `describe`, `plugin`, `dev`, `doctor`. No `push`, no `serve`.
- `Makefile` (`verify`) and `examples/catalog/gen/` — generated output committed, and the gate that
  keeps it current.

See ADR-0029 (the in-process driver is a real driver, not a mock) and ADR-0033 (generated output is
committed and drift-gated).
