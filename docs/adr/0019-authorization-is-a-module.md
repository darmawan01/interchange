# ADR-0019 — Authorization is a module, not a core requirement

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 2

## Context

The failure that motivates this whole project is authorization enforced on one road and forgotten
on another. The obvious conclusion — put authorization in core and make it mandatory — is the wrong
one, and it took writing the extension model out to see why.

Core would then have to know what a permission is. It would own the `(auth)` annotation, the
permission atom grammar, the tenant model, and the decision of what happens when an annotation is
missing. Every one of those is a product opinion. An adopter who authorizes at a gateway, runs
mTLS-only internal services, or ships a public data API would be importing a policy model they will
never call, and a bug in it would be a breaking change for everyone.

The property actually needed is narrower: *whatever* check you install runs on every road. That is
chain symmetry (ADR-0015), and it does not require core to know what a permission is.

## Decision

Authorization lives in `/auth`, a separate Go module. **The module imports core; core does not
import the module.** It gets no privileged access: `Authn` and `Authz` are ordinary
`interchange.Stage` values, appended to a chain by name like anyone else's. It reads *its own*
annotation off `MethodDesc.Desc` — a `protoreflect.MethodDescriptor` that core carries and never
interprets — reached through `interchange.MethodFromContext(ctx)`. That is the entire seam.

Its annotation lives in its own proto (`auth/api/interchange/auth/v1/auth.proto`) with numbers
recorded in `docs/annotation-band.md`, never in core's band. Its policy knob
(`on_missing_annotation`) is the module's, not core's (ADR-0020). The decider is one interface:

```go
type Authorizer interface {
    Authorize(ctx context.Context, procedure string, ann Annotation,
        md map[string]string, msg proto.Message) error
}
```

RBAC ships in the box. OPA, Cedar and Casbin do not, deliberately — the module imports no policy
engine; they arrive as `AuthorizerFactory` registrations from a module that does.

## Consequences

What this buys is the extension model demonstrated rather than described. `/auth` is the worked
example of an outside consumer: if it needed a hook core does not give everyone, the extension point
was not real. It also keeps authorization versioned separately from dispatch, and keeps a policy
engine out of every adopter's binary.

The cost is the one §08 lists under "Costs accepted", and it is not small: **making authz optional
means an adopter can ship a multi-transport service with no authorization at all.** Nothing in core
warns them. `TestCoreServesWithoutTheModule` asserts that this configuration works, because it has
to work for the decision to be honest. Chain symmetry guarantees consistency, not safety. Whether
`ix lint` should warn when no authz module is configured is still open in §08.

A second cost: the module reads its annotation by reflection at dispatch time, which is why core
needed `ResolveOptions` — a descriptor built by anything other than linked generated Go carries
custom options as `dynamicpb` values, and reading them the naive way returns the zero value. An
annotation that reads as absent is an authorization check that stops firing (ADR-0035).

## Alternatives

**Authz in core, mandatory.** Rejected in `docs/08-decisions.md`: "Core owns chain symmetry, not
authorization." It forces a permission model on adopters who have one already.

**Generated enforcement only, no interceptor.** The table cannot see the message body, so
tenant scoping is out of reach. The module does both — the generated table is the build-time gate,
the interceptor is the runtime check — which is the "enforce twice, if you enforce at all" rule.

**A privileged hook in core for authorization specifically.** Then the seam has a special case, and
the next module with a good reason gets one too.

## Evidence

- `auth/README.md` — "Core takes no position on authorization … it does not import this package."
- `auth/authz.go` — `Authz` is an `interchange.Stage`; the ordered, fail-closed decision list is in
  its doc comment.
- `hack/depcheck.sh` — core's graph is asserted to contain no auth module (`grep -Eq '… |/auth$'`)
  and nothing outside the protobuf + connect allowlist.
- **The strongest evidence** is `auth/authz_test.go` `TestThirdPartyAuthorizer`: a bespoke decider
  written inside the test replaces RBAC without touching core, the contract, or any binding. It
  authorizes on the message body, sees the procedure and the permission atom, and its own
  `not_found` code and `NOTE_NOT_FOUND` reason reach the caller unflattened.
- `auth/e2e_test.go` `TestAuthorizationFiresOnBothRoads` (the same denial, same code, same reason
  over `binding/rpc` and over `engine` + `driver/memory`) and `TestCoreServesWithoutTheModule` (the
  cost).
- `auth/authz_test.go` `TestChainIsOrdinary` — no privileged position in the chain.
