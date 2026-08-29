# ADR-0026 — MQTT 5 only

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 5

## Context

MQTT 3.1.1 is still the version most deployed brokers and device SDKs speak, so supporting both
looks like the generous choice. It is not, once you write down what a 3.1.1 driver would have to
do.

3.1.1 has no user properties, no response topic and no correlation data. All three are things this
transport layer needs on every message: metadata for the chain, a return address for the reply, and
a correlation id so a reply finds its call. A 3.1.1 driver has to reinvent them **inside the
payload** — which means metadata no other client on the broker can read, reply routing a
`mosquitto_sub` cannot follow, and a second definition of correlation that has to agree with the
engine's and will eventually not. It also means the driver parses the body, which is the one thing
an adapter may not do (ADR-0024).

MQTT 5 provides all three natively. That is the entire reason the driver can declare
`NativeHeaders: true` and `NativeReply: true` and still fit on one page.

## Decision

The driver speaks MQTT 5 and only MQTT 5. Metadata rides as user properties, one per key; reply
routing is the Response Topic property; correlation is the Correlation Data property, **moved**
from `interchange.MetaCorrelationID` in the header map rather than copied — the id is already
inside the envelope for a peer that speaks Interchange, and Correlation Data is where a peer that
speaks only MQTT 5 looks, so a third copy would buy nothing and leak an `ix-` key into handler
metadata.

The client library is `github.com/eclipse/paho.golang/paho` rather than `paho.mqtt.golang`, because
the latter is 3.1.1 only.

**The driver also refuses QoS 0.** Only 1 and 2 are accepted, at construction and in config
parsing, with an error naming the value. At-most-once silently loses one chunk of a chunked message
— turning a call into one that never completes and never reports why — and it removes the
redelivery that the `sequence` field exists to absorb (ADR-0013).

## Consequences

The driver stays small and the wire stays legible: a plain MQTT 5 client can correlate against us
and read our metadata without knowing anything about Interchange. `AtLeastOnce: true` follows
honestly from QoS 1, so the engine's replay suppression is what stops a retried request running the
handler twice.

The cost is exclusion, and it is not hypothetical: **brokers and devices that only speak 3.1.1
cannot use this driver.** For a device fleet that is a real constraint — firmware is not always
upgradable — and the answer is a bridge in front of the broker, not a degraded driver. Refusing QoS
0 also removes the cheapest configuration from anybody who wanted fire-and-forget telemetry over
this transport; that is a request/response layer's bias showing, stated rather than hidden.

Leaving a packet unacknowledged is the only way to say "not handled" (ADR-0025), which has two
consequences worth knowing: paho flushes acks in receipt order, so one unhandled message holds up
the acks behind it on that connection, and a redelivery arrives only when the broker's inflight
timer fires or the session resumes.

## Alternatives

**3.1.1 with an in-payload envelope for metadata, reply and correlation.** Everything above. It is
buildable and it is a private protocol.

**3.1.1 with topic-encoded reply addresses.** Works for the return address and answers neither
metadata nor correlation, and it burns topic space that the tenancy prefix already uses.

**Accept QoS 0 with a warning.** A warning is read after the incident. The chunk that vanished did
so silently, which is the property that makes it worth refusing rather than documenting.

## Evidence

- `driver/mqtt/mqtt.go` — package doc ("Version 5 only, and that is a decision rather than an
  omission"), the QoS validation in `Connect` and in the config parser (`only 1 and 2 are
  supported`), and `Caps()` with `AtLeastOnce: true`.
- `driver/mqtt/README.md` — "Why version 5 only", the topic grammar, the moved Correlation Data,
  and the QoS 0 rejection with its reason.
- `driver/mqtt/mqtt_test.go` — `TestChunking`, `TestRedeliverySuppressed`, `TestSharedSubscription`,
  and `TestCorrelationData`, which asserts Correlation Data and Response Topic on the wire through
  a broker hook; every broker-exercising test ends by asserting nothing is
  inflight, which is what catches a forgotten PUBACK.
- `docs/08-decisions.md` — "MQTT version | 3.1.1 + 5 / 5 only | **5 only** — 3.1.1 means
  reinventing correlation and metadata badly".
- `4b9f100` — the phase-5 commit, which states the same reasoning and reports the engine bug the
  driver found.
