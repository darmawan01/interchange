# `/validate` — declarative field rules

Optional. Rules are written once, in the contract, as
[protovalidate](https://buf.build/bufbuild/protovalidate) options; this module is the one
interceptor that enforces them.

```protobuf
message CreateProviderRequest {
  string name  = 1 [(buf.validate.field).string = {min_len: 3, max_len: 64}];
  string email = 2 [(buf.validate.field).string.email = true];
  int32  rate_limit = 3 [(buf.validate.field).int32.gte = 0];
}
```

```go
chain := interchange.DefaultChain(cfg).Append(validate.Stage(v))
```

## "Written once, applied on every transport" — stated precisely

The claim is not that this module reaches every transport. It is narrower and stronger, and it is
split between core and here:

- **Core guarantees** that a chain runs identically on every road, in the same order, over the same
  envelope. Nothing but `Registry.Dispatch` runs a chain, and no binding or driver holds one — so
  this stage *cannot* be enforced on HTTP and silently skipped on the bus. That is chain symmetry,
  and it is the half you cannot get by being careful.
- **This module guarantees** that the enforcement itself is transport-blind: it validates
  `Envelope.Msg`, the decoded message that dispatch produced before the chain ran. There is no
  second, transport-shaped copy of the request to check, and therefore no second place for the rule
  to be subtly different.

What follows is that a violation is *the same violation* everywhere: same `invalid_argument` code,
same reason, same field paths, same rule ids, same messages. `validate_test.go` asserts exactly
that — one registry, one chain, the identical bad request over `binding/rpc` (httptest, Connect
JSON) and over `engine` + `driver/memory`, and a `slices.Equal` on the violations that come back.

What it does **not** claim: a rule enforced at a client, at a gateway you own, or in a hand-written
route bolted onto the same mux beside Interchange. Chain symmetry covers the roads Interchange owns.

## What comes back

`interchange.CodeInvalidArgument`, reason `INVALID_ARGUMENT`, and the field-level detail in the
error's metadata — ordinary metadata, so it reaches a caller as HTTP headers on the Connect binding
and in `Response.metadata` on a bus, with no per-transport code:

```
ix-violations                3
ix-violation-1-field         name
ix-violation-1-rule          string.min_len
ix-violation-1-message       value length must be at least 3 characters
```

Read it back with `validate.ViolationsOf(err)`, whichever road the error arrived on, or
`validate.ViolationsFrom(md)` if you are holding headers rather than an error. `validate.Count(err)`
is the exact number of violations, which can exceed the number reported: `WithMaxDetails` (default
8) caps the metadata so a message with a hundred bad fields does not produce a header block a proxy
rejects.

Values are stripped of control characters — a rule message is author-supplied CEL output, and a
newline in it would be rejected at header-write time on one road while travelling fine on another.

`INVALID_ARGUMENT` is the stock `/errors` reason, written here as a string constant rather than
imported: neither optional module depends on the other, and core takes no position on either. Pass
`WithReason` if you run a different taxonomy.

A rule that will not compile, or a CEL expression that fails at runtime, comes back as
`CodeInternal` — that is a defect in the contract, not in the request, and reporting it as
`invalid_argument` sends the caller chasing their own payload.

## Swapping the validator

`Stage`/`Interceptor` take a `protovalidate.Validator`. It is an interface, so anything satisfying
it works:

```go
v, err := protovalidate.New(
    protovalidate.WithFailFast(),                     // stop at the first violation
    protovalidate.WithExtensionTypeResolver(myTypes), // predefined rules of your own
)
chain := interchange.DefaultChain(cfg).Append(validate.Stage(v))
```

`validate.Stage(nil)` uses `protovalidate.GlobalValidator`, the shared instance with the shared rule
cache — lower memory than one validator per service, and the right default when you have no reason
to configure anything.

## Ordering

Extend by name, never by position:

```go
interchange.DefaultChain(cfg).
    Before(validate.StageName, authn()).      // credentials before rules
    After(validate.StageName, authz())        // ... and a resource-aware permission check
                                              //     after, so it never reads an unchecked field
```

## Dependencies

`buf.build/go/protovalidate` (and its CEL stack), protobuf, core. Nothing else.

## Test fixture

`validate/testdata/` is a self-contained buf module — its own `buf.yaml`, its own `buf.lock`
pinning `buf.build/bufbuild/protovalidate`, and its own `buf.gen.yaml`. It is deliberately not a
member of the root workspace: it is a fixture, and it should not appear in the repo's public
contract. Regenerate with `buf generate` from inside `validate/testdata`; the output in
`validate/internal/testpb` is committed.
