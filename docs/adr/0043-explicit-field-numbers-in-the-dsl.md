# ADR-0043 — The DSL requires explicit field numbers

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

The Interchange DSL exists to be the gentlest on-ramp in the project: a small YAML file for a team
that does not want to learn an IDL. Field numbers are the least YAML-ish thing in protobuf. Every
instinct says derive them — count from one down the list of fields, the way JSON Schema, OpenAPI and
every YAML config the user has ever written manage without them.

Derivation is wire-unsafe, and unsafe in the worst way. A field number is part of the wire contract:
it is what an old binary uses to find a field in a message a new binary encoded. If numbers come
from declaration order, then moving a field up two lines — an edit a reviewer reads as cosmetic,
that an autoformatter or an alphabetising editor might make unprompted — renumbers the contract.
Every deployed peer now decodes the wrong field into the wrong slot. The diff shows two lines
swapped; the breaking-change checker, run against the *emitted* proto, would flag it, but the person
who made the change has no reason to connect a YAML reordering to a wire break.

## Decision

`n` is mandatory on every field. The DSL will not derive a field number from declaration order.
A field with no `n` is an error naming the field, with a line and a column and the hint `n: 1`, and
nothing is emitted. Enum values are declared with their numbers for the same reason. Reordering the
YAML can never move a number, and the emitted proto puts fields and enum values in numeric order
regardless of how they were typed — so declaration order carries no meaning at all and cannot
accidentally acquire any.

## Consequences

The wire contract is stated where it is enforced. A reviewer of a DSL diff sees the numbers change
when the numbers change, and never otherwise. Reordering, reformatting and alphabetising are all
safe operations.

Adjacent invariants come free once numbers are explicit: a duplicate number is an error naming both
fields, and a number in protobuf's reserved range is an error — neither of which is expressible if
numbers are derived.

The cost is friction on precisely the on-ramp the DSL exists to smooth, and it is paid deliberately.
The first thing a new user meets is a mandatory field whose purpose they do not yet understand, in
the tool that was sold to them as the one that would not make them learn protobuf. They will get it
wrong, they will ask why, and the answer is a paragraph about wire compatibility they did not want
to read. There is no way to soften this without giving up the property.

## Alternatives

**Derive from declaration order.** The whole point of the alternative and the reason the decision
exists. Rejected: it converts a cosmetic edit into a silent wire break with no reviewable diff.

**Derive, but pin on first emit** — write the derived numbers back into the YAML the first time.
Rejected: it means the tool edits the user's source file, and it only protects contracts that have
already been emitted once. A field added before the first emit is still ordered.

**Derive from a hash of the field name.** Rejected: it makes renaming a field a wire break instead,
trading one silent failure for another, and it produces numbers no human can reason about.

**Derive by default with an opt-in strict mode.** Rejected on the same grounds as every "warn
instead of fail" option in this project: the default is what most contracts will be written under,
and the default has to be the safe one.

## Evidence

- `frontend/dsl/README.md` §"Field numbers are required, and
  that is the point" — the rule, the reasoning, and the note that fields and enum values are emitted
  in numeric order while messages, enums, services and RPCs keep declaration order.
- `frontend/dsl/diag_test.go` — three of the 18 `TestTotalOrLoud`
  cases pin this: "field with no number" (message `field "a" has no number`, hint `` `n: 1` ``),
  "duplicate field number" (`both use number 1`, hint mentioning the wire contract), and "reserved
  field number". Each asserts the exact line, a non-zero column, and that no partial descriptor set
  came back.
- Commit `74404af` records it as one of "two design calls worth keeping": field numbers are
  required "because deriving them from declaration order means reordering the YAML silently
  renumbers the wire contract".

See ADR-0004 for the breaking-change rule the emitted proto is checked against, and ADR-0039 for
the refusal machinery this error rides on.
