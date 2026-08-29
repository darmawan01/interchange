# ADR-0046 — `ix` shells out to buf rather than embedding it

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

Every command in `ix` that reads a contract needs the same input: a linked `FileDescriptorSet`
with the custom options on it resolved (ADR-0001). There are two ways to get one. Embed buf as a
library and compile the proto tree in-process, or run the buf the project already pins and read
what it writes.

Embedding looks tempting because it removes a prerequisite. It also makes `ix` the compiler. The
version of buf that resolves imports, applies `managed` mode and enforces `buf.yaml` would then be
whichever version was linked into the `ix` binary the user happened to install — not the one
`buf.lock` pins, and not the one CI runs. Two compilers over one tree is how a contract starts
building in one place and failing in the other.

## Decision

`ix` drives the installed buf binary. `bufx.Runner` is the only thing in the tool that starts a
process, and every descriptor-reading command goes through `image.Build`, which runs
`buf build -o - --as-file-descriptor-set` and parses the bytes on stdout. `ix generate` synthesises
a `buf.gen.yaml` from `interchange.yaml`'s `generate[]` block and hands it to `buf generate
--template`; `ix lint`, `ix breaking` and `ix fmt` relay buf's own diagnostics verbatim, because
buf's messages carry file and line and rewording them loses that.

`image.Parse` then does the part buf will not do for you: it unmarshals the descriptor set twice.
The first pass yields descriptors whose options are unknown bytes; from those it collects every
extension the tree *declares* and mints a `dynamicpb` extension type for each; the second pass
re-parses the same bytes with that resolver. `(transports)`, `(internal)`, `(cli.command)` and the
optional module's `(auth)` all arrive as typed fields, read by number and field name rather than by
importing the Go package that defines them. That is why `ix describe` can print an auth annotation
without `ix` depending on the auth module, and it is the same dual availability of a custom option
that the running server relies on (§02): one descriptor, read in a plugin at build time and by
reflection at run time.

## Consequences

buf is a runtime prerequisite. `ix` is not self-contained, and a machine without buf gets a
failure from the first command that touches a contract — so `bufx.ErrNotFound` names the install
command instead of surfacing `exec: "buf": executable file not found in $PATH` three frames deep,
and `ix doctor` checks for buf and reports its version and path first, before anything else. The
container image ships buf beside `ix` for the same reason: an image that needs a second tool
installed next to it is not a distribution channel.

The payoff is that anything `ix` does is reproducible by hand. Run with `--verbose` and every buf
invocation is echoed; a user who already knows buf can paste the line. The project's pinned buf
version is the one that runs, `managed` mode behaves as the buf docs say it does, and `ix` inherits
buf's workspace resolution rather than reimplementing it — `ix doctor` walks up for `buf.yaml`
precisely because buf does.

One thing shelling out cannot paper over: buf builds one input per invocation. `image.Build`
refuses more than one rather than merging N images by hand, because merging would silently drop
conflicting file entries. A project that needs the union expresses it in a `buf.yaml` workspace.

## Alternatives

**Embed `bufbuild/buf` as a library.** Lost on version skew: the compiler would be whatever `ix`
was built against, not what the project pins, and an `ix` upgrade would silently change how a
contract compiles. It also drags buf's full dependency tree into a tool whose own job is small.

**Call `protoc`.** Loses `buf.yaml`, `buf.lock`, managed mode, the dependency graph and the lint and
breaking rules — everything ADR-0004 and the workspace layout depend on. `ix` would have to
reimplement dependency resolution to be useful, which is embedding a compiler with extra steps.

**Vendor a descriptor set into the repo and read that.** Removes the prerequisite and the
reproducibility together: the descriptors would be another committed artifact that can go stale,
and staleness in the IR is invisible in a way staleness in generated Go is not.

## Evidence

- `ix/internal/bufx/bufx.go` — the only process launcher in the tool; `Output` is documented as
  "a binary image on stdout is the point", and `ErrNotFound` carries the install instruction.
- `ix/internal/image/image.go` — `Build` runs `buf build -o - --as-file-descriptor-set`; `Parse`
  is the two-pass unmarshal with the `dynamicpb` resolver.
- `ix/internal/cmd/project.go:63` and `:67` — every command reaches descriptors through
  `image.Build` / `p.Image()`; `describe.go`, `lint.go`, `verify.go`, `dev.go`, `doctor.go` and
  `import.go` all call it and none parse protos themselves.
- `ix/internal/cmd/generate.go:63` — `p.Buf.Run("generate", "--template", f.Name())`.
- `ix/internal/cmd/doctor.go` — the buf check runs first and reports version and path.
- `Dockerfile` — `FROM bufbuild/buf:latest AS buf`, then `COPY --from=buf /usr/local/bin/buf`.

See ADR-0001 (`FileDescriptorSet` is the IR, not a bespoke AST) and ADR-0035 (annotations are read
through core, never off `Descriptor.Options()`).
