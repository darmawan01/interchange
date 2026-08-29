# driver/mqtt — the MQTT 5 transport driver

An `interchange.Driver` over MQTT 5: six methods, a `Capabilities` value, and no knowledge of the
envelope beyond "it is bytes". Correlation, deadlines, chunking, replay suppression and metadata
fallback stay in `engine/`.

## Why version 5 only

MQTT 3.1.1 has no user properties, no response topic and no correlation data. A 3.1.1 driver has to
reinvent all three inside the payload, which means metadata that no other client on the broker can
read, reply routing that a `mosquitto_sub` cannot follow, and a second definition of correlation
that has to agree with the engine's. Version 5 provides them natively, which is the whole reason
this driver can declare `NativeHeaders` and `NativeReply` and still fit on one page. Recorded in
`docs/08-decisions.md`.

This is also why the client library is `github.com/eclipse/paho.golang/paho` and not
`paho.mqtt.golang`: the latter is 3.1.1 only.

## Topic grammar

| | |
| --- | --- |
| procedure `/pkg.Svc/Method` | `<prefix>/rpc/pkg.Svc/Method` |
| service `pkg.Svc` | `<prefix>/rpc/pkg.Svc/+` |
| reply address | `<prefix>/reply/<client-id>` |
| competing group `g` | `$share/g/<pattern>` |

The prefix defaults to `ix` and is the tenancy boundary: two deployments on one broker set
different prefixes and never see each other. The method sits in its own topic level so a service
wildcard is a single `+` rather than a `#`, which keeps a subscription from catching the reply
topics or anything else under the prefix.

- **Metadata** travels as MQTT 5 user properties, one per key.
- **Reply routing** is the Response Topic property, set on every publish to this client's inbox.
  The server answers through `Inbound.Reply`, which publishes to the topic the request carried and
  echoes its Correlation Data back.
- **Correlation** is the Correlation Data property, taken from `interchange.MetaCorrelationID` in
  the header map. It is **moved there, not copied**: the id is already inside the envelope for a
  peer that speaks interchange, and Correlation Data is where a peer that speaks only MQTT 5 looks
  for it, so a third copy would buy nothing and leak an `ix-` key into handler metadata.
- **Groups**: a non-empty `group` becomes `$share/<group>/<pattern>`, MQTT's competing consumers.

## Acknowledgement

The client runs with `EnableManualAcknowledgment`, and the PUBACK is `Inbound.Done` -- not
delivery. A request is acknowledged when the engine has handled it and sent its reply; every frame
of a chunked request stays unacknowledged until the whole message has been handled. Acking on
arrival would tell the broker a message was handled before the handler started, and replay
suppression can dedupe a redelivery but cannot conjure one.

`Done(err)` leaves the packet unacknowledged, because MQTT has no negative acknowledgement -- a
PUBACK reason code does not make a broker retry -- so silence is the only way to say "not handled".
Two consequences worth knowing: paho flushes acks in receipt order, so one unhandled message holds
up the acks behind it on that connection; and a redelivery arrives only when the broker's inflight
timer fires or the session resumes.

Two packets are acknowledged by the driver rather than by the engine, both noted in `route`: one
nothing subscribed to (there is no handler to finish, and redelivering it would loop), and a reply
on this client's own inbox (the engine matches a reply to its pending call synchronously, and it
calls `Done` only for the reassembled kind -- see the seam note below).

## Capabilities

| Field | Value |
| --- | --- |
| `Name` | `mqtt` |
| `Transport` | `TRANSPORT_MQTT` |
| `NativeHeaders` | true — user properties |
| `NativeReply` | true — response topic |
| `CompetingGroup` | true — `$share` |
| `OrderedPerKey` | false |
| `MaxPayload` | the CONNACK's Maximum Packet Size less 1 KiB of headroom, else 256 KiB |
| `AtLeastOnce` | true — QoS 1 redelivers, so the engine's replay suppression is what stops a retried request from running the handler twice |

QoS is 1 or 2, default 1. QoS 0 is rejected: at-most-once turns a lost chunk of a chunked message
into a call that never completes and never reports why, and it removes the redelivery that the
`sequence` field exists to absorb.

## Wiring

```go
import _ "github.com/darmawan01/interchange/driver/mqtt" // registers "mqtt"

drv, err := interchange.NewDriver("mqtt", map[string]string{"url": "tcp://broker:1883"})
```

or directly, when you want the config in Go:

```go
drv, err := mqtt.Connect(ctx, mqtt.Config{URL: "tcp://broker:1883", Prefix: "org", QoS: 1})
```

Then `engine.NewServer(drv, reg)` to serve and `engine.NewClient(ctx, drv)` to call. The driver
implements `interchange.Closer`, so shutdown closes the connection.

### Config keys

| Key | Default | Meaning |
| --- | --- | --- |
| `url` | `tcp://127.0.0.1:1883` | broker address; the scheme is ignored, the host:port is used |
| `prefix` | `ix` | first topic level for every topic this driver builds |
| `qos` | `1` | 1 or 2 |
| `client_id` | random `ix-<hex>` | also the last level of the reply topic, so it must be unique per process |
| `username`, `password` | none | sent in CONNECT |
| `max_payload` | negotiated | overrides the ceiling above which the engine chunks |

## Tests

`go test ./...` starts `github.com/mochi-mqtt/server/v2` in-process on a free localhost port and
runs `drivertest.Run` against it — no docker, no external broker. Beyond the conformance suite:
redelivery of the exact request bytes is answered from the dedupe cache without running the handler
twice; a small `MaxPayload` makes one call go out as several packets and come back whole; two
servers sharing a group split 20 calls instead of each answering all of them; a broker hook asserts
Correlation Data and Response Topic on the wire; and every test that exercises the broker asserts
it has no message left inflight, which is what catches a forgotten PUBACK.

The broker is shut down off the test goroutine: mochi 2.7.9 can deadlock in `Server.Close` when a
client is connecting or disconnecting at the same moment -- `Clients.GetByListener` holds the read
lock and calls `Len`, which takes it again, behind a writer waiting in `Clients.Delete`. It is an
upstream bug in the test harness, not in the driver or the engine, and v2.7.9 is the latest release.

## Seam findings

Two findings from the first pass, both since fixed in core; one minor note and one still open.

1. **Fixed.** `drivertest` could not be run by a driver that was not `TRANSPORT_BUS`: the fixture
   declared RPC/REST/BUS only and `drivertest.Run` passed no `engine.Expose`, so an honest
   `TRANSPORT_MQTT` driver failed `Start` with *"no procedure is exposed on TRANSPORT_MQTT"*. The
   suite now passes `engine.Expose(pair.Server.Caps().Transport)` and the fixture declares every
   road; the shim this package carried is gone and the driver runs the suite as itself.

2. **Fixed.** The correlation id was unreachable from `Publish` -- inside the opaque body, which a
   driver may not parse -- so MQTT 5 Correlation Data could not be set on a request. The engine now
   surfaces it as `interchange.MetaCorrelationID` in the header map, and `TestCorrelationData`
   asserts it reaches the wire and comes back on the reply.

3. **Minor.** `Inbound.Reply` and `Unsubscribe` carry no context, so a driver whose transport can
   block -- a QoS 1 publish waits for its PUBACK -- has to invent its own deadline or risk hanging
   the engine's shutdown behind a silent broker. This one uses 30 seconds; a context parameter on
   `Reply` would let the engine own that the way it owns the fallback path's.

4. **Open.** `engine.Client.onReply` calls `Inbound.Done` only on the reassembly path. A reply that
   arrives whole -- the common case -- and every early return (malformed, not a response, unknown
   correlation id) leave it uncalled. On a transport that wires `Done` to an acknowledgement, that
   is a QoS 1 packet nobody ever PUBACKs: it stays inflight, is redelivered on the broker's timer,
   and, because acks flush in receipt order, it stalls the ack of every message behind it on that
   connection. This driver works around it by acknowledging anything delivered on its own inbox
   once `fn` returns, which is honest for a reply but is the engine's guarantee to make, not the
   driver's. The fix is one `ackAll` in `onReply` on the whole-response path -- and the WebSocket
   and NATS drivers will want it before they wire acknowledgement to anything.
