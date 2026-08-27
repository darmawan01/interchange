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

## Open questions

**Per-RPC transport opt-in, or per-service?** The `transports` annotation is drawn per-RPC, which is
the most precise but also the most annotation to maintain. A service-level default with per-RPC
overrides is probably the right ergonomics; it needs a real service to decide.

**Where does the bus binding get identity?** Over HTTP the credential is a header. On a bus there is
a second option — the broker's own authenticated connection identity — which is stronger but couples
authorization to broker configuration. Worth prototyping both.

**Do generated bus clients block?** A synchronous `ListProviders` over request/reply is the easy
mental model but hides a network timeout behind a function call that looks local. Consider making
the bus client's signature take an explicit deadline.

**How far does the CLI generator go?** It is nearly free once the annotations exist, but a generated
CLI that covers only 80% of RPCs may be worse than none.

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
