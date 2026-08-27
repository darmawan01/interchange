# 05 — One call, two transports

The claim this proposal has to earn: **a browser and a peer service invoke the same declared method,
over roads that share nothing at the network layer, and hit the same handler through the same
middleware.**

The convergence point is the envelope. Above it, two completely different network protocols. Below
it, one code path — which is why authorization cannot be enforced on one road and forgotten on the
other.

## The front end

```ts
import { createClient } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { CatalogService } from './gen/platform/catalog/v1/catalog_pb'

const transport = createConnectTransport({
  baseUrl: '/api',
  interceptors: [next => async req => {
    req.header.set('Authorization', `Bearer ${session}`)
    return next(req)
  }],
})

const catalog = createClient(CatalogService, transport)

// Fully typed. The response type came from the same .proto the server
// implements -- there is no hand-written interface to fall out of date.
const { providers } = await catalog.listProviders({ pageSize: 50 })
```

## The peer service

```go
catalog := catalogv1bus.NewCatalogServiceBusClient(bus,
    transport.WithWorkloadIdentity(creds),
)

// Same method name, same request type, same response type. The only
// difference from the browser call above is which constructor was used.
resp, err := catalog.ListProviders(ctx, &catalogv1.ListProvidersRequest{
    PageSize: 50,
})
```

## The server, once

```go
// The handler is an ordinary struct implementing the generated interface.
// It has no idea a message bus exists.
type catalogServer struct{ store Store }

func (s *catalogServer) ListProviders(ctx context.Context,
    req *catalogv1.ListProvidersRequest) (*catalogv1.ListProvidersResponse, error) {
    // ... business logic ...
}

// Both bindings register the SAME implementation with the SAME chain.
chain := []Interceptor{Reason(), Metrics(m), Auth(authCfg), Validate(v)}
httpBinding.Register(catalogv1.CatalogServiceDesc, srv, chain...)
busBinding.Register(catalogv1.CatalogServiceDesc, srv, chain...)
```

## What ships to the front end

The **generated SDK package** — not the proto, and not a hand-written client library. The `.proto`
tree never leaves the API repo; the front end depends on a versioned artifact built from it,
exactly the way it depends on any other package.

| Model | How it works | Good when | Cost |
| --- | --- | --- | --- |
| Workspace package | Generated TS lands in `gen/ts` and the front end imports it as a monorepo workspace | API and front end share a repo | Couples the two repos; no versioning story if they ever split |
| **Published package** *(recommended default)* | CI runs generate and publishes a versioned npm package on merge; the front end pins a version | Separate repos, or more than one consumer | A publish pipeline and a version-bump convention |
| Registry-hosted SDK | A schema registry hosts generated packages; the front end installs one directly | You want zero pipeline to maintain | Ties you to that registry; private modules generally need a paid plan |

**What is in the package**

- Message types — the shape of every request and response.
- A service descriptor per service, which is what `createClient` consumes.
- Nothing else. No fetch wrappers, no per-endpoint helper functions, no re-declared enums.

> **The thing to resist.** There is always pressure to add a hand-written wrapper layer on top —
> nicer method names, a bit of caching, a mapped error type. **That layer is a second contract,
> maintained by hand, and it will drift from the first one.** It is the exact failure this proposal
> exists to remove, reintroduced one convenience function at a time. If a call site wants
> ergonomics, put them in a hook or a query wrapper in the front-end repo, not in the generated
> package.

## Does the front end ever touch the bus?

No, and it should not want to. The browser gets the RPC binding, plus WebSocket if it needs bidi.
When a browser action has to reach something that only lives on the bus, an edge service accepts
the RPC call and re-emits it — and because it is **the same envelope on both sides**, that bridge is
a few lines rather than a translation layer.

> **Why browsers not speaking NATS is fine.** The contract layer absorbs the difference. A browser
> cannot open a NATS connection and never will — but it does not need to, because it is calling
> `ListProviders`, not calling NATS. The transport is chosen by whoever wires the client, and the
> method signature is identical either way. That is the difference between a transport-agnostic
> **contract** and a transport-agnostic **network**.
