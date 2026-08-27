# 06 — Cross-cutting concerns

Cross-cutting concerns are where a multi-transport system either pays off or quietly fails.

**Core takes no position on which ones you need.** It guarantees one thing, and the guarantee is
narrow on purpose:

> **Chain symmetry.** Whatever interceptor chain you configure runs **identically on every
> transport**, in the same order, over the same envelope.

That is the property worth having. The classic multi-transport failure — a check enforced in the
HTTP handler and silently absent on the bus — is impossible when there is one chain and one
dispatch. It does not require core to know what a permission is.

Everything below this line is a **stock interceptor**: shipped, optional, and replaceable. Use
none, some, or your own.

---

## The interceptor

An interceptor is middleware over the **typed RPC** rather than over a raw network frame. It sees
the procedure name, the metadata and the decoded message — which is exactly the envelope, which is
why one chain works everywhere.

```go
type UnaryFunc func(ctx context.Context, req *Envelope) (*Envelope, error)
type Interceptor func(next UnaryFunc) UnaryFunc
```

That signature is core's stable API, and it is core's **entire** contribution to this subject.
`Envelope` carries a procedure string, a metadata map, and bytes. It has no notion of a user, a
tenant, a permission, or a policy.

## Stock interceptors

| Interceptor | Module | Does |
| --- | --- | --- |
| `telemetry` | core | Spans and metrics labelled by procedure — the same label on every road |
| `recover` | core | Turns a handler panic into an error rather than a dropped connection |
| `deadline` | core | Enforces `deadline_unix_ms` — the one thing HTTP gives free and a bus does not |
| `validate` | `/validate` | Declarative field rules (`protovalidate`) |
| `errors` | `/errors` | Maps handler errors onto a reason enum |
| `authn` | `/auth` | Credential extraction and verification |
| `authz` | `/auth` | Permission decisions — **see below** |
| `ratelimit` | `/ratelimit` | Per-procedure or per-caller limits |
| `idempotency` | `/idempotency` | Dedupe by an idempotency key from metadata |

Core ships only the first three, because they are properties of *dispatch* rather than of any
security or business model. The rest are ordinary modules with no privileged access — which is the
test of whether the extension point is real.

## Composing

```go
// Nothing. Perfectly valid -- an internal service behind a gateway
// that already authorizes may want exactly this.
chain := interchange.Chain()

// The stock defaults.
chain := interchange.DefaultChain(cfg)

// Extend by NAME, not position. A positional chain breaks silently
// the day a stage is inserted upstream.
chain := interchange.DefaultChain(cfg).
    After("deadline", tenantResolver()).
    Before("validate", idempotency(store)).
    Replace("telemetry", myOtel())

// Or build your own from scratch.
chain := interchange.Chain(recover(), myAuth(), validate(v))
```

---

## Authorization, if you want it

Authorization is a **module**, not a core requirement. Adopters who authorize at a gateway, run
mTLS-only internal services, or build public data APIs should be able to use Interchange without
ever importing it — and can.

When you do want it, the module supplies three things.

**1 · An annotation you opt into.** The `auth` option lives in the `/auth` module's proto, not in
core. Core never parses it; the interceptor and the plugin do.

```protobuf
option (interchange.auth.v1.auth) = {
  auth_types: [AUTH_TYPE_SESSION, AUTH_TYPE_WORKLOAD]
  permission: {resource: "providers" verb: VERB_READ}
};
```

**2 · A pluggable decider.** The annotation is the *declaration*; the decision is yours.

```go
type Authorizer interface {
    Authorize(ctx context.Context, procedure string, ann Annotation,
        md map[string]string, msg proto.Message) error
}
```

Ships with RBAC. OPA, Cedar and Casbin adapters are the obvious next ones. A team with a bespoke
permission service implements this in an afternoon without touching core, the contract, or any
binding.

**3 · An opt-in strictness policy.** If you adopt the module, you probably want a missing annotation
to fail the build rather than default to open — but that is *your* policy, declared in your config:

```yaml
authz:
  provider: rbac              # or opa | cedar | custom
  on_missing_annotation: error   # error | warn | ignore   (default: error)
```

`error` is the default **within the module** because a half-annotated service is worse than an
unannotated one. It is not a property of core, and it is not imposed on anyone who does not install
the module.

### Enforce twice, if you enforce at all

Two ways to turn the annotation into enforcement, catching different failures:

| | Sees | Catches | Misses |
| --- | --- | --- | --- |
| **Runtime interceptor** | procedure, metadata, **the decoded message** | resource-aware checks ("is this caller the owner?") | a missing annotation, until it runs |
| **Generated table** | procedure, metadata | missing/unknown annotations, at build time; enforceable at an edge gateway | anything needing the message body |

Take both if you take either. The annotation feeds both — one is a lint pass, the other is the
enforcement point.

### Rules worth keeping if you build an authz layer

Not framework rules — hard-won ones, offered because they generalise:

- **absent ≠ public.** Make a missing annotation an error, and make public endpoints say so
  explicitly, so they are greppable and reviewable.
- **fail closed on nil.** An optional resolver that is unwired must **deny**. An RPC needing a
  resolver it does not have is a wiring bug, not an open door.
- **no unguarded route.** Every entry point behind the chain. Hand-written routes bolted onto the
  same mux are how an unauthenticated read ships. *Chain symmetry gives you this for the transports
  Interchange owns — it cannot help with a route you add beside it.*

---

## Errors

Also optional, also worth having. A closed, append-only enum of machine-readable reasons means a
client branches on the **reason** instead of pattern-matching English that gets reworded next
sprint.

```protobuf
enum ErrorReason {
  ERROR_REASON_UNSPECIFIED       = 0;
  ERROR_REASON_INVALID_ARGUMENT  = 1;
  ERROR_REASON_UNAUTHENTICATED   = 2;
  ERROR_REASON_PERMISSION_DENIED = 3;
  ERROR_REASON_NOT_FOUND         = 4;
  // ... appended below; renumbering is breaking ...
}
```

**One error, four surfaces:**

| Surface | Form |
| --- | --- |
| handler | returns `NotFound` + `"PROVIDER_NOT_FOUND"` |
| envelope | `Response{code: 5, reason: "PROVIDER_NOT_FOUND"}` — canonical |
| HTTP | `404` + `problem+json` carrying the reason |
| bus | `Response.code` / `Response.reason`, as-is |

The reason string is the same on every road. That is what lets a client branch once.

The envelope reserves `code`, `message` and `reason` fields, but core assigns them **no meaning** —
it moves them. Bring your own taxonomy, or use the stock one.

## Validation

Declarative field rules on the message, enforced by one interceptor: written once in the contract,
applied on every transport — instead of implemented three times in three languages with three
different error messages.

Optional, like everything else here. What core guarantees is that if you *do* install it, it cannot
run on HTTP and skip the bus.
