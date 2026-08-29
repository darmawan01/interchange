# `/errors` — the error taxonomy

Optional. Core ships three interceptors (`telemetry`, `recover`, `deadline`) because those are
properties of *dispatch*; an error taxonomy is a property of your product, so it lives out here as
an ordinary module with no privileged access to core (§06).

Core already carries the shape: `interchange.Error{Code, Message, Reason}`, and the envelope
reserves `code`, `message` and `reason`. Core assigns `Reason` **no meaning** — it moves it. This
module is the meaning: a closed enum, an interceptor that enforces membership, and the RFC 9457
projection.

## Why the reason and not the message

A client has to branch on *something*. If that something is the message, then rewording

```
"no such provider"  ->  "provider not found"
```

is a breaking change to every caller, and nobody will notice until the pager goes off. `code` alone
is too coarse: `not_found` cannot tell "no such provider" from "no such region", and those are
different recoveries.

So the branch point is a third thing: a short, stable, machine-readable string drawn from a **closed
enum in the contract**. Closed matters. A reason a client cannot enumerate from the proto is not
something it can write a `switch` over — it is a string it discovers in production.

The message stays free-form and is for humans. Reword it whenever you like.

## One error, four surfaces

`errors/foursurfaces_test.go` asserts this table rather than describing it, from one registry with
one chain:

| Surface | Form |
| --- | --- |
| handler | `errors.NotFound("PROVIDER_NOT_FOUND", "no such provider")` |
| envelope | `Response{code: 5, reason: "PROVIDER_NOT_FOUND"}` — **canonical** |
| HTTP | `404`, header `Ix-Reason: PROVIDER_NOT_FOUND` |
| REST | `404` + `application/problem+json` carrying `"reason": "PROVIDER_NOT_FOUND"` |

The reason string is byte-identical on every road, which is what lets a client branch once.

## Installing it

```go
chain := interchange.DefaultChain(cfg).
    After(interchange.StageTelemetry, errors.Stage(
        errors.WithReasons(errors.EnumSet(catalogv1.CatalogReason(0).Descriptor())),
    ))
```

Position: directly inside `telemetry`, and **outside `recover`** — everything it normalises has to
be below it, including the internal error that `recover` makes out of a panic. Appending it
innermost still works, but then it never sees a panic, and its unknown-reason panic is swallowed by
`recover` into a plain 500.

## The taxonomy

The stock enum is `errors/api/interchange/errors/v1/reasons.proto`, generated into
`errors/gen/go` (committed). It is append-only: renumbering is breaking.

A reason travels as the enum value name with the enum's own prefix trimmed —
`ERROR_REASON_NOT_FOUND` goes out as `NOT_FOUND` — so your own enum works the same way:

```protobuf
enum CatalogReason {
  CATALOG_REASON_UNSPECIFIED       = 0;   // never a reason: nothing to branch on
  CATALOG_REASON_PROVIDER_NOT_FOUND = 1;  // travels as PROVIDER_NOT_FOUND
}
```

`errors.EnumSet(desc)` builds the set from that descriptor. The stock set stays accepted alongside
yours, because a generic `PERMISSION_DENIED` raised by an interceptor is legal in every service.
`errors.SetOf("...")` is the escape hatch for a taxonomy not in a proto yet — prefer the enum: a Go
slice is not something a TypeScript client can read.

### Enforcement

An unknown reason is a programming error — a typo, or a value someone forgot to append to the enum.
What the process does about it is a deployment decision:

| Policy | Behaviour | Default when |
| --- | --- | --- |
| `UnknownReasonPanic` | panics | `testing.Testing()` — under `go test` |
| `UnknownReasonRewrite` | logs, substitutes the code's stock reason | everywhere else |
| `UnknownReasonLog` | logs, lets it through | migrating an existing taxonomy |
| `UnknownReasonAllow` | no check | — |

The runtime default rewrites rather than passes through: an unregistered reason on the wire breaks
the only promise this module makes.

## Bring your own

Two seams, in increasing order of how much you are replacing.

**Your own reasons, our mapping.** Pass `WithReasons(EnumSet(...))` and use the constructors:
`errors.NotFound`, `errors.PermissionDenied`, `errors.FailedPrecondition`, … Each takes the reason
first, because the reason is the part a client sees.

**Your own error type.** Implement `Mapper` once and stop writing `interchange.Error` at every call
site:

```go
errors.Stage(errors.WithMapper(errors.MapperFunc(
    func(ctx context.Context, procedure string, err error) *interchange.Error {
        var nf *catalog.NotFoundError
        if errors.As(err, &nf) {
            return errors.NotFound("PROVIDER_NOT_FOUND", "no such provider %q", nf.ID)
        }
        return errors.DefaultMapper{Redact: true}.Map(ctx, procedure, err)
    })))
```

`DefaultMapper` on its own: an error that already carries a reason passes through; an error with a
code but no reason gets that code's stock reason; a context error keeps `deadline_exceeded` or
`canceled`; anything else is `internal` + `INTERNAL`, with `Redact` available so `sql: no rows in
result set` does not become a public API.

## The `problem+json` projection

`Problem(err, opts...)` returns `interchange.common.v1.Problem` and the HTTP status. It is what the
REST binding serves; it lives here because the taxonomy is this module's business.

```go
p, status := errors.Problem(err,
    errors.WithInstance(r.URL.Path),
    errors.WithTypeBase("https://errors.example.com/"))
```

`WriteProblem(w, err, opts...)` does the whole response: `application/problem+json`, the status, and
the `Ix-Reason` header — the same header the Connect binding sets, so a client reads the reason
identically on both HTTP roads without parsing a body.

### code → HTTP status

The table is connect-go's, deliberately: the Connect binding and the REST binding are two roads out
of one dispatch, and a client that gets `504` from one and `408` from the other for the same handler
error has been told two different things.

| code | | code | |
| --- | --- | --- | --- |
| `ok` | 200 | `resource_exhausted` | 429 |
| `canceled` | 499 | `failed_precondition` | 400 |
| `unknown` | 500 | `aborted` | 409 |
| `invalid_argument` | 400 | `out_of_range` | 400 |
| `deadline_exceeded` | 504 | `unimplemented` | 501 |
| `not_found` | 404 | `internal` | 500 |
| `already_exists` | 409 | `unavailable` | 503 |
| `permission_denied` | 403 | `data_loss` | 500 |
| `unauthenticated` | 401 | | |

## Dependencies

Protobuf and core. Nothing else — no HTTP router, no connect, no logging framework. The Connect
reason header is repeated here as a string constant rather than imported, because a taxonomy module
that depends on a binding is a taxonomy module you cannot use on a bus-only service.

## Regenerating

```sh
buf generate --template buf.gen.errors.yaml errors/api   # from the repo root
```

Output is committed.
