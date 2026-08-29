# Build plan

Six phases. Each is independently useful — **stopping after any one leaves the system better than
it started**, which is the property that makes this adoptable rather than a rewrite.

> **All six are built.** The exit criteria below are ticked, and each names the test that closes
> it. Two were not closed as written and say so: the engine's "no switch on transport type" needed
> one equality test for routing, and "the NATS driver is ~150 lines" held for the driver proper and
> not for the module. What no phase closed is production traffic.

Two ordering decisions carry most of the risk, and both are deliberate:

**The message engine is phase 4, not phase 1.** Phases 1–3 need no new runtime at all — they are
contract work and generated code. That means the first real win (typed clients that cannot drift)
lands before any of the hard engineering.

**Non-proto frontends are phase 6, not phase 1** — even though "bring your own format" is the
headline feature. A frontend normalises *into* the canonical contract model. Building one while
that model is still moving means aiming at a moving target, and every frontend then needs reworking
each time the model shifts. Prove the proto path end to end, freeze the IR, then open the front
door.

```
P1  core + contract + CLI    ──▶  no new runtime
P2  RPC + pluggable chain    ──▶  no new runtime
P3  REST                     ──▶  no new runtime
P4  MESSAGE ENGINE + NATS    ──▶  the real build
P5  MQTT + WebSocket         ──▶  proves the seam
P6  schema frontends         ──▶  opens the front door
```

> **If your adopters cannot write proto at all**, phase 6 can move to phase 2 — but then the IR must
> be frozen early and you accept rework on every frontend as the model settles. Make that trade
> knowingly; do not drift into it.

---

## Phase 1 — Core, contract, and the CLI skeleton

**Goal.** Make the contract the only place the API exists, make drift a build failure, and make `ix`
the only thing anyone installs.

**Build**

- **Core module** — the IR (`FileDescriptorSet`), and the five extension-point *interfaces*:
  `Frontend`, `Driver`, `Codec`, `Interceptor`. Interfaces only; no implementations beyond proto
  and the three dispatch-level stock interceptors.
- **The dependency rule, enforced from day one** — a CI check asserting the core module's graph
  contains no broker client, no HTTP router, no policy engine, and no auth module. Retrofitting this is painful; it
  costs nothing now.
- `api/` layout, `buf.yaml`, `buf.lock`, and core's own protos (`transport/v1/envelope.proto`,
  `common/v1/types.proto`).
- The proto frontend — the identity case, and the reference implementation every other frontend is
  measured against.
- **`ix`**: `init`, `generate`, `fmt`, `lint`, `breaking`, `verify`.
- The **drift gate**: generated output is committed; `ix verify` regenerates and fails on a diff.

**Generated, not written.** Go message types and server interfaces; TypeScript types and browser
clients.

**Exit criteria**

- [x] `ix init` produces a working project with a generated typed client. — `ix/internal/cmd` `TestInit*`
- [x] `ix verify` fails when generated output is stale, and runs in CI. — verified by mutation; `make verify`
- [x] A front end imports its types from generated output, not a hand-written file. — `TestAFrontEndImportsItsTypesFromGeneratedOutput` (`tsc --noEmit`)
- [x] The core module's dependency graph is asserted clean. — `hack/depcheck.sh`, an allowlist
- [x] `ix` is installable via npm as well as Go — the launcher package resolves and execs a platform
      binary, verified with `npm pack` and a local run. **Nothing is published**: the registry
      artifacts exist, the uploads are switched off while the name is a working name.

**Failure class closed.** Hand-written client types drifting from the server — with no new transport
and no new runtime.

**Decide here.** `WIRE_JSON` vs `FILE` for breaking checks. Changing it later re-baselines every
check.

---

## Phase 2 — RPC binding and the pluggable chain

**Goal.** Serve generated handlers over HTTP, and establish **chain symmetry** — the guarantee that
whatever chain you configure runs identically on every transport. Authorization is built here too,
but as the *worked example of a module*, not as a core requirement.

**Build**

- The chain with **named anchors** — `Chain()`, `DefaultChain()`, plus `After` / `Before` /
  `Replace`. Named, not positional, so inserting a stage upstream cannot silently reorder someone
  else's chain.
- **Core's three stock interceptors only**: `telemetry`, `recover`, `deadline`. These are properties
  of dispatch, not of any security or business model. Nothing else goes in core.
- The **annotation band registry** — reserve `50000–59999` and record every assignment in one table
  *before* the second annotation exists. Two annotations at the same number is a silent,
  undebuggable collision.
- `connect-go` handlers wired to the chain.
- `ix describe` — the reviewability property as a command.
- **The `/auth` module, built as an outside consumer.** Its own annotation proto, an `Authorizer`
  interface, an RBAC implementation, `protoc-gen-authz`, and a configurable
  `on_missing_annotation` policy. It imports core; core does not import it.

**Exit criteria**

- [x] A chain configured once demonstrably runs in the same order on every registered binding. — `TestChainConfiguredOnceRunsInTheSameOrderOnEveryRegisteredBinding`, four roads
- [x] **Core builds and passes its tests with the `/auth` module absent.** A service with an empty
      chain works.
- [x] The dependency check shows core importing no policy engine and no auth module.
- [x] `ix describe` shows the transports for any RPC, and any module-supplied annotations it finds. — golden-tested
- [x] *(module)* CI can be configured to fail on an RPC with no `(auth)` annotation.
- [x] *(module)* A third party can replace the `Authorizer` **without touching core, the contract, or
      any binding** — the real test of whether the extension point works.

**Failure class closed.** A cross-cutting check enforced on one road and forgotten on another — for
*any* check, not just authorization.

> **Why authz is built here despite being optional.** It is the most demanding consumer of the
> extension model — it needs an annotation, a build-time plugin, a runtime interceptor, and
> reflection into the message. If the plugin API can carry authz cleanly, it can carry anything.
> Building it early is how you find out the seams are wrong while they are still cheap to move.

---

## Phase 3 — REST binding

**Goal.** A partner-facing REST surface with no second contract.

**Build**

- `google.api.http` annotations on the RPCs that need it; in-process transcoding.
- Per-surface JSON casing, written down: camelCase on RPC, snake_case on REST.
- OpenAPI emission from the same descriptors.

**Exit criteria**

- [x] An existing REST consumer is served by the transcoder. — `TestAnExistingRESTConsumerIsServedByTheTranscoder`
- [ ] Old hand-written handlers are **deleted** as each path is covered — not left running beside.
      *Not applicable: nothing here was migrated from hand-written handlers. It stays unticked
      rather than ticked falsely — this criterion is for an adopter, and it is the one that
      actually costs something.*
- [x] The emitted OpenAPI matches what partners already call — `TestTheEmittedOpenAPIMatchesWhatPartnersCall`, golden-gated

---

## Phase 4 — Message engine + NATS · **the real build**

**Goal.** Prove a call over a message bus lands on the same handler through the same chain.

**Build**

- `transport/v1/envelope.proto` — `Request`, `Response`, `Frame`.
- **`protoc-gen-bus`** — subscribers and typed clients from the `(transports)` annotation.
- **The message engine**, once, for every broker:
  - correlation — pending-call map keyed by `correlation_id`, timeout eviction
  - deadlines — `deadline_unix_ms` → server context; cancel pending calls when the client's dies
  - metadata fallback — fold into the envelope when `!NativeHeaders`, lift back on receipt
  - chunking — split over `MaxPayload` into sequenced `Frame`s, reassemble
  - replay suppression — drop already-seen `sequence` when `AtLeastOnce`
  - streaming — frame ordering, `KIND_END`, back-pressure
  - dispatch — procedure → handler, through the chain
- **The NATS driver** against `Driver`, in its own module.

**Exit criteria**

- [x] One low-risk service-to-service call, already on HTTP, is moved to the bus. — `TestTheInterceptorChainCameAlongUnchanged`
- [x] **The interceptor chain came along unchanged.** *This* is what the phase tests — not
      throughput. — `TestTheInterceptorChainCameAlongUnchanged`
- [x] The same procedure string appears in the authz check, the metrics labels and the trace span on
      both roads.
- [x] Authorization demonstrably fires on the bus call. — `TestAuthorizationFiresOnTheBusCall`, against a real broker
- [x] The engine contains **no switch on transport type** — all variation comes from `Caps()`.
      *One qualification, stated rather than hidden: the engine compares `Caps().Transport` for
      equality to decide which procedures to subscribe. That is routing, not behaviour — no code
      path branches on which transport it is — but it is not literally zero comparisons.*
- [x] The NATS driver imports no concrete message type. — review, and `drivertest` cannot pass without it

**What this phase actually found.** The envelope survived contact with four transports without a
shape change. The seam did not: acknowledging on delivery rather than on completion, and a
correlation id no driver could reach, were both engine bugs that only appeared once a real broker
was wired up. The ~150-line rule did its job — it held for the driver proper (NATS 172 lines of
code, WebSocket 84) and the overage in every module was connection handshakes and config parsing,
which is not a leaked engine responsibility.

**Decide here.** Core NATS for request/reply; JetStream for anything that must survive a restart.
One durability tier for everything is wrong in both directions.

---

## Phase 5 — MQTT 5 and WebSocket

**Goal.** Demonstrate that adding a road is writing one adapter, not adding an API surface. This
phase exists to **falsify the seam** — if either driver needs an engine change, phase 4 was wrong.

**Build**

- MQTT 5 driver. **Version 5 only** — 3.1.1 means reinventing correlation and metadata badly.
- WebSocket driver + a connection-lifecycle shim: accepting the socket, the handshake frame carrying
  credentials (a browser cannot set `Authorization` on an upgrade), demultiplexing concurrent calls
  over one pipe.

**Exit criteria**

- [x] **Neither driver required a change to the engine** — *this one failed, and the failure was
      the point.* Between them MQTT and WebSocket found four engine bugs: inbound metadata dropped
      on exactly the transports that needed it, replies acknowledged only on the chunked path, a
      subscription plan that ran a handler N times on a single-channel transport, and a conformance
      suite only one transport could run. All four were fixed in the engine, where every driver
      inherits them, and each driver then deleted its workaround — 100 lines out of the WebSocket
      one. A fifth driver should expect to find a seventh.
- [x] Neither driver imports a concrete message type. — same
- [x] No new authorization path, no new validation path, no second definition of any method.

**Note.** WebSocket is the **degenerate case, not the hard one** — one channel, so `Address()`
returns a constant and the procedure lives entirely in the envelope. What it adds is connection
lifecycle: a shim around the driver, not extra protocol.

---

## Phase 6 — Schema frontends

**Goal.** Let people adopt without writing protobuf.

**Build**

- The `Frontend` interface is already in core from phase 1; this phase implements against it.
- **Interchange DSL** (YAML) first — smallest surface, no external spec to track, best onboarding
  story, and it exercises the interface without an evolving standard underneath.
- Then one real-world importer. **TypeSpec** is the cleanest fit; **OpenAPI** has the most existing
  contracts to import. Pick by who your first ten users are.
- The **sidecar annotation file** — the universal fallback so no frontend is blocked on inventing
  extension syntax.
- `ix import`, with format auto-detection.

**Exit criteria**

- [x] A user with no proto knowledge reaches a generated typed client via `ix init` + `ix generate`.
- [x] Every frontend is **total or loud** — a construct it cannot represent produces an error with
      the exact source location, never a partial contract.
- [x] Emitted proto is committed and under the drift gate. — `SourceEmitter`; `ix import` refuses a frontend without one
- [x] Adding a frontend requires **no change to core**. — OpenAPI added none; it found gaps in the seam, which were additive

**The rule that makes this safe.** A frontend that silently drops what it cannot represent produces
a contract that *lies* — worse than the three honest contracts this project exists to replace.
Prefer refusing to emit.

**Not a goal: round-tripping.** Making `proto → OpenAPI → proto` an identity function is a tar pit
that will consume the project. The emitted proto is the artifact; the source format is an input.

---

## Cross-phase gates

Hold from phase 1 and never relax.

| Gate | Enforced by |
| --- | --- |
| The contract is edited first, always | team convention + the drift gate |
| Generated output is committed and current | `ix verify` in CI |
| The configured chain runs identically on every transport | chain symmetry — core's one invariant |
| Every entry point sits behind the chain | review; there is no third category |
| A binding adapter imports no concrete message type | review — the rule that keeps adapters adapters |
| Core depends on no broker, router, policy engine, or auth module | dependency-graph check in CI |
| Plugins sort before emitting | otherwise the drift gate flaps and nobody trusts it |
| A frontend is total or loud | never emit a partial contract |

## Fixed on purpose

Two things are **not** pluggable, because making them so would dissolve the guarantee the project
exists to provide:

- **The envelope shape.** Bindings populate it; nobody redefines it. It is the convergence point — a
  per-deployment envelope means no shared dispatch and no shared chain.
- **Chain symmetry.** A driver may not add, skip or reorder a stage. This is the guarantee the
  project exists to provide, and the only behavioural invariant core imposes.

Authorization is **not** on that list. Core takes no position on it — see
[§06](docs/06-crosscutting.md).

## Deliberately out of scope

- **Streaming** until a use case forces it — it constrains every later transport choice.
- **A hand-written SDK wrapper** over generated clients. That is a second contract, maintained by
  hand, and it will drift. Ergonomics belong in a hook or query wrapper in the consuming repo.
- **The browser touching the bus.** It gets RPC, plus WebSocket for bidi. When a browser action must
  reach something bus-only, an edge service accepts the RPC and re-emits it — a few lines, because
  it is the same envelope on both sides.
- **Hosting a registry.** `ix` can talk to one; it is not one.
