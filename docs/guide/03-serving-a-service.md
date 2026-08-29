# 03 — Serving a service

You write two files: the contract, and a handler. Everything between them is one composition root
that you also write, once, and that does not grow when you add a road.

The reference is [`examples/catalog/wire.go`](../../examples/catalog/wire.go) and
[`examples/catalog/server.go`](../../examples/catalog/server.go). Read them alongside this page —
this is the annotated walkthrough.

## The handler

`protoc-gen-bus` emits an interface. Implement it, and nothing else:

```go
// gen/go/catalog/v1/catalogv1bus/catalog_bus.pb.go
type CatalogServiceHandler interface {
	CreateProvider(context.Context, *catalogv1.CreateProviderRequest) (*catalogv1.CreateProviderResponse, error)
	GetProvider(context.Context, *catalogv1.GetProviderRequest) (*catalogv1.GetProviderResponse, error)
	ListProviders(context.Context, *catalogv1.ListProvidersRequest) (*catalogv1.ListProvidersResponse, error)
	Reconcile(context.Context, *catalogv1.ReconcileRequest) (*catalogv1.ReconcileResponse, error)
	SyncProvider(context.Context, *catalogv1.SyncProviderRequest) (*catalogv1.SyncProviderResponse, error)
}
```

An implementation, from `server.go`:

```go
func (s *Server) GetProvider(_ context.Context, req *catalogv1.GetProviderRequest) (*catalogv1.GetProviderResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.byID[req.GetProviderId()]
	if !ok || p.GetTenantId() != req.GetTenantId() {
		// The reason is what a client branches on; the message is for a
		// human. A tenant mismatch answers "not found" rather than "denied"
		// so the id space of another tenant is not enumerable.
		return nil, errors.NotFound(ReasonProviderNotFound, "no provider %q in tenant %q",
			req.GetProviderId(), req.GetTenantId())
	}
	return &catalogv1.GetProviderResponse{Provider: proto.Clone(p).(*catalogv1.Provider)}, nil
}
```

Note what is **not** in that signature: no `http.ResponseWriter`, no `*connect.Request`, no
envelope, no metadata, no broker. `examples/catalog/server.go` names no transport type at all —
it could not tell you which road a call arrived on, and the acceptance test
`TestTheInterceptorChainCameAlongUnchanged` moves a call from HTTP to NATS to prove the handler
does not change, recompile or notice.

If you need the caller's identity or the request's metadata, take it off the context — that is what
`auth.PrincipalFromContext(ctx)` and `interchange.MethodFromContext(ctx)` are for. Neither is a
transport type.

## One chain, one registry

This is the whole design in two sentences. **Build one `*interchange.ChainSpec`. Register the
service once against one `*interchange.Registry`.** Every binding you then construct over that
registry inherits the same dispatch and the same chain, because nothing but `Registry.Dispatch`
runs a chain and no binding holds one ([ADR-0016](../adr/0016-chain-symmetry-is-structural.md)).

### The chain

`interchange.DefaultChain(cfg)` is core's three stock interceptors, outermost first:

```go
func DefaultChain(cfg Config) *ChainSpec {
	return Chain(Telemetry(cfg), Recover(cfg), Deadline(cfg))
}
```

Core ships those three and no more, because each is a property of *dispatch* rather than of your
product ([ADR-0018](../adr/0018-three-stock-interceptors.md)). Their names are API:
`interchange.StageTelemetry`, `StageRecover`, `StageDeadline`.

`interchange.Config` is the only knob:

```go
chain, err := catalog.Chain(interchange.Config{
	Observer: interchange.SlogObserver(log),
	Logger:   log,
	// A bus call carries no deadline unless someone sets one, and a
	// handler nobody is waiting for is work the process should not do.
	DefaultTimeout: 30 * time.Second,
})
```

`Observer` is core's telemetry seam — one interface, one method, no OpenTelemetry dependency
anywhere in core. `interchange.SlogObserver(log)` is the one in the box; see
[05](05-cross-cutting.md#telemetry-the-observer-seam).

### Extending it by name, never by position

```go
// ChainSpec, from chain.go — every combinator returns a NEW value.
func (c *ChainSpec) After(anchor string, stages ...Stage) *ChainSpec
func (c *ChainSpec) Before(anchor string, stages ...Stage) *ChainSpec
func (c *ChainSpec) Replace(anchor string, stage Stage) *ChainSpec
func (c *ChainSpec) Remove(anchor string) *ChainSpec
func (c *ChainSpec) Append(stages ...Stage) *ChainSpec    // innermost, closest to the handler
func (c *ChainSpec) Prepend(stages ...Stage) *ChainSpec   // outermost
func (c *ChainSpec) Names() []string
func (c *ChainSpec) Err() error
```

A positional chain breaks **silently** the day a stage is inserted upstream. A named one fails
**loudly** if the anchor disappears: `After("authn", ...)` on a chain with no `authn` stage does not
guess, it records an error that `Err()` returns and `Register` refuses
([ADR-0017](../adr/0017-named-anchors-not-positions.md)).

`ChainSpec` is immutable, so handing the same chain to two bindings cannot let one mutate the
other's — which is the other half of why chain symmetry holds.

**Always check `Err()`.** Combinator errors accumulate rather than panicking:

```go
if err := chain.Err(); err != nil {
	return nil, fmt.Errorf("catalog: chain: %w", err)
}
```

### The catalog's chain, and why it is in that order

From `wire.go`:

```go
chain := interchange.DefaultChain(cfg).
	After(interchange.StageTelemetry, errors.Stage(errors.WithReasons(Reasons()))).
	After(interchange.StageDeadline, auth.Authn(authCfg, c.authn)).
	After(auth.StageAuthn, validate.Stage(nil)).
	Append(auth.Authz(authCfg, c.authz, auth.WithTenantScoper(auth.PrincipalTenantScoper())))
```

Seven stages, outermost first:

| Stage | Owner | Why it is here |
| --- | --- | --- |
| `telemetry` | core | one observation per call, labelled by procedure — the same label on every road |
| `errors` | `/errors` | **inside telemetry and outside `recover`** |
| `recover` | core | a panic on a bus takes the subscriber with it |
| `deadline` | core | the one thing HTTP gives free and a bus does not |
| `authn` | `/auth` | who is calling — **before `validate`** |
| `validate` | `/validate` | declarative field rules — **before `authz`** |
| `authz` | `/auth` | may they do this, to this tenant |

Three of those placements are load-bearing, and the example's comments say so:

**`errors` outside `recover`.** Everything the taxonomy normalises has to be *below* it, including
the internal error `recover` makes out of a panic. `Append`ing `errors` innermost also works, but
then it never sees a panic — and its unknown-reason panic gets swallowed by `recover` into a plain
500. Hence `After(StageTelemetry, ...)` rather than `Append`.

**`authn` before `validate`.** An anonymous caller is turned away without spending CPU on their
payload.

**`validate` before `authz`.** The authz stage reads `tenant_id` off the message; a permission
decision should not run against a request that was never checked. The catalog contract declares no
protovalidate rules yet, so today this stage is a no-op — **it is wired anyway, because the day a
rule is added is not the day anyone should be editing the chain.**

Both deviate from the sketch in `docs/06`, deliberately, and both are documented in the modules
that own them (`errors/README.md`, `validate/README.md`).

### An empty chain is valid

```go
reg.Register(desc, impl, interchange.Chain())   // no stages
```

Core takes no position on authorization, validation or error taxonomy. A service with an empty
chain serves on every road, and the acceptance test `TestAServiceWithAnEmptyChainWorks` asserts it
over HTTP and over a real NATS broker with no credential anywhere.

## The registry, and registering once

```go
func Wire(impl catalogv1bus.CatalogServiceHandler, chain *interchange.ChainSpec) (*Service, error) {
	reg := interchange.NewRegistry()
	if err := catalogv1bus.RegisterCatalogService(reg, impl, chain); err != nil {
		return nil, fmt.Errorf("catalog: register: %w", err)
	}
	...
}
```

`RegisterCatalogService` is generated. Its first parameter is `interchange.Registrar`, an interface
both `*interchange.Registry` and a binding satisfy — so generated wiring does not know which it was
handed.

**The chain is bound at registration, and never handed to a binding.** That is not a convention, it
is the mechanism: a binding cannot vary a chain because a binding never sees one. It is also why
`Registry.Register` rejects a second registration of the same service — a shadowed handler is a
production-only bug, and "serve the same registry with a different chain" is deliberately not
expressible.

Useful reads off a registry:

```go
reg.Procedures()                      // []string, sorted — what catalogd logs at startup
reg.Method(procedure)                 // (*MethodDesc, bool)
reg.ChainNames(procedure)             // ([]string, bool) — outermost first; what a symmetry test compares
reg.MethodsOn(transportv1.Transport_TRANSPORT_REST)   // []*MethodDesc
reg.Services()                        // []*ServiceDesc
```

## Mounting the roads

Every binding below is constructed **over the same registry**, and none of them takes a chain.

### Connect over HTTP

```go
binding := rpc.New(reg)
if err := binding.Mount(catalogv1bus.NewCatalogServiceDesc()); err != nil {
	return nil, fmt.Errorf("catalog: mount rpc: %w", err)
}
```

`Mount` walks the `ServiceDesc` and skips any method that does not name `TRANSPORT_RPC`, and any
method marked `Internal`. `binding.Handler()` returns an `http.Handler` serving
`POST /catalog.v1.CatalogService/*`, and `*Binding` is itself an `http.Handler`.

`rpc.Expose(t)` overrides which road's method set it mounts; `rpc.WithHandlerOptions(...)` passes
connect-go handler options straight through.

### The REST road

```go
// The second HTTP road, over the same registry and with no second
// Register call. Which methods it serves was decided in the .proto: the
// three that name TRANSPORT_REST and carry a google.api.http rule.
restBinding := rest.New(reg)
if err := restBinding.Mount(catalogv1bus.NewCatalogServiceDesc()); err != nil {
	return nil, fmt.Errorf("catalog: mount rest: %w", err)
}
```

In-process transcoding: no second router, no second contract, no hand-written handler. `rest.New`
takes `rest.WithProblemWriter(w)` to swap the error projection and `rest.Expose(t)` like the RPC
binding.

Both roads onto one listener:

```go
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", s.REST.Handler())
	mux.Handle("/", s.RPC.Handler())
	return mux
}
```

They are kept apart deliberately. The transcoder also answers Connect on the procedure path, but
with **this surface's** snake_case casing — so a browser client that wandered onto it would get a
different spelling of the same message. One prefix per audience.

### The bus — NATS, or in-process

```go
func (s *Service) ServeBus(ctx context.Context, drv interchange.Driver, opts ...engine.ServerOption) (*engine.Server, error) {
	srv := engine.NewServer(drv, s.Registry, opts...)
	if err := srv.Start(ctx); err != nil {
		return nil, fmt.Errorf("catalog: serve bus: %w", err)
	}
	s.busses = append(s.busses, srv)
	return srv, nil
}
```

The driver is the **only** thing that differs between an in-process bus and NATS. Nothing above
that line changes, and the engine has no idea which one it was handed — `catalogd` proves it in
five lines:

```go
// busDriver is the only place in this binary that names a broker. Nothing
// above it knows which one it got, which is the seam the engine is built on.
func busDriver(url string) (interchange.Driver, error) {
	if url == "" {
		return memory.New().Driver("catalogd"), nil
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return natsdriver.New(conn)
}
```

`engine.NewServer` does not subscribe until `Start`. `Server.Plan()` is what it *will* subscribe,
sorted — `ix describe` prints it and tests assert on it. Server options worth knowing:

| Option | Use it when |
| --- | --- |
| `engine.WithConcurrency(n)` | cap in-flight handler calls; zero is unlimited |
| `engine.WithMaxMessage(n)` | **any transport reachable by something you do not trust** — chunking otherwise lets a peer stream an unbounded body into memory a chunk at a time |
| `engine.WithReplyTimeout(d)` | bound writing a reply when the request carried no deadline |
| `engine.Expose(t)` | override which road's procedures to subscribe; the default is the driver's declared transport |

JetStream is a **separate constructor**, `natsdriver.NewJetStream(ctx, conn, natsdriver.WithStream(name))`,
not a flag on the core driver — the two make different delivery promises and
[ADR-0027](../adr/0027-two-nats-tiers.md) explains why conflating them would be dishonest.

### MQTT 5

```go
drv, err := mqtt.Connect(ctx, mqtt.Config{
	URL:      "tcp://broker:1883",
	QoS:      1,           // 1 or 2; QoS 0 would drop chunks of a message with no way to notice
	ClientID: "catalogd",
})
if err != nil {
	return err
}
srv, err := svc.ServeBus(ctx, drv)
```

MQTT 5 only ([ADR-0026](../adr/0026-mqtt-5-only.md)): 3.1.1 has no response topic, no correlation
data and no user properties, which would mean the engine carrying a per-transport fallback for
every one of them. The driver is also registered as `"mqtt"` with `interchange.RegisterDriver`, so
it can be built from a config block by name.

### WebSocket

The shape is different, and for a reason: a WebSocket is a connection, not a broker. There is one
driver and one engine server **per socket**.

```go
mux.Handle("/ws", ws.NewServer(reg))
```

That is the whole server side. `NewServer` accepts each socket, builds a driver for it, and starts
an `engine.Server` bound to that driver — subscribed before the first frame is read, stopped when
the socket goes away. Engine options pass through:

```go
mux.Handle("/ws", ws.NewServer(reg,
	ws.WithOrigins("app.example.com"),
	ws.WithServerOptions(
		engine.WithConcurrency(64),
		engine.WithMaxMessage(4<<20),
	),
))
```

If a connection has to do something else as well, `ws.Handler(setup, opts...)` hands you the socket
and leaves the engine server to you; `setup` runs before the read loop starts, so a subscription it
makes cannot miss a frame, and it must not block.

A socket has exactly one channel, so `Address()` ignores the procedure and the procedure travels in
the envelope ([ADR-0028](../adr/0028-websocket-is-one-channel.md)). A browser also cannot set an
`Authorization` header on an upgrade, which is why the driver has a handshake frame —
[04](04-calling-a-service.md#websocket) covers the client half.

> **What the worked example actually wires.** `examples/catalog` mounts Connect, REST, and the bus
> over `driver/memory` or NATS. MQTT and WebSocket are exercised by their own modules'
> tests against real brokers and real sockets, not by the catalog.

## The composition root, end to end

`cmd/catalogd/main.go`, with the flags and signal handling removed:

```go
chain, err := catalog.Chain(interchange.Config{
	Observer:       interchange.SlogObserver(log),
	Logger:         log,
	DefaultTimeout: 30 * time.Second,
})

impl := catalog.NewServer()
impl.Seed("acme", "stripe", "adyen")

svc, err := catalog.Wire(impl, chain)      // one registry, RPC + REST mounted
defer func() { _ = svc.Close() }()

drv, err := busDriver(natsURL)
_, err = svc.ServeBus(ctx, drv)            // the third road

srv := &http.Server{Addr: addr, Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
srv.ListenAndServe()
```

Adding NATS is one more line, and it is a driver — not a second handler, not a second chain, not a
second definition of any method.

```
$ go run ./cmd/catalogd
time=2026-08-30T08:11:10.558+10:00 level=INFO msg=serving
  connect=:8080/catalog.v1.CatalogService/*
  rest=:8080/v1/catalog/providers
  bus=memory
  procedures="[/catalog.v1.CatalogService/CreateProvider ... /catalog.v1.CatalogService/SyncProvider]"
```

## Emitting OpenAPI

The partner-facing document comes from the same descriptors the REST binding routes on, so a
method that did not declare the road — or that is marked `internal` — is absent from both:

```go
func OpenAPI(servers ...string) ([]byte, error) {
	return openapi.FromFiles(
		[]protoreflect.FileDescriptor{catalogv1.File_catalog_v1_catalog_proto},
		openapi.Options{
			Title:       "Catalog API",
			Version:     "1.0.0",
			Description: "Providers registered to a tenant.",
			Servers:     servers,
		})
}
```

`examples/catalog/openapi.json` is committed and has a drift gate of its own
(`TestTheEmittedOpenAPIMatchesWhatPartnersCall`): a path that changes should be visible in a diff
before it is visible in someone else's logs.

## Shutting down

```go
func (s *Service) Close() error {
	for _, srv := range s.busses {
		if err := srv.Stop(); err != nil {
			return err
		}
	}
	s.busses = nil
	return nil
}
```

`engine.Server.Stop` unsubscribes and drains. A driver that holds a connection implements
`interchange.Closer`, and the engine closes it on shutdown; the HTTP roads are `http.Server`'s
business, not Interchange's.

Next: [04 Calling a service](04-calling-a-service.md), or
[05 Cross-cutting concerns](05-cross-cutting.md) for the modules the chain above installs.
