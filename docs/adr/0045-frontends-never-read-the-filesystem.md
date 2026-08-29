# ADR-0045 — A frontend never reads the filesystem

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 6

## Context

The natural signature for an importer takes a path and opens it. That makes the frontend depend on
a filesystem laid out the way the frontend expects, and it removes every caller that does not have
one: `ix` cannot drive a frontend against a git object, an archive, a `--dry-run` from stdin, or a
test fixture held in a string. It also gives the frontend a second, undeclared input channel —
whatever it decides to open next — which is exactly what makes an OpenAPI external `$ref` a
security and reproducibility problem rather than a feature.

So `Sources` carries content: `Paths` in order, `Content` mapping each path to its bytes, plus the
optional sidecar and its name. No frontend opens a file.

That closed one problem and opened another, which is the part worth recording because it was
discovered rather than designed. A frontend that cannot read the filesystem can only resolve
external types that are *linked into whatever binary is doing the parsing*. That is fine for
`google.protobuf.Timestamp` and for core's own `transports` annotation. It is useless for anything
an adopter wrote, and worse: to emit an `(interchange.auth.v1.auth)` annotation, the DSL module had
to import the `/auth` module's generated Go — making an **optional** module a hard dependency of a
frontend, which inverts the whole extension model (ADR-0019).

## Decision

`Sources` carries content and a frontend never reads the filesystem. `Options.Deps` carries a
`*descriptorpb.FileDescriptorSet` of everything the sources may reference by fully-qualified name —
the annotation protos for the modules the caller has installed, and the adopter's own existing tree.
`ix import` fills it from the workspace image, so the annotation protos reach the frontend without
the frontend linking the modules that define them. An external `$ref` is a refusal in OpenAPI:
"a frontend never reads the filesystem — bundle the document".

Resolving `Deps` is core's job, in `interchange.DepFiles`, because two frontends wrote the same ten
lines and the first got it wrong.

## Consequences

`ix` drives a frontend from anywhere bytes come from, and a frontend's tests need no fixtures on
disk. The DSL module's import graph contains neither `/auth` nor `/tools`, so an optional module
stays optional.

Without the descriptors for an optional module's annotation, an `auth:` or `cli:` block is a **loud
error at the RPC naming the missing file**, never a silently dropped annotation — the ADR-0039 rule
applied to the case that would otherwise reintroduce ADR-0035's silent absence at a different layer.

`DepFiles` owns the two hazards both frontends got wrong:

1. **Already-linked files must be dropped.** Every `FileDescriptorSet` built with
   `--include_imports` carries `descriptor.proto`. Building a second object for a path the compiler
   already has puts two of that file in one link, and then every symbol in it collides with itself.
   The DSL had this latent and untested; OpenAPI hit it.
2. **Set ordering is conventionally, but not always, deps-first.** A file that imports another must
   be registered after it whatever order the set arrived in, so `DepFiles` registers by repeated
   passes until one makes no progress. The failure when this is wrong is an unresolved import,
   which nobody would connect to file ordering.

Costs: the caller now has to *have* the descriptors, so a frontend used as a library outside `ix`
must build a `FileDescriptorSet` itself before it can emit an optional module's annotation. The
whole document must be in memory, which rules out streaming a very large source. And a bundled
document is required — a real-world OpenAPI spec split across files with external `$ref`s has to be
bundled before import, which is work for the user before it is value.

## Alternatives

**Take a path and open files.** Rejected: it couples the frontend to a filesystem layout, removes
every non-filesystem caller, and turns an external `$ref` into an undeclared network or disk read.

**Give the frontend a `fs.FS` instead of bytes.** Considered. It keeps the abstraction but still
lets a frontend decide what to open, so an external `$ref` remains resolvable and the input set
remains undeclared. `Sources.Content` makes the input set exact.

**Let each frontend link the annotation modules it wants to support.** What the DSL did before
`Options.Deps`. Rejected: it makes an optional module mandatory, and it caps a frontend's
resolvable types at whatever its author thought to import.

**Let each frontend resolve `Deps` itself.** What both frontends did first, and the first got it
wrong. The third copy of the same ten lines is what moved it into core — the same argument that
moved option resolution there (ADR-0035).

## Evidence

- `frontend.go` — `Sources` ("a frontend must not read the
  filesystem itself, so that `ix` can drive it from a git object, an archive, or a test fixture"),
  `Options.Deps`, and `DepFiles` with both hazards in its doc comment.
- `frontend_deps_test.go` —
  `TestDepFilesDropsAlreadyLinkedFiles` (a `Deps` set carrying `descriptor.proto` must register
  zero files), `TestDepFilesResolvesOutOfOrder` (dependent first, "the order that fails a single
  pass"), `TestDepFilesReportsAGenuinelyMissingImport`.
- `frontend/dsl/internal/nolink/` and
  `frontend/openapi/internal/nolink/` — **test packages that
  exist to link nothing**, and thereby prove the dependency is gone; their `doc.go` explains that
  the property is invisible in each frontend's own tests, which link the generated types in order
  to assert on them. `TestAuthAnnotationNeedsItsDescriptors` asserts that with no `Deps` an `auth:`
  block fails loudly, naming `interchange/auth/v1/auth.proto`, with a path, a non-zero line and
  column, a hint mentioning `Options.Deps`, and no descriptor set returned alongside the error.
  `TestCoreAnnotationsNeedNoDeps` asserts core's own `transports` still resolves with no `Deps`.
- `ix/internal/cmd/import.go` — reads the bytes itself and passes
  `opt.Deps = im.FDS` from the workspace image.
- `frontend/openapi/README.md` — external `$ref` in the refusal
  table: "a frontend never reads the filesystem — bundle the document".
- Commits `74404af` (`Options.Deps` added as a seam gap the DSL found), `b1007d3` (the DSL adopts it
  and the `nolink` package proves the dependency is gone) and `54bb942` ("the dependency-linking
  trap it found twice").
