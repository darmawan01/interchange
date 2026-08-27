# 01 — The proposal

One service definition. Generated adapters fan it out onto every road it needs to travel, and all
of them converge on the same handler through the same middleware.

## The fan-out, and the convergence

```
                  ┌────────────────────────────────┐
                  │  catalog/v1/catalog.proto      │  ← you write this
                  │  the only place the contract   │
                  │  exists                        │
                  └───────────────┬────────────────┘
                                  ▼
            buf generate · standard plugins + 3 local plugins
                                  │
        ┌──────────────┬──────────┴──────────┬──────────────┐
        ▼              ▼                     ▼              ▼
  ┌───────────┐  ┌───────────┐        ┌───────────┐  ┌───────────┐
  │RPC binding│  │REST bind. │        │NATS bind. │  │ MQTT / WS │
  │POST /pkg. │  │GET /v1/   │        │rpc.pkg.   │  │topic or   │
  │Svc/Method │  │providers  │        │Svc.Method │  │socket fr. │
  │browsers,  │  │partners,  │        │service to │  │devices,   │
  │CLIs, SDKs │  │webhooks   │        │service    │  │live UI    │
  └─────┬─────┘  └─────┬─────┘        └─────┬─────┘  └─────┬─────┘
        └──────────────┴──────────┬──────────┴──────────────┘
                                  ▼
        ┌─────────────────────────────────────────────────┐
        │        dispatch · interceptor chain             │
        │ authn · authz · validation · errors · telemetry │
        │                 written once                    │
        └─────────────────────┬───────────────────────────┘
                              ▼
                  ┌────────────────────────────────┐
                  │  CatalogServiceHandler         │  ← and this
                  │  your implementation,          │
                  │  unaware of transport          │
                  └────────────────────────────────┘
```

The fan-out is generated and **the convergence is the point**: adding MQTT does not add an
authorization path, a validation path, or a second definition of `ListProviders`. The two
highlighted boxes are the only files a developer writes.

## The four layers

Everything rests on drawing the line between what a transport can carry unchanged and what has to
be re-bound per transport.

| Layer | What it is | Portable? |
| --- | --- | --- |
| **contract** | `.proto`: messages, services, annotations | portable |
| **dispatch** | procedure → handler, plus the interceptor chain | portable |
| binding | envelope ↔ native frames, per transport | bound |
| transport | HTTP · NATS · MQTT · WebSocket | bound |

### Portable — write once

- Messages and their binary/JSON encodings
- Descriptors, and every custom option riding on them
- The generated server interface
- The interceptor chain — procedure name in, decision out
- The error model: code + reason
- Field validation rules

### Bound — one adapter each

- Address shape: URL path, subject, or topic
- Metadata: headers, user properties, envelope map
- Status: HTTP code vs an explicit field
- Correlation: free in HTTP, explicit elsewhere
- Deadlines and cancellation
- Streaming semantics

---

> **The rule that keeps this honest.** A binding adapter may not import a single concrete message
> type. If the NATS adapter imports `catalogv1`, it has stopped being an adapter and become a
> second implementation of the API — which is the exact failure this proposal exists to prevent.
> Adapters see procedure strings, bytes, and metadata. Generated dispatch does the rest.
