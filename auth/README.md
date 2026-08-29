# `/auth` — authorization as a module

Core takes no position on authorization. It does not know what a permission is, it never parses
the `(auth)` annotation, and it does not import this package — `hack/depcheck.sh` asserts the last
part. **Interchange works with this module absent**: a service with an empty chain serves on every
road, and an adopter who authorizes at a gateway, runs mTLS-only internal services, or ships a
public data API installs none of this.

This module is the worked example of the extension model. It gets no privileged access. Its
interceptors are ordinary `interchange.Interceptor` values, and they read *this module's* annotation
off `MethodDesc.Desc` — a `protoreflect.MethodDescriptor` that core carries and never reads —
reached through `interchange.MethodFromContext(ctx)`. That is the whole seam.

What core guarantees in return is the one thing that makes any of this worth doing: **chain
symmetry**. Whatever chain you configure runs identically on every transport. The classic
multi-transport failure — a check enforced in the HTTP handler and silently absent on the bus — is
structurally impossible, because nothing but `Registry.Dispatch` runs a chain.
`e2e_test.go` asserts exactly that: the same call, denied with the same code and the same reason,
over `binding/rpc` and over `engine` + `driver/memory`.

## Wiring it

```go
cfg := auth.Config{
    Provider:            auth.ProviderRBAC,
    OnMissingAnnotation: auth.StrictError,   // the default; the zero value means this too
}

roles, err := auth.NewRBAC(map[string][]string{
    "reader":  {"providers.read"},
    "writer":  {"providers.read", "providers.create", "providers.edit"},
    "admin":   {"providers.*"},
})

chain := interchange.DefaultChain(interchange.Config{}).Append(
    auth.Authn(cfg, auth.NewTokenAuthenticator(tokens)),
    auth.Authz(cfg, roles, auth.WithTenantScoper(auth.PrincipalTenantScoper())),
)

reg.Register(catalogv1.CatalogServiceDesc(), impl, chain)
```

Stages are named (`auth.StageAuthn`, `auth.StageAuthz`), so a deployment inserts, replaces or
removes them by anchor rather than by position.

## The annotation

Lives in this module's proto (`auth/api/interchange/auth/v1/auth.proto`), never in core. Numbers
`50001`, `50007` and `50008` are recorded in `docs/annotation-band.md`.

```protobuf
rpc ListProviders(ListProvidersRequest) returns (ListProvidersResponse) {
  option (interchange.auth.v1.auth) = {
    auth_types: [AUTH_TYPE_SESSION, AUTH_TYPE_WORKLOAD]
    permission: {resource: "providers" verb: VERB_READ}   // atom: "providers.read"
  };
}

rpc Health(HealthRequest) returns (HealthResponse) {
  option (interchange.auth.v1.auth) = {public: true};     // explicit, greppable
}

rpc Reindex(ReindexRequest) returns (ReindexResponse) {   // cross-tenant: no tenant in the request
  option (interchange.auth.v1.auth) = {
    auth_types: [AUTH_TYPE_WORKLOAD]
    permission: {resource: "providers" verb: VERB_EDIT}
    platform: true
  };
}
```

## The three rules

**1 · `absent != public`.** A method with no annotation is denied under the default policy. A
public RPC says `public: true`, so it is greppable and reviewable. `protoc-gen-authz` fails the
build on the same input, which is where you want to find it.

**2 · Fail closed on nil.** An optional dependency that is unwired denies:

| Unwired | Result |
| --- | --- |
| `Authz(cfg, nil)` | denied, `AUTHZ_NOT_WIRED` |
| tenant-scoped RPC with no `WithTenantScoper` | denied, `AUTHZ_NOT_WIRED` |
| `Authn(cfg, nil)` | denied, `AUTHZ_NOT_WIRED` |
| an authenticator returning neither principal nor error | denied, `UNAUTHENTICATED` |
| an envelope that never went through a `Registry` | denied, `AUTHZ_NOT_WIRED` |

An RPC needing a resolver it does not have is a wiring bug, not an open door.

**3 · Enforce twice, if you enforce at all.** The generated table is the build-time gate; the
interceptor is the runtime check. Both feed off the one annotation. The table catches a missing
annotation on an RPC nobody has called yet; the interceptor catches what needs the message body.

## Strictness

`on_missing_annotation` is a policy of **this module**, declared in your config. It is not a
property of core and it is not imposed on anyone who does not install the module.

```yaml
authz:
  provider: rbac                 # or opa | cedar | custom
  on_missing_annotation: error   # error | warn | ignore   (default: error)
```

| Value | Interceptor | `protoc-gen-authz` |
| --- | --- | --- |
| `error` (default) | denies, `AUTHZ_ANNOTATION_MISSING` | fails the build, naming the RPC |
| `warn` | allows, logs the procedure | omits the row, warns on stderr |
| `ignore` | allows silently | omits the row |

`error` is the default because a half-annotated service is worse than an unannotated one: the
reviewer believes the annotations mean something. An annotation that is *present and wrong* — a
permission with no verb, or an atom outside a configured `known_atoms` set — fails the build under
every policy, because it was written and reviewed.

## Swapping the `Authorizer`

The exit criterion for the whole extension model: a third party replaces the decider **without
touching core, the contract, or any binding**. It is one method.

```go
type Authorizer interface {
    Authorize(ctx context.Context, procedure string, ann Annotation,
        md map[string]string, msg proto.Message) error
}
```

```go
// A bespoke permission service. No annotation change, no core change, no
// binding change -- it goes in where RBAC went in.
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

RBAC ships here. **OPA, Cedar and Casbin are not implemented** — deliberately: this module imports
no policy engine. They arrive as `AuthorizerFactory` registrations, which is the seam that would
accept them:

```go
auth.RegisterProvider(auth.ProviderOPA, func(options map[string]string) (auth.Authorizer, error) {
    return opaAdapter(options)      // in your module, importing OPA
})
```

`auth.NewAuthorizer(cfg)` builds the provider named in config; an unregistered name is an error,
never an empty authorizer that allows everything.

## Tenant scoping

The interceptor finds the request's tenant **by reflection, never by a concrete message type** —
it runs in front of every service in the process and can import none of their generated packages.

- by convention: a `tenant_id` / `project_id` string field;
- by annotation, when the message calls them something else:

```protobuf
message ListProvidersRequest {
  string org_id = 1 [(interchange.auth.v1.tenant_id_field) = true];
  string workspace_id = 2 [(interchange.auth.v1.project_id_field) = true];
}
```

The annotation wins; convention is the fallback. `auth.TenantScopeOf(msg)` is the same lookup,
exported for an authorizer that wants it. A tenant-scoped RPC — one that is neither `public` nor
`platform` — whose request names no tenant is denied (`AUTHZ_TENANT_MISSING`): mark it
`platform: true` if it really is cross-tenant.

## `protoc-gen-authz`

An ordinary protoc plugin with no privileged access, shipped with this module and installed only
by adopters who want the build-time gate. It emits the sorted procedure → permission table plus
`Rules()`, `Permissions()` and `Atoms()`, and fails the build on a missing or unknown annotation
per the configured policy.

```yaml
# buf.gen.yaml
plugins:
  - local: bin/protoc-gen-authz
    out: gen/go/authz
    strategy: all              # the table is cross-cutting: one pass over the whole tree
    opt:
      - package=authz
      - on_missing_annotation=error
      - known_atoms=providers.read+providers.create   # optional closed set; "+"-separated
```

Deterministic by construction: rules sorted by procedure, no timestamps, no version strings,
nothing read from the environment — same input, same bytes, so the drift gate does not flap. It
guards on `file.Generate`, so it emits no rows for the annotation protos it imports. Managed mode
(or a `go_package` option) must be on: `protogen` needs a Go import path for every file it
generates.

A procedure with no row in the table has no declaration. A gateway reading the table must treat
that as denied.

## Layout

| File | What |
| --- | --- |
| `auth.go` | `Annotation`, `Permission`, `Authorizer`, the denial reasons |
| `annotation.go` | decoding the `(auth)` option off a method descriptor |
| `authn.go` | credential extraction and verification; the stock token authenticator |
| `authz.go` | the permission interceptor and the tenant-scope seam |
| `rbac.go` | the in-the-box `Authorizer` |
| `tenant.go` | tenant/project lookup by reflection |
| `config.go` | `Config`, the strictness policy, the provider registry |
| `cmd/protoc-gen-authz` | the build-time gate |
| `internal/fixture` | the test contract, compiled at test time so descriptors are real |

```
go test ./...            # runtime rules, both roads, plugin golden + determinism
go test ./internal/authgen -update   # rewrite the golden table
```
