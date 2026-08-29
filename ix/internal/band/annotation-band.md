# The annotation band

Field numbers on `google.protobuf.MethodOptions` (and its siblings) are a
**global namespace**. Two annotations at the same number is a silent,
undebuggable collision: the descriptor still parses, and one of the two
options is simply gone.

So the band is reserved once, and every assignment is recorded here **before**
the annotation exists. `ix lint` reads this table.

**Reserved band: `50000–59999`** — the conventional private range.

| No. | Extends | Annotation | Module | Consumed by |
| --- | --- | --- | --- | --- |
| 50001 | `MethodOptions` | `interchange.auth.v1.auth` | `/auth` *(optional)* | the authz interceptor and `protoc-gen-authz` |
| 50002 | `MethodOptions` | `interchange.transport.v1.transports` | core | every binding plugin — which roads this RPC is exposed on |
| 50002 | `ServiceOptions` | `interchange.transport.v1.service_transports` | core | the service-level default for the above |
| 50003 | `MethodOptions` | `interchange.transport.v1.internal` | core | skipped by every public binding; mTLS-only |
| 50004 | `MethodOptions` | `interchange.cli.v1.command` | `/tools` | `protoc-gen-cli` |
| 50007 | `FieldOptions` | `interchange.auth.v1.tenant_id_field` | `/auth` *(optional)* | the authz interceptor, by reflection |
| 50008 | `FieldOptions` | `interchange.auth.v1.project_id_field` | `/auth` *(optional)* | the authz interceptor, by reflection |

## Free

`50005`, `50006`, and `50009–59999`.

## Rules

1. **Claim the number here first.** A PR that adds an extension without a row
   in this table does not merge.
2. **Different extendees may share a number.** `50002` is `transports` on
   `MethodOptions` and `service_transports` on `ServiceOptions`; they cannot
   collide because the extendee is part of the identity. Both still get a row.
3. **Never renumber.** A renumbered option is a silently dropped annotation on
   every descriptor built before the change — which, for the `auth` option, is
   an authorization check that stops firing.
4. **A module owns its own numbers.** Core does not reserve space for an
   annotation it will never parse; the `/auth` module's numbers belong to
   `/auth`, and its proto lives in that module's tree.
