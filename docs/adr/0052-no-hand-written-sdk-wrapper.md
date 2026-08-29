# ADR-0052 — No hand-written SDK wrapper over generated clients

**Status:** Revisit when a wrapper can be *generated* from the contract — a hand-maintained one
never becomes correct
**Date:** 2026-08-30 · **Phase:** 0

## Context

There is always pressure to put a layer on top of a generated client. Nicer method names.
A bit of caching. A mapped error type. A default header set in one place. Each addition is small,
obviously useful, and argued for on its own merits.

The sum of them is a second contract, maintained by hand, over the first. It will drift — not
because anyone is careless, but because the generated client is regenerated from the proto and the
wrapper is not. A field added to a request appears in the generated type immediately and in the
wrapper whenever someone remembers. That is the exact failure this project exists to remove,
reintroduced one convenience function at a time.

## Decision

What ships to a consumer is the generated package and nothing else. For TypeScript that is:

- the message types — the shape of every request and response;
- one service descriptor per service, which is what `@connectrpc/connect`'s `createClient`
  consumes;
- nothing else. No fetch wrappers, no per-endpoint helper functions, no re-declared enums.

The Go side is the same rule: `protoc-gen-bus` emits the `ServiceDesc`, the server interface and a
typed bus client, and `protoc-gen-cli` emits a command tree. None of them is wrapped by hand.

Ergonomics belong in the consuming repository — a React hook, a query wrapper, a repository object
in the caller's own layer. That code is free to be as opinionated as its owners like, because it is
owned by the people who feel the friction and it is not presented to anyone else as the API.

## Consequences

A contract change reaches the call site as a compile error. The example's front end imports
`CatalogService` and its types from `../../gen/ts/catalog/v1/catalog_pb.js` and nothing else, so a
renamed field is a failed `tsc --noEmit`, not a broken page — and that is asserted, not assumed:
`npm run typecheck` runs inside the Go acceptance suite, and CI installs the workspace so it cannot
skip.

The cost is real and lands on every call site. Generated method names are the proto names, and
`createClient(CatalogService, transport).listProviders({ pageSize: 50 })` is what you write, every
time, including the transport construction and the header interceptor. There is no place to put a
shared retry policy, a shared error mapping or a shared cache except in each consuming repo — which
means several consumers may each build their own, and those will differ. That is a deliberate trade:
N divergent convenience layers that everyone knows are local beat one shared layer that everyone
believes is the API.

There is also no versioning story handed to you. §05 lists three delivery models — workspace
package, published npm package (the recommended default), registry-hosted SDK — and the example
uses the first because it shares a repository. Choosing one of the other two is the adopter's work.

## Alternatives

**Ship a thin blessed wrapper with the framework.** Then the framework owns a hand-written surface
that has to track five generators and every adopter's error taxonomy, and the drift is in the
framework rather than in one team's repo — strictly worse, because it is harder to change.

**Generate the wrapper too.** Not rejected in principle, and it is the condition on this record's
status: a generated ergonomics layer is regenerated with everything else and cannot drift. It is
deferred because "nicer" is not a property a plugin can derive from a descriptor — the naming, the
caching policy and the error mapping are all judgment calls, and a generator that guesses them
produces a layer nobody wants and everybody has to keep.

**Let the generated client carry the conveniences directly.** The bus client already does this
once, with a second explicit-deadline signature beside the plain one (ADR-0037), because hiding a
network timeout behind a local-looking call is a trap rather than a convenience. That is the bar:
a convenience earns a place in generated output when its absence is a correctness problem, not
when its presence is nicer.

## Evidence

- `docs/05-one-call-two-transports.md` §"What is in the package" and the "The thing to resist"
  callout — the source of this decision's wording.
- `examples/catalog/web/src/main.ts` — one import from `../../gen/ts/...`; the file comment states
  the criterion it closes and why there is no hand-written client interface in that directory.
- `examples/catalog/acceptance_test.go` — `TestAFrontEndImportsItsTypesFromGeneratedOutput` runs
  `npm run typecheck` (`tsc --noEmit`) over `web/`.
- `.github/workflows/ci.yaml` — `npm --prefix examples/catalog ci`, so the test above cannot skip
  in CI.
- `BUILD-PLAN.md` §Deliberately out of scope — "A hand-written SDK wrapper over generated clients.
  That is a second contract, maintained by hand, and it will drift."

See ADR-0033 (generated output is committed and drift-gated), ADR-0037 (bus clients carry an
explicit deadline) and ADR-0049 (the npm channel is first-class).
