# binding/rest — the partner-facing surface

A REST road with no second contract. A URI exists because an RPC carries a `google.api.http`
annotation and declares `TRANSPORT_REST`; the request binds onto the same request message, and the
call goes to `Registry.Dispatch` like every other road. There is no gateway process, no second
router, and no hand-written handler to keep in step.

Transcoding is in process, by [`connectrpc.com/vanguard`](https://github.com/connectrpc/vanguard-go)
over the Connect handler `binding/rpc` already builds — which is what §04 means by "generated + a
transcoder library". §08 records the decision: a transcoder until a gateway team owns routing.

## Mounting it beside the RPC binding

Register once, mount on each. The chain is configured once and handed to `Register`; neither
binding is given it, which is why the same interceptors run on both roads whether or not anybody
remembers to check.

```go
reg := interchange.NewRegistry()
chain := interchange.DefaultChain(cfg)

if err := reg.Register(catalogv1.CatalogServiceDesc(), impl, chain); err != nil { ... }

rpcBinding := rpc.New(reg)                 // POST /catalog.v1.CatalogService/ListProviders
restBinding := rest.New(reg)               // GET  /v1/catalog/providers
for _, b := range []interchange.Mounter{rpcBinding, restBinding} {
    if err := b.Mount(catalogv1.CatalogServiceDesc()); err != nil { ... }
}

mux := http.NewServeMux()
mux.Handle("/v1/", restBinding.Handler())
mux.Handle("/", rpcBinding.Handler())
```

`rest.Register(sd, impl, chain)` does the register-and-mount in one call when only the REST road is
served.

Mount the two on distinct prefixes, as above, or on distinct listeners. The transcoder also
answers the Connect protocol on the procedure path for the methods it serves — the method set is
still the REST-filtered one, so nothing that declined this road becomes reachable, but a Connect
client pointed at the REST handler gets this surface's snake_case rather than the RPC surface's
camelCase. Routing `/v1/` here and `/` at the Connect binding keeps each audience on the surface
that was chosen for it.

Two things are refused rather than degraded, both at `Mount`:

- a `ServiceDesc` with no `Desc` — there are no annotations to route on, so there is nothing to
  serve;
- a method that declares `TRANSPORT_REST` and carries no `google.api.http` rule — it has no URI to
  be reached at, and mounting nothing while reporting success would let the annotation say one
  thing and the surface do another.

A method that does **not** declare the road, and one marked `internal`, are absent from the
transcoder's schema entirely. They are not routed and then rejected; there is no route. A method
with an HTTP rule that does not declare `TRANSPORT_REST` answers `404` here and still answers over
Connect — that is what makes the transport annotation load-bearing rather than decorative.

## Casing is per surface, and written down

§08: **camelCase on RPC, snake_case on REST.** The two surfaces have different audiences — an SDK
generated from the contract, and a partner reading a URI — and pretending they are one audience is
how a service ends up with a casing nobody chose.

The REST codec sets `protojson.MarshalOptions{UseProtoNames: true}`, so `display_name` goes out as
`display_name`. Nothing special is needed on the way in: `protojson` accepts a field under both its
proto name and its JSON name, so a partner already sending `displayName` keeps working.

`EmitUnpopulated` stays at vanguard's default, `true`: a response carries every field of the
message, zero-valued ones included, so a partner never has to distinguish "absent" from "zero" on a
field the contract says is always there. This is the one way the two surfaces differ beyond casing,
and it is a choice, not an oversight.

The emitted OpenAPI honours the same decision — property names are proto names — because a spec
that says `probeId` over a wire that says `probe_id` is worse than no spec.

## Errors are `problem+json`

§04's status row for the REST road. A failed call answers with `application/problem+json` (RFC
9457), the machine-readable reason in the `Ix-Reason` header — the same header the Connect binding
sets, so a client reads the reason the same way on both roads without parsing a body.

The status comes from `errors.HTTPStatus`, which is connect-go's own table. That is deliberate:
the two HTTP roads are two exits from one dispatch, and a client that gets `504` from one and `408`
from the other for the same handler error has been told two different things.

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "no such provider",
  "instance": "/v1/catalog/providers/prov_7",
  "reason": "PROVIDER_NOT_FOUND"
}
```

The projection lives in the optional `/errors` module, not here — the taxonomy is that module's
business. This binding holds only the seam:

```go
rest.New(reg, rest.WithProblemWriter(func(w http.ResponseWriter, r *http.Request, f rest.Failure) {
    // f.Err is the handler's error, recovered from below the transcoder;
    // f.Status and f.Detail are all there is when the transcoder refused the
    // request before any handler ran.
}))
```

Vanguard renders a failure as the JSON form of a gRPC status, which is right for a transcoder and
wrong for this surface. So a response is held back the moment its status says failure, and the
accurate error — code, reason, message and metadata — is picked up from below the transcoder where
the handler's own error is still intact. A successful response is never buffered.

## Emitting the OpenAPI document

`binding/rest/openapi` reads the same descriptors and emits OpenAPI 3.1. It is a build-time
artifact and takes descriptors, not a registry:

```go
fds := // *descriptorpb.FileDescriptorSet, from `buf build -o -` or protoc
doc, err := openapi.FromFileDescriptorSet(fds, openapi.Options{
    Title:   "Catalog API",
    Version: "1.0.0",
    Servers: []string{"https://api.example.com"},
    Files:   []string{"catalog/v1/catalog.proto"}, // empty means every file
})
```

What it emits, and why it is safe to commit:

- **Deterministic.** Sorted paths, sorted properties, sorted parameters, no timestamps and no
  version strings that change per build. Same input, same bytes — otherwise the drift gate flaps
  and nobody trusts it. There is a test that emits twice and compares.
- **Skips what the binding skips.** Not on the road, or `internal`, means absent. An internal RPC
  in a partner-facing document is a leak, and this is the only thing standing between a partner and
  one.
- **Loud where it cannot represent something.** A REST method with no HTTP rule, a streaming
  method, nested `additional_bindings`, two methods claiming one verb and path: all errors naming
  the method, never a partial document.
- Paths, path parameters and the request body come from the HTTP rule; everything the URI did not
  bind becomes a query parameter, dotted for a nested field the way `google.api.http` addresses it.
- Every operation carries a `default` response of `application/problem+json`, referencing the same
  `Problem` shape the binding writes.
- 64-bit integers are documented as strings, because that is what `protojson` puts on the wire.

## The fixture

`internal/testfixture` is a hand-written stand-in for generated service code over a **real**
compiled descriptor set: the transcoder routes on the annotation, so a fixture that invents its
descriptor tests nothing. `api/rest/test/v1/probe.proto` is compiled by buf and the descriptor set
is embedded; the messages are `dynamicpb`, so no generated Go can quietly supply what an annotation
should have.

Regenerate it from the repo root after editing the .proto:

```
buf build --config binding/rest/internal/testfixture/buf.workspace.yaml \
  -o binding/rest/internal/testfixture/testdata/fixture.binpb .
```

The workspace config exists because a buf module cannot name a path outside its own context
directory, and the fixture imports `interchange/transport/v1/transports.proto` from the repo's `api`
module. Passing the workspace to `--config` keeps the repo's own `buf.yaml` untouched.

Update the golden document with `go test ./openapi -update`.
