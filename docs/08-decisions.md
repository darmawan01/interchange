# 08 — Decisions and open questions

## Decisions to make once, deliberately

| Decision | Options | Recommendation |
| --- | --- | --- |
| Missing `auth` annotation *(within the auth module)* | defaults to public / build failure | Build failure — but a **module policy**, not a framework rule |
| Authz in core or a module | core requirement / optional module | **Optional module.** Core owns chain symmetry, not authorization |
| Authz enforcement *(if used)* | runtime / generated table / both | **Both**: generate for the gate, intercept for the check |
| REST surface | in-process transcoder / gateway routes | Transcoder until a gateway team owns routing |
| JSON casing | camelCase / snake_case | Per surface, documented: camelCase on RPC, snake_case on REST |
| Breaking rule | `FILE` / `WIRE_JSON` | `WIRE_JSON` once any JSON surface is public |
| Bus durability | core pub/sub / JetStream | Core for request/reply, JetStream for anything that must survive a restart |
| MQTT version | 3.1.1 + 5 / 5 only | **5 only** — 3.1.1 means reinventing correlation and metadata badly |
| Streaming | none / server / bidi | None until a use case forces it; it constrains every later transport choice |
| Envelope payload | `bytes` / `google.protobuf.Any` | `bytes` — the procedure already names the type |

### Added by the framework framing

| Decision | Options | Recommendation |
| --- | --- | --- |
| The IR | `FileDescriptorSet` / bespoke AST | **`FileDescriptorSet`** — inheriting the protoc ecosystem is the whole leverage ([§09](09-schema-frontends.md)) |
| Non-proto frontends | ship early / after the proto path is proven | **After.** A frontend normalises *into* the contract model; building it while that model moves means aiming at a moving target |
| Emitted proto from a frontend | build artifact / committed | **Committed**, under the same drift gate — otherwise the IR is invisible and unreviewable |
| Adapter distribution | one module / module per adapter | **Module per adapter** — the core must not depend on a broker client |
| Interceptor ordering | positional / named anchors | **Named** — positional chains break silently when a stage is inserted upstream |
| CLI name | `interchange` / `ix` | `ix`, with `interchange` as an alias |

## Resolved by building it

**Per-RPC transport opt-in, or per-service? — Both, and the per-RPC one replaces rather than
merges.** A service-level `(service_transports)` sets the default; a per-RPC `(transports)`
replaces it entirely, including the queue group. Merging would mean a reviewer has to compose two
annotations in their head to answer "what does this expose", and the whole point of the annotation
is that the answer is visible in the diff.

**Do generated bus clients block? — Both signatures are generated.** `ListProviders(ctx, req)` and
`ListProvidersWithin(ctx, timeout, req)`. The plain one is the mental model people want; the
explicit-deadline one exists because a synchronous call that hides a network timeout behind a
local-looking signature is exactly the trap this section worried about.

**Bus durability — two drivers, not one with a flag.** Core NATS and JetStream are separate
constructors with different `Capabilities`. The durable tier cannot preserve the publisher's reply
subject, so it declares `NativeReply: false` and the return address rides in the envelope. Nothing
above the driver noticed, which is the strongest evidence the capability model works.

**How far does the CLI generator go? — It reports its own coverage.** `protoc-gen-cli` emits a
`Coverage()` report and takes `require_annotation=true` to fail the build on an unannotated RPC.
The worry was that 80% coverage is worse than none; the answer is to make the percentage visible
rather than to guess it.

## Open questions

**Where does the bus binding get identity?** Still open. The broker's own authenticated connection
identity is stronger than a credential in metadata but couples authorization to broker
configuration. Only the metadata path is built.

**Which non-proto frontend first?** TypeSpec is the cleanest fit; OpenAPI has the most existing
contracts to import; the Interchange DSL is the best onboarding story. They serve different
adopters and the answer depends on who the first ten users are.

**Should `ix lint` warn when no authz module is configured?** Core takes no position on
authorization — but a service exposed on a public bus with an empty chain is probably a mistake
rather than a choice. A warning is not a mandate; it may also be exactly the kind of opinion a
framework should not have.

**How much does the sidecar undermine the single-source claim?** A frontend that cannot express
annotations natively needs one — but a contract split across two files is a contract that can drift
between them. Possibly the sidecar should be a migration aid with a deprecation path, not a
permanent fixture.

## What this is worth

**Failure classes removed**

- Hand-written client types drifting from the server
- Field renames caught in production instead of at build
- Validation implemented three times in three languages
- Authorization enforced on one road and forgotten on another
- Async consumers parsing JSON with no contract to generate from
- Adding a transport meaning adding an API surface

**Costs accepted**

- Three plugins to write and maintain
- Generated code in the repo, and a CI gate that guards it
- A team-wide convention: the proto is edited first, always
- Bus payload ceilings and at-least-once delivery become your problem
- Streaming mappings are real work per transport
- An annotation band and registry to govern
- *(framework)* Every extension point is public API — breaking it breaks other people's adapters
- *(framework)* Making authz optional means an adopter can ship a multi-transport service with no
  authorization at all. Chain symmetry guarantees consistency, not safety
- *(framework)* Each frontend is an ongoing maintenance commitment against an evolving spec

## Status

Working name, open for a better one.

| Part | Standing |
| --- | --- |
| Contract layer, RPC + REST bindings, annotation-driven authz, codegen pipeline | Follow patterns **proven in production systems** |
| NATS binding | Follows a pattern **in production**, adapted here to the envelope |
| **The envelope** | **Proposed design** |
| **MQTT and WebSocket bindings** | **Proposed design** |
| **Schema frontends, adapter registries, the CLI** | **Proposed design** — no implementation yet |

The envelope and the MQTT/WebSocket bindings should be prototyped against one real service before
phase 4 is committed to.
