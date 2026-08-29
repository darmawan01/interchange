# ADR-0053 — The browser does not touch the bus

**Status:** Revisit when a browser-capable broker protocol is a deployment requirement rather than
a convenience — the bridge is cheaper than the exposure until then
**Date:** 2026-08-30 · **Phase:** 0

## Context

"One contract, every road" invites an obvious next question: if a peer service can call
`ListProviders` over NATS, why can't the browser? Several brokers offer a WebSocket transport, and
a browser NATS client exists. The apparent prize is one fewer hop.

The prize is not worth what it costs. Reaching the broker from a browser means the broker's
authentication is the browser's authentication, the broker's subject space is reachable from
untrusted code, and the broker's connection limits are sized by however many tabs are open. It also
makes the front end's build depend on a broker client library and its version.

## Decision

The browser gets the RPC binding — Connect over HTTP — plus the WebSocket binding when it needs
bidirectional or server-pushed traffic. Not the bus.

When a browser action must reach something that only lives on the bus, an edge service accepts the
RPC call and re-emits it on the bus. Because it is the same envelope on both sides — same
`procedure` string, same metadata map, same payload bytes — that bridge is a few lines of forwarding
rather than a translation layer, and it is a natural place to put the authorization the broker
would otherwise have had to do.

## Consequences

The distinction this makes explicit is worth stating plainly, because it is the one people get
wrong: a transport-agnostic **contract** is not a transport-agnostic **network**. A browser cannot
open a NATS connection and never will. It does not need to, because it is calling `ListProviders`,
not calling NATS. The transport is chosen by whoever wires the client; the method signature is
identical either way, and so is the interceptor chain the call lands in (ADR-0015).

What it buys: the broker stays inside the deployment, with one class of authenticated peer on it.
The front end's dependency list is `@connectrpc/connect` and `@connectrpc/connect-web` — no broker
client, no broker version to track. Note that the browser's two roads are not the two `ix lint`
treats as public: `publicRoads` is `rpc` and `rest`, so an `(internal)` method declared on `ws` is
served to a socket and not flagged — see ADR-0050, which carries that caveat.

What it costs: a hop, and a component. An action that is genuinely bus-only needs an edge service
somebody writes, deploys and monitors — and if there is no such service yet, "expose it on RPC as
well" is the tempting shortcut, which quietly turns an internal method into a public one. The
bridge is also where the credential changes hands: the browser presents a session, the re-emitted
call presents a workload identity, and that mapping is application code the framework does not
write.

The WebSocket road takes some of the pressure off — it is a real interchange road with the full
envelope, so server-pushed and bidirectional traffic does not need the bus — but it is one channel
per socket with the procedure in the envelope (ADR-0028), and its `Capabilities` say
`CompetingGroup: false` and no acknowledgement. It is not a broker and does not pretend to be.

## Alternatives

**Browser → broker over the broker's own WebSocket transport.** Rejected on exposure and coupling:
authenticating untrusted clients against the broker, and pinning a broker client library into the
front-end build.

**A generic HTTP-to-bus gateway shipped with the framework.** A gateway that forwards any procedure
is an authorization decision made once, generically, for every method — which is precisely the
decision that should be made per method, by someone who knows what the method does. An edge service
that forwards the three procedures a product actually needs is smaller and safer than a
configurable one that forwards everything.

**Let the browser publish fire-and-forget events onto the bus and nothing else.** Narrower, and
still requires broker credentials in the browser. The exposure is the problem, not the verb.

## Evidence

- `docs/05-one-call-two-transports.md` §"Does the front end ever touch the bus?" — including the
  contract/network distinction this record restates.
- `examples/catalog/web/src/main.ts` — `createConnectTransport` and `createClient`; the front end's
  only network dependency is Connect over HTTP.
- `driver/ws/README.md` and `driver/ws/server.go` — the browser's bidi road: one socket, one
  `engine.Server` per connection, the procedure carried in the envelope.
- `binding/rpc/rpc.go` — the browser's request/response road, mounted on the same registry as every
  other binding.
- `BUILD-PLAN.md` §Deliberately out of scope — "The browser touching the bus. It gets RPC, plus
  WebSocket for bidi."

See ADR-0007 (the envelope shape is not pluggable — the reason the bridge is a few lines),
ADR-0028 (WebSocket has one channel) and ADR-0050 (`(internal)` means public bindings skip it).
