# 05 — Cross-cutting concerns

Authorization, validation and an error taxonomy are **optional modules**. Core takes no position on
any of them, imports none of them, and works with none of them installed —
`hack/depcheck.sh` asserts it in CI.

**An empty chain is valid.** `interchange.Chain()` with no stages serves on every road, and the
acceptance test `TestAServiceWithAnEmptyChainWorks` proves it over HTTP and a real NATS broker with
no credential anywhere. If you authorize at a gateway, run mTLS-only internal services, or ship a
public data API, you install none of this and lose nothing.

What core guarantees in return is the one thing that makes installing them worth doing: **whatever
chain you configure runs identically on every transport**
([ADR-0015](../adr/0015-chain-symmetry-is-the-one-guarantee.md)). The classic multi-transport
failure — a check enforced in the HTTP handler and silently absent on the bus — is structurally
impossible, because nothing but `Registry.Dispatch` runs a chain and no binding holds one.

Each module below has a README with the full reference. This page is the task-shaped version:
install it, wire it, know what it costs.

---

## `/auth`

→ [`auth/README.md`](../../auth/README.md) · [ADR-0019](../adr/0019-authorization-is-a-module.md),
[ADR-0020](../adr/0020-absent-is-not-public.md)

### Install

```yaml
# interchange.yaml — the optional module's block.
# Delete it and core never learns what a permission is.
auth:
  provider: rbac
  on_missing_annotation: error   # error | warn | ignore
```

```yaml
# and the build-time gate, in generate:
- plugin: ./bin/protoc-gen-authz
  out: gen/go/authz
  strategy: all                  # the table is cross-cutting: one pass over the whole tree
  opt:
    - package=authz
    - on_missing_annotation=error
    - known_atoms=providers.read+providers.create+providers.edit
```

`known_atoms` is a closed set: an atom that is not in it fails the build rather than granting
nothing at runtime.

### Wire

```go
authCfg := auth.Config{
	Provider: auth.ProviderRBAC,
	// The default, spelled out: a method with no (auth) annotation is
	// denied. absent != public.
	OnMissingAnnotation: auth.StrictError,
}

roles, err := auth.NewRBAC(map[string][]string{
	"reader":   {"providers.read"},
	"writer":   {"providers.read", "providers.create", "providers.edit"},
	"syncer":   {"providers.read", "providers.edit"},
	"platform": {"providers.*"},
})

chain := interchange.DefaultChain(cfg).
	After(interchange.StageDeadline, auth.Authn(authCfg, credentials)).
	Append(auth.Authz(authCfg, roles, auth.WithTenantScoper(auth.PrincipalTenantScoper())))
```

Two stages, both named (`auth.StageAuthn`, `auth.StageAuthz`), so a deployment inserts, replaces or
removes them by anchor rather than by position.

**authn** verifies the credential and puts a `*auth.Principal` on the context. The stock
`auth.NewTokenAuthenticator(map[string]*auth.Principal)` is a static table — the catalog example
uses it, and a real deployment verifies a JWT or calls an identity service behind the same
`auth.Authenticator` interface. Swapping it changes nothing else.

**authz** decides. It reads *this module's* annotation off `MethodDesc.Desc` — a
`protoreflect.MethodDescriptor` that core carries and never reads — reached through
`interchange.MethodFromContext(ctx)`. That is the entire seam, and it is the same one your own
module would use.

### RBAC

`auth.NewRBAC(map[string][]string)` maps a role to the atoms it grants. `providers.*` is a wildcard
over one resource. It returns an error on an unknown shape rather than an authorizer that allows
everything.

### Tenant scoping

The interceptor finds the request's tenant **by reflection, never by a concrete message type** — it
runs in front of every service in the process and can import none of their generated packages.

- by convention: a `tenant_id` / `project_id` string field;
- by annotation, when the message calls them something else:

```protobuf
message ListProvidersRequest {
  string org_id = 1 [(interchange.auth.v1.tenant_id_field) = true];
}
```

The annotation wins; convention is the fallback. `ix describe` tells you which one fired:

```
    tenant field tenant_id (convention)
```

`auth.PrincipalTenantScoper()` is the stock scoper: the principal's `Tenants` must contain the
request's tenant. A tenant-scoped RPC — one that is neither `public` nor `platform` — whose request
names no tenant is **denied** with `AUTHZ_TENANT_MISSING`. Mark it `platform: true` if it really is
cross-tenant.

### The strictness policy

`on_missing_annotation` is a policy of **this module**, declared in your config. It is not a
property of core and it is not imposed on anyone who does not install the module.

| Value | Interceptor | `protoc-gen-authz` |
| --- | --- | --- |
| `error` (default) | denies, `AUTHZ_ANNOTATION_MISSING` | fails the build, naming the RPC |
| `warn` | allows, logs the procedure | omits the row, warns on stderr |
| `ignore` | allows silently | omits the row |

`error` is the default because a half-annotated service is worse than an unannotated one: the
reviewer believes the annotations mean something. An annotation that is *present and wrong* — a
permission with no verb, or an atom outside `known_atoms` — fails the build under **every** policy,
because it was written and reviewed.

### Fail closed on nil

An optional dependency that is unwired denies. This is the part worth internalising:

| Unwired | Result |
| --- | --- |
| `Authz(cfg, nil)` | denied, `AUTHZ_NOT_WIRED` |
| tenant-scoped RPC with no `WithTenantScoper` | denied, `AUTHZ_NOT_WIRED` |
| `Authn(cfg, nil)` | denied, `AUTHZ_NOT_WIRED` |
| an authenticator returning neither principal nor error | denied, `UNAUTHENTICATED` |
| an envelope that never went through a `Registry` | denied, `AUTHZ_NOT_WIRED` |

An RPC needing a resolver it does not have is a wiring bug, not an open door.

### Swapping the `Authorizer`

The exit criterion for the whole extension model: a third party replaces the decider **without
touching core, the contract, or any binding**. It is one method.

```go
type Authorizer interface {
	Authorize(ctx context.Context, procedure string, ann Annotation,
		md map[string]string, msg proto.Message) error
}
```

```go
type remote struct{ client *policy.Client }

func (r remote) Authorize(ctx context.Context, procedure string, ann auth.Annotation,
	md map[string]string, msg proto.Message) error {
	p, _ := auth.PrincipalFromContext(ctx)
	ok, err := r.client.Check(ctx, p.Subject, ann.Permission.Atom(), procedure)
	if err != nil {
		return err
	}
	if !ok {
		return interchange.Errorf(interchange.CodePermissionDenied, "denied").
			WithReason(auth.ReasonPermissionDenied)
	}
	return nil
}

chain := interchange.DefaultChain(cfg).Append(auth.Authz(authCfg, remote{client}))
```

Returning an `*interchange.Error` carries your code and reason to the caller unchanged on every
road — an authorizer that answers "not found" to avoid leaking existence is believed rather than
flattened to "denied". Anything else becomes `PERMISSION_DENIED`.

The `examples/catalog` composition root exposes the same seam as an option, so a deployment can
swap the decider and the acceptance tests can wrap it to record what it was asked:

```go
func WithAuthorizer(a auth.Authorizer) ChainOption
func WithAuthenticator(a auth.Authenticator) ChainOption
```

**OPA, Cedar and Casbin are not implemented** — deliberately: this module imports no policy engine.
They arrive as `auth.RegisterProvider` registrations, in your module, importing your engine.

### Enforce twice, if you enforce at all

The generated table is the build-time gate; the interceptor is the runtime check. Both feed off the
one annotation. The table catches a missing annotation on an RPC nobody has called yet; the
interceptor catches what needs the message body. `TestGeneratedPermissionTableMatchesTheRuntimeInterceptor`
asserts the two agree, procedure for procedure.

### The cost

Making authz optional means an adopter can ship a multi-transport service with **no authorization
at all** and nothing in the framework will complain. That is the price of core taking no position,
and it is why `on_missing_annotation: error` is the default the moment you install the module.

---

## `/validate`

→ [`validate/README.md`](../../validate/README.md)

Rules are written **once, in the contract**, as
[protovalidate](https://buf.build/bufbuild/protovalidate) options. This module is the one
interceptor that enforces them.

### Install

```bash
buf dep add buf.build/bufbuild/protovalidate && buf dep update
```

```protobuf
message CreateProviderRequest {
  string name  = 1 [(buf.validate.field).string = {min_len: 3, max_len: 64}];
  string email = 2 [(buf.validate.field).string.email = true];
  int32  rate_limit = 3 [(buf.validate.field).int32.gte = 0];
}
```

### Wire

```go
chain := interchange.DefaultChain(cfg).Append(validate.Stage(nil))
```

`validate.Stage(nil)` uses `protovalidate.GlobalValidator` — the shared instance with the shared
rule cache, which is lower memory than one validator per service and the right default when you
have no reason to configure anything. To configure one:

```go
v, err := protovalidate.New(
	protovalidate.WithFailFast(),
	protovalidate.WithExtensionTypeResolver(myTypes),
)
chain := interchange.DefaultChain(cfg).Append(validate.Stage(v))
```

Order it by name, never by position:

```go
interchange.DefaultChain(cfg).
	Before(validate.StageName, authn()).   // credentials before rules
	After(validate.StageName, authz())     // a resource-aware check that never reads an unchecked field
```

### "Written once, applied everywhere" — stated precisely

The claim is narrower and stronger than it sounds, and it is split in two:

- **Core guarantees** that a chain runs identically on every road, in the same order, over the same
  envelope. This stage *cannot* be enforced on HTTP and silently skipped on the bus.
- **This module guarantees** that the enforcement is transport-blind: it validates `Envelope.Msg`,
  the decoded message dispatch produced *before* the chain ran. There is no second,
  transport-shaped copy of the request to check, and therefore no second place for the rule to be
  subtly different.

So a violation is *the same violation* everywhere: same `invalid_argument` code, same reason, same
field paths, same rule ids, same messages. `validate_test.go` asserts exactly that — one registry,
one chain, the identical bad request over `binding/rpc` and over `engine` + `driver/memory`, and a
`slices.Equal` on the violations that come back.

What it does **not** claim: a rule enforced at a client, at a gateway you own, or in a hand-written
route bolted onto the same mux beside Interchange. Chain symmetry covers the roads Interchange owns.

### Reading a violation back

`interchange.CodeInvalidArgument`, reason `INVALID_ARGUMENT`, and the field detail in the error's
metadata — ordinary metadata, so it arrives as HTTP headers on Connect and in `Response.metadata`
on a bus, with no per-transport code:

```
ix-violations                3
ix-violation-1-field         name
ix-violation-1-rule          string.min_len
ix-violation-1-message       value length must be at least 3 characters
```

```go
validate.ViolationsOf(err)      // whichever road the error arrived on
validate.ViolationsFrom(md)     // if you are holding headers rather than an error
validate.Count(err)             // the exact number, which can exceed the number reported
```

`WithMaxDetails` (default 8) caps the metadata so a message with a hundred bad fields does not
produce a header block a proxy rejects.

A rule that will not compile, or a CEL expression that fails at runtime, comes back as
`CodeInternal` — that is a defect in the contract, not in the request, and reporting it as
`invalid_argument` sends the caller chasing their own payload.

### Wire it before you need it

The catalog contract declares no protovalidate rules yet, and the stage is in the chain anyway. The
reason is in `wire.go`: *the day a rule is added is not the day anyone should be editing the chain.*

---

## `/errors`

→ [`errors/README.md`](../../errors/README.md) ·
[ADR-0021](../adr/0021-core-moves-errors-without-meaning.md)

Core already carries the shape — `interchange.Error{Code, Message, Reason}` — and the envelope
reserves `code`, `message` and `reason`. Core assigns `Reason` **no meaning**; it moves it. This
module is the meaning: a closed enum, an interceptor that enforces membership, and the RFC 9457
projection.

### Why the reason and not the message

A client has to branch on *something*. If that something is the message, then rewording
`"no such provider"` → `"provider not found"` is a breaking change to every caller and nobody
notices until the pager goes off. `code` alone is too coarse: `not_found` cannot tell "no such
provider" from "no such region", and those are different recoveries.

So the branch point is a third thing: a short, stable, machine-readable string drawn from a
**closed enum in the contract**. Closed matters — a reason a client cannot enumerate from the proto
is not something it can write a `switch` over.

### Install

```protobuf
enum CatalogReason {
  CATALOG_REASON_UNSPECIFIED        = 0;   // never a reason: nothing to branch on
  CATALOG_REASON_PROVIDER_NOT_FOUND = 1;   // travels as PROVIDER_NOT_FOUND
}
```

A reason travels as the enum value name with the enum's own prefix trimmed.

```go
chain := interchange.DefaultChain(cfg).
	After(interchange.StageTelemetry, errors.Stage(
		errors.WithReasons(errors.EnumSet(catalogv1.CatalogReason(0).Descriptor())),
	))
```

**Position: directly inside `telemetry`, and outside `recover`.** Everything it normalises has to be
below it, including the internal error `recover` makes out of a panic. Appending it innermost still
works, but then it never sees a panic, and its unknown-reason panic is swallowed by `recover` into
a plain 500.

`errors.SetOf("A", "B", ...)` is the escape hatch for a taxonomy not in a proto yet — the catalog
example uses it and says so. Prefer the enum: a Go slice is not something a TypeScript client can
read. The stock set stays accepted alongside yours, because a generic `PERMISSION_DENIED` raised by
an interceptor is legal in every service.

### Raising one

```go
return nil, errors.NotFound(ReasonProviderNotFound, "no provider %q in tenant %q",
	req.GetProviderId(), req.GetTenantId())
```

Each constructor takes the **reason first**, because the reason is the part a client sees:
`errors.NotFound`, `errors.PermissionDenied`, `errors.InvalidArgument`, `errors.AlreadyExists`,
`errors.FailedPrecondition`, …

### One error, four surfaces

`errors/foursurfaces_test.go` asserts this table rather than describing it, from one registry with
one chain:

| Surface | Form |
| --- | --- |
| handler | `errors.NotFound("PROVIDER_NOT_FOUND", "no such provider")` |
| envelope | `Response{code: 5, reason: "PROVIDER_NOT_FOUND"}` — **canonical** |
| HTTP | `404`, header `Ix-Reason: PROVIDER_NOT_FOUND` |
| REST | `404` + `application/problem+json` carrying `"reason": "PROVIDER_NOT_FOUND"` |

Live, from the running catalog:

```
$ curl -s -i "localhost:8080/v1/catalog/providers?tenant_id=acme"
HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json
Ix-Reason: UNAUTHENTICATED

{"type":"about:blank", "title":"Unauthorized", "status":401,
 "detail":"/catalog.v1.CatalogService/ListProviders: no credential",
 "instance":"/v1/catalog/providers", "reason":"UNAUTHENTICATED"}
```

The reason string is byte-identical on every road, which is what lets a client branch once. The
code → HTTP status table is connect-go's, deliberately: two roads out of one dispatch must not tell
a client two different things about the same handler error.

### Unknown reasons

An unknown reason is a programming error — a typo, or a value someone forgot to append to the enum.
What the process does about it is a deployment decision:

| Policy | Behaviour | Default when |
| --- | --- | --- |
| `UnknownReasonPanic` | panics | `testing.Testing()` — under `go test` |
| `UnknownReasonRewrite` | logs, substitutes the code's stock reason | everywhere else |
| `UnknownReasonLog` | logs, lets it through | migrating an existing taxonomy |
| `UnknownReasonAllow` | no check | — |

The runtime default rewrites rather than passes through: an unregistered reason on the wire breaks
the only promise this module makes.

### Your own error type

Implement `Mapper` once and stop writing `interchange.Error` at every call site:

```go
errors.Stage(errors.WithMapper(errors.MapperFunc(
	func(ctx context.Context, procedure string, err error) *interchange.Error {
		var nf *catalog.NotFoundError
		if errors.As(err, &nf) {
			return errors.NotFound("PROVIDER_NOT_FOUND", "no such provider %q", nf.ID)
		}
		return errors.DefaultMapper{Redact: true}.Map(ctx, procedure, err)
	})))
```

`DefaultMapper` on its own: an error carrying a reason passes through; an error with a code but no
reason gets that code's stock reason; a context error keeps `deadline_exceeded` or `canceled`;
anything else is `internal` + `INTERNAL`. **Set `Redact: true`** so `sql: no rows in result set`
does not become a public API.

---

## Telemetry: the `Observer` seam

Core ships no OpenTelemetry dependency and never will — `hack/depcheck.sh` is an allowlist, and
adding a line to it is a design decision, not a build fix. What core ships is one interface:

```go
// Observer is core's telemetry seam. Core ships no OpenTelemetry dependency
// -- you supply the adapter, or take the slog one below.
type Observer interface {
	// ObserveCall starts an observation for a procedure and returns the
	// context handlers should use plus a function to end it.
	ObserveCall(ctx context.Context, procedure string) (context.Context, func(err error))
}
```

The one in the box:

```go
chain := catalog.Chain(interchange.Config{
	Observer: interchange.SlogObserver(log),
})
```

which logs `procedure`, `code` and `took` per call. An OpenTelemetry adapter is the same shape:
start a span in `ObserveCall`, return the context carrying it, and end it — recording the error —
in the returned closure.

`Config.Observer` is nil by default, and the `telemetry` stage then does nothing. That is the
whole cost of the seam.

**The label is the procedure string**, and it is the same string on every road. That is what makes
`TestSameProcedureStringInAuthzCheckMetricsLabelsAndTraceSpanOnBothRoads` a meaningful test rather
than a tautology.

---

## The two core interceptors you did not configure

They come from `DefaultChain` and are worth knowing about:

**`recover`** turns a panic into an internal error and logs it with a stack. On a bus this is not a
nicety — a panic takes the subscriber with it, and the process stops receiving work.
`Config.Logger` is where the stack goes; nil means `slog.Default()`.

**`deadline`** applies `Config.DefaultTimeout` when a caller supplied none. Zero means no default.
This is the one thing HTTP gives you free and a bus does not: a bus call carries no deadline unless
someone sets one, and a handler nobody is waiting for is work the process should not do.

Next: [06 Adding a transport](06-adding-a-transport.md).
