# 04 — Calling a service

Five callers, one contract. Every one of them attaches its credential as **metadata**, and the
chain cannot tell them apart — which is exactly why a check cannot be enforced on one road and
forgotten on another.

| Caller | Client | Road |
| --- | --- | --- |
| A browser | `createClient(CatalogService, transport)` | Connect over HTTP |
| A Go peer | `catalogv1connect.NewCatalogServiceClient(...)` | Connect over HTTP |
| A Go peer on the bus | `catalogv1bus.NewCatalogServiceBusClient(...)` | NATS · MQTT · WS · in-process |
| Anything holding a procedure string | `rpc.Client` / `engine.Client` | either |
| A terminal | the generated Cobra tree | either, by flag |

## The browser: TypeScript over Connect

[`examples/catalog/web/src/main.ts`](../../examples/catalog/web/src/main.ts) is the whole client.
The two lines that matter are the import:

```ts
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  CatalogService,
  type Provider,
  ProviderStatus,
} from "../../gen/ts/catalog/v1/catalog_pb.js";
```

`CatalogService` and the request/response types come from `gen/ts`, generated from the same
`catalog.proto` the Go server implements. **Rename a field in the contract and `npm run typecheck`
fails here** — which is the whole point, and the reason there is no hand-written client interface
in that directory. The acceptance test
`TestAFrontEndImportsItsTypesFromGeneratedOutput` asserts it.

### Attaching a credential

```ts
const transport = createConnectTransport({
  baseUrl: "/",
  interceptors: [
    // The credential is metadata, and metadata is what the chain sees. The
    // peer service on the bus sets the same key on its envelope; the authz
    // interceptor cannot tell the two apart, which is why the check cannot be
    // enforced on one road and forgotten on the other.
    (next) => async (req) => {
      req.header.set("Authorization", `Bearer ${session()}`);
      return next(req);
    },
  ],
});

const catalog = createClient(CatalogService, transport);
```

### Calling, and reading an error

```ts
export async function load(tenantId: string): Promise<void> {
  try {
    // Fully typed, both ways. tenantId is a declared field; the response's
    // `providers` is Provider[] because the .proto said so.
    const { providers } = await catalog.listProviders({ tenantId });
    render(providers);
  } catch (err) {
    // The reason travels in the Ix-Reason header on this road and in the
    // envelope on the bus, and it is the same string either way -- so a client
    // branches once.
    say(err instanceof Error ? err.message : String(err));
  }
}
```

connect-web surfaces the message; a production app reads `Ix-Reason` off the response and switches
on the reason, not on the message text — see [05](05-cross-cutting.md#one-error-four-surfaces)
for why the reason is the branch point.

> **There is no `connectrpc/es` plugin in the template.** Connect-ES v2 removed its code generator:
> protobuf-es v2 emits the service descriptor into `catalog_pb.ts` and
> `createClient(CatalogService, transport)` consumes it directly.
> `@connectrpc/protoc-gen-connect-es` stops at 1.7.0 and is protobuf-es v1 era; using it would mean
> pinning the whole TypeScript surface a major version back.

**The browser does not touch the bus** ([ADR-0053](../adr/0053-the-browser-does-not-touch-the-bus.md)).
A browser that needs a bus-shaped road gets WebSocket, below.

## Go over Connect: the generated Connect client

```go
client := catalogv1connect.NewCatalogServiceClient(http.DefaultClient, "http://localhost:8080")

req := connect.NewRequest(&catalogv1.ListProvidersRequest{TenantId: "acme"})
req.Header().Set("authorization", "Bearer "+token)

resp, err := client.ListProviders(ctx, req)
// resp.Msg is *catalogv1.ListProvidersResponse
```

This is stock connect-go — Interchange adds nothing to it. The acceptance suite drives the
**generated** Connect client rather than a hand-rolled HTTP call, precisely so a change to the
contract that breaks a browser also breaks the test.

## Go over the bus: the generated bus client

`protoc-gen-bus` emits a bus client **only** for services with a method on `TRANSPORT_BUS`,
`_MQTT` or `_WS`, and only those methods get client methods — a generated call to a procedure
nothing subscribes to is a timeout wearing a confident signature.

```go
// One connection, one engine client, one typed client over it.
conn, err := nats.Connect("nats://localhost:4222")
drv, err := natsdriver.New(conn)

cli, err := engine.NewClient(ctx, drv,
	engine.WithStaticMetadata(interchange.Metadata{"authorization": "Bearer " + token}),
	engine.WithTimeout(10*time.Second))
defer cli.Close()

catalog := catalogv1bus.NewCatalogServiceBusClient(cli)
resp, err := catalog.ListProviders(ctx, &catalogv1.ListProvidersRequest{TenantId: "acme"})
```

### The explicit-deadline variant, and why it exists

Every method comes in two forms. From the generated file's own doc comment:

> A plain method blocks until the reply arrives or the context is done, and that is the trap: the
> signature looks local and the call is not. So every method also has a `Within` form that takes
> the deadline as an argument, and `WithCatalogServiceTimeout` sets a default for the plain form.
> Pick one deliberately: the engine's own client timeout is the last line of defence, not the
> first.

```go
// Blocks. Bounded by the client's default timeout if one was configured,
// otherwise by ctx alone.
resp, err := catalog.ListProviders(ctx, req)

// The network deadline in the signature, where a caller cannot miss it.
resp, err := catalog.ListProvidersWithin(ctx, 2*time.Second, req)

// A default for the plain form.
catalog := catalogv1bus.NewCatalogServiceBusClient(cli,
	catalogv1bus.WithCatalogServiceTimeout(5*time.Second))
```

[ADR-0037](../adr/0037-bus-clients-carry-an-explicit-deadline.md) records the decision to generate
both rather than pick one.

### Metadata and credentials on the bus

`engine.ClientOption` covers both the static and the per-call case:

```go
engine.WithStaticMetadata(interchange.Metadata{"authorization": "Bearer " + token})
engine.WithMetadata(func(ctx context.Context) (interchange.Metadata, error) {
	tok, err := tokens.Fresh(ctx)          // rotate, mint, or read from ctx
	return interchange.Metadata{"authorization": "Bearer " + tok}, err
})
engine.WithTimeout(10 * time.Second)       // the last line of defence
engine.WithCodec(interchange.CodecJSON)    // default is proto
```

`interchange.Metadata` is a `map[string]string` that **lower-cases every key** on `Set` and `Get`,
so `Authorization` and `authorization` are the same key on every road. That is what makes an HTTP
header and a bus envelope field indistinguishable to the authz interceptor.

### The other bus transports

The client half is the same three lines with a different driver:

```go
// MQTT 5
drv, err := mqtt.Connect(ctx, mqtt.Config{URL: "tcp://broker:1883", QoS: 1, ClientID: "worker"})

// In-process — a real driver, not a mock
bus := memory.New()
drv := bus.Driver("worker")
```

#### WebSocket

A browser cannot set an `Authorization` header on an upgrade, so the driver carries a **handshake
frame**: the first message on the socket is a flat JSON object of metadata, and the engine merges
it beneath each call's own metadata so a per-call value always wins over a per-connection one.

```go
d, err := ws.Dial(ctx, "wss://example.com/ws",
	ws.WithHandshakeMetadata(interchange.Metadata{"authorization": "Bearer " + tok}))
if err != nil { ... }
defer d.Close()

cli, err := engine.NewClient(ctx, d, engine.WithTimeout(10*time.Second))
```

There is no client wrapper here and there should not be: the driver implements
`interchange.Watcher`, so `engine.NewClient` fails every pending call the moment the socket dies
rather than leaving it to wait out its deadline.

Server-side, `ws.WithRequestMetadata(func(*http.Request) interchange.Metadata)` reads a credential
off the upgrade request itself — a query parameter or a cookie — for a client that cannot send a
handshake frame.

## The dynamic client

Both `*rpc.Client` and `*engine.Client` expose the same one method:

```go
Invoke(ctx context.Context, procedure string, in, out proto.Message) error
```

No generated code required — you supply the procedure string and two messages. That is what lets
the generated CLI be transport-agnostic, and what a gateway or a test harness reaches for.

```go
client := rpc.NewClient(http.DefaultClient, "http://localhost:8080",
	rpc.WithStaticMetadata(interchange.Metadata{"authorization": "Bearer " + token}))

out := &catalogv1.ListProvidersResponse{}
err := client.Invoke(ctx, catalogv1bus.CatalogServiceListProvidersProcedure,
	&catalogv1.ListProvidersRequest{TenantId: "acme"}, out)
```

`rpc.ClientOption`:

| Option | What |
| --- | --- |
| `rpc.WithStaticMetadata(md)` | headers on every call |
| `rpc.WithMetadata(func(ctx) (Metadata, error))` | headers computed per call |
| `rpc.WithClientCodec(name)` | `interchange.CodecProto` (default) or `CodecJSON` |
| `rpc.WithClientOptions(...)` | connect-go client options, passed through |

`rpc.Client` also has `InvokeMethod(ctx, md *interchange.MethodDesc, in, out, header)` for a caller
that already holds the descriptor and wants per-call headers without an option closure.

The procedure string is the **Connect** procedure string, verbatim, on every road
([ADR-0009](../adr/0009-the-connect-procedure-string.md)) — so the same string appears in the URL,
in the bus subject, in the authz check, in the metrics label and in the trace span. The acceptance
test `TestSameProcedureStringInAuthzCheckMetricsLabelsAndTraceSpanOnBothRoads` asserts it.

Generated procedure constants save you from typing it:

```go
const (
	CatalogServiceCreateProviderProcedure = "/catalog.v1.CatalogService/CreateProvider"
	CatalogServiceGetProviderProcedure    = "/catalog.v1.CatalogService/GetProvider"
	CatalogServiceListProvidersProcedure  = "/catalog.v1.CatalogService/ListProviders"
	CatalogServiceReconcileProcedure      = "/catalog.v1.CatalogService/Reconcile"
	CatalogServiceSyncProviderProcedure   = "/catalog.v1.CatalogService/SyncProvider"
)
```

## The generated CLI

`protoc-gen-cli` emits a Cobra tree from `(interchange.cli.v1.command)`. Mounting it is one call:

```go
catalogv1cli.RegisterCatalogServiceCommands(root, invoker)
```

`invoker` is a `clisupport.Invoker` — the same `Invoke(ctx, procedure, in, out)` method above, so
**`*rpc.Client` and `*engine.Client` satisfy it unchanged**. A CLI is a caller, and a caller does
not pick the road:

```go
// cmd/catalogctl/main.go, abridged
catalogv1cli.RegisterCatalogServiceCommands(root, lazyInvoker(func() (clisupport.Invoker, error) {
	md := interchange.Metadata{}
	if token != "" {
		md.Set("authorization", "Bearer "+token)
	}
	if apiKey != "" {
		md.Set("x-api-key", apiKey)
	}
	if natsURL == "" {
		return rpc.NewClient(http.DefaultClient, addr, rpc.WithStaticMetadata(md)), nil
	}
	conn, err := nats.Connect(natsURL)
	if err != nil {
		return nil, err
	}
	drv, err := natsdriver.New(conn)
	if err != nil {
		return nil, err
	}
	return engine.NewClient(context.Background(), drv,
		engine.WithStaticMetadata(md), engine.WithTimeout(30*time.Second))
}))
```

`--nats` is the only difference between the two roads. The client is built lazily because the flags
it reads are not parsed until cobra runs the leaf command.

### What the tree looks like

```
$ catalogctl --help
Drive catalog.v1.CatalogService from a terminal

Available Commands:
  catalog     catalog commands
  coverage    Report which RPCs this command tree fronts

Flags:
      --addr string      Connect endpoint (default "http://localhost:8080")
      --api-key string   workload API key
      --nats string      call over NATS instead of HTTP
      --token string     bearer token
```

```
$ catalogctl catalog providers --tenant-id acme --token reader-token
{
  "providers": [
    {
      "providerId": "prov_00000001",
      "tenantId": "acme",
      "displayName": "stripe",
      "status": "PROVIDER_STATUS_ACTIVE",
      "createdAt": "2026-08-29T22:11:10.557449Z"
    }
  ]
}
```

Errors carry the code and the reason, not a stack trace:

```
$ catalogctl catalog provider create --tenant-id acme --display-name checkout --token reader-token
catalogctl: permission_denied: /catalog.v1.CatalogService/CreateProvider: user:reader requires providers.create (PERMISSION_DENIED)
exit status 1
```

### Flags, positionals, and the escape hatch

- one flag per **scalar top-level** request field, kebab-cased (`tenant_id` → `--tenant-id`);
- positional arguments from the annotation's `args`, which must name scalar fields;
- `--request-json` for anything no single token can carry — repeated, map, or message fields.
  Flags applied afterwards override it, and only a flag the user actually typed touches the request.

The response is printed as indented JSON through `encoding/json` — protojson deliberately varies
its own whitespace, which is no way to feed `jq`.

### Coverage

```
$ catalogctl coverage
catalog.v1.CatalogService: 4/5 covered, 1 skipped, 0 unannotated
```

`Reconcile` is the skipped one: it declares `{skip: true}` because it is cross-tenant platform
maintenance and does not belong in a general-purpose CLI. `unannotated` is the number that should
worry you — and `require_annotation=true` in `interchange.yaml` turns it into a build failure.

## `ix dev call` — calling without writing a client at all

```
$ ix dev call CatalogService.ListProviders '{"tenant_id":"acme"}'
```

Stub responses only: this exercises the contract, not your handlers. It is in
[01](01-getting-started.md#ix-dev--exercise-the-contract-with-no-infrastructure).

Next: [05 Cross-cutting concerns](05-cross-cutting.md).
