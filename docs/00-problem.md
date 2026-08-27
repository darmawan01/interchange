# 00 — The three-contract problem

One operation. Three consumers. Today that usually means three separate definitions of the same
thing, kept in step by discipline alone.

## Today

```
   Browser / SPA          Peer service          Async consumer
   front end team         backend team          data / worker team
         │                      │                      │
         ▼                      ▼                      ▼
   ┌─────────────┐        ┌─────────────┐        ┌──────────────┐
   │ OpenAPI spec│◀ drift▶│ .proto      │◀ drift▶│ a JSON shape │
   │ hand-written│        │ generated   │        │ in a wiki    │
   │ TS types    │        │ stubs       │        │ versioned by │
   │ camelCase   │        │ snake_case  │        │ nobody       │
   └─────────────┘        └─────────────┘        └──────────────┘
         │ HTTP                 │ gRPC                 │ pub/sub
         ▼                      ▼                      ▼
   ══════════════════════════════════════════════════════════════
                        Catalog service
     three entry points, three hand-maintained mappings,
                    one implementation
```

`ListProviders` exists three times — once as an OpenAPI path, once as a proto RPC, once as an
informally agreed JSON payload. Nothing mechanical connects them, so a renamed field is caught by a
**runtime failure rather than a build failure**.

## Where the duplication fails

The cost is not the duplication itself, it is where it fails. Every one of these is a real,
recurring incident class:

- A field is renamed in the proto. The TypeScript types were hand-written, so the front end
  compiles fine and breaks in production.
- The REST surface emits `camelCase`, the message bus payload emits `snake_case`, and a shared
  parser is written to accept both.
- A new required field lands. Three teams add validation for it, in three languages, with three
  different error messages.
- Authorization is enforced in the HTTP handler. The same operation reached over the message bus
  skips it entirely, because nobody remembered there were two doors.
- An async consumer is added six months later. There is no contract to generate from, so it parses
  the JSON by hand.

## Why "generate bus stubs from the proto" is not the whole answer

The obvious fix is to generate message-bus subscribers and clients from the same proto service
definitions the HTTP side already uses. That instinct is correct and it is where this design
starts — but a minimal binding of that kind gets the *contract* right and leaves seven gaps, each
of which turns into a per-service workaround within a quarter.

| Gap in a minimal bus binding | What it costs | Handled by |
| --- | --- | --- |
| No metadata channel | Credentials, tenant hint and trace context have nowhere to ride, so they get smuggled into the request message — where they become part of the public contract forever | `metadata` map · [§03](03-envelope.md) |
| No deadlines | A slow handler is abandoned rather than cancelled; the work keeps running after the caller has given up | `deadline_unix_ms` · [§03](03-envelope.md) |
| Untyped errors | A bare error string means clients pattern-match English that gets reworded next sprint | code + closed reason enum · [§06](06-crosscutting.md) |
| One durability tier | Fire-and-forget pub/sub loses in-flight work on a restart; making everything durable makes request/reply expensive | core vs stream split · [§04](04-bindings.md) |
| No streaming | Any long or unbounded result needs a bespoke protocol invented per service | `Frame` + sequence · [§03](03-envelope.md) |
| No authorization | The bus door is unguarded while the HTTP door is not — the worst possible asymmetry, and invisible until someone finds it | annotation + one chain · [§06](06-crosscutting.md) |
| No browser story | The front end still gets a separate, hand-maintained surface, so the original problem survives untouched | RPC binding · [§04](04-bindings.md) |

**Read that table as the requirements list.** Everything in the sections that follow exists to close
one of those rows, and the envelope in [§03](03-envelope.md) is where five of the seven land.

---

> **The goal of this proposal.** Make the transport a deployment decision rather than an API
> decision. One declared method, one generated dispatch, one enforcement path — whether the caller
> is a browser over HTTP, a peer over NATS, or a device over MQTT.
