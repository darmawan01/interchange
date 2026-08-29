# 08 — Operating it

Living with a contract that has a build gate. What runs in CI, what breaks, and the gotchas that
are real in this repository rather than hypothetical.

## The drift gate

**Generated output is committed** ([ADR-0033](../adr/0033-generated-output-is-committed.md)). That
is the decision everything on this page follows from: it makes the generated surface reviewable, it
means a consumer does not need your toolchain to read your client, and it means the tree can go
stale. `ix verify` is what makes "committed and current" a build failure rather than an aspiration.

```
$ ix verify

  ✓ frontends       1 sources → 6 descriptors
  ✓ annotations     5 RPCs, 5 annotated, 0 public (reviewed)
  ✓ generators      6 targets
  ✓ drift           generated output matches the contract
```

Change one byte of generated code and it stops being clean:

```
$ printf '\n// drift\n' >> gen/go/authz/permissions.authz.go
$ ix verify

  ✓ frontends       1 sources → 6 descriptors
  ✓ annotations     5 RPCs, 5 annotated, 0 public (reviewed)
  ✓ generators      6 targets
  ✗ drift           gen/go/authz/permissions.authz.go differs

  generated output is stale — run `ix generate`
$ echo $?
1
```

It regenerates into a **temporary tree** and compares byte for byte, so it never touches your
working copy and is safe on a dirty checkout. It also checks that the committed `buf.gen.yaml` still
matches `interchange.yaml` — that file exists so `buf generate` works for someone without `ix`, and
two files that disagree about what CI generates is the same failure the gate exists to stop.
`ix plugin sync` rewrites it.

This is why plugins must **sort before emitting** and carry no timestamps. Go map iteration is
random, and a shuffled file fails the drift check every other run until nobody trusts the gate
([ADR-0036](../adr/0036-determinism-is-a-test.md)). Every plugin in this repo ships a determinism
test: generate twice, compare bytes.

## The four checks

### `ix lint`

Two passes. `buf lint` applies the `STANDARD` proto rules; `ix` then applies the rules buf cannot
know about, because they are load-bearing rather than stylistic — the URL, the bus subject, the CLI
command and the SDK method are all *derived* from the names.

```
$ ix lint
  ✓ lint            5 RPCs, 8 extensions, band ix builtin annotation band
```

Errors: `SERVICE_SUFFIX`, `FIELD_SNAKE_CASE`, `ID_IS_STRING`, `ENUM_ZERO_UNSPECIFIED`,
`ENUM_APPEND_ONLY`, `BAND_UNREGISTERED`, `BAND_MISMATCH`, `BAND_COLLISION`, `INTERNAL_EXPOSED`.
Warnings: `REST_NO_HTTP_RULE`, `GROUP_WITHOUT_BUS`, `TIMESTAMP_SUFFIX`, `DURATION_UNIT`.
`AUTH_MISSING` fires only when `auth.on_missing_annotation` is set — that is the module's policy,
not a framework rule.

`INTERNAL_EXPOSED` fires on `(internal)` plus a **public** road, and only that. `internal` + `bus`
is not flagged, because it is precisely how an RPC is made reachable service-to-service and nowhere
else — which is what `Reconcile` in the catalog example declares
([ADR-0050](../adr/0050-internal-means-public-bindings-skip-it.md)).

The band table is read from `docs/annotation-band.md` under the project root when present, and from
`ix`'s embedded copy otherwise. `--band <file>` overrides both. See
[02](02-defining-a-contract.md#the-annotation-band).

### `ix breaking`

`buf breaking` with the project's rule, `WIRE_JSON` rather than `FILE`
([ADR-0004](../adr/0004-wire-json-breaking-rule.md)): it allows refactors that keep both the binary
wire form and the JSON field names compatible, which is what a public JSON surface actually
requires.

```
$ ix breaking --against '../../.git#branch=main,subdir=examples/catalog'
  ✓ breaking        no incompatible change against ../../.git#branch=main,subdir=examples/catalog
```

> **The `--against` ref is a buf input string, and it is fiddly.** The scaffolded `Makefile` writes
> `ix breaking --against '.git#branch=main'`, which only works when the project root **is** the git
> root. In this repository, running that from `examples/catalog` fails:
>
> ```
> Failure: could not clone file:///.../examples/catalog/.git: exit status 128
> fatal: '/.../examples/catalog/.git' does not appear to be a git repository
> ```
>
> Point at the real `.git` and name the subdirectory, as above. The scaffolded CI workflow gets this
> right (`,subdir=.`); the scaffolded Makefile does not.

Watch for these specifically, because none of them is obvious in a diff:

- **Renumbering an enum value.** Append-only, forever.
- **Reordering properties in an imported OpenAPI document.** Field numbers come from document order,
  so a reorder is a wire break. [07](07-bring-your-own-format.md#two-things-that-will-bite-you).
- **Renumbering an annotation.** Not a wire break for your messages — a *silently dropped
  annotation* on every descriptor built before the change. For the `(auth)` option, that is an
  authorization check that stops firing, and `buf breaking` will not tell you.

### `ix doctor`

The first thing to run when something is wrong and you do not know what:

```
$ ix doctor

  ✓ buf             1.69.0  (/opt/homebrew/bin/buf)
  ✓ go              go1.26.1 darwin/arm64  (/usr/local/go/bin/go)
  ✓ ix              dev  (darwin/arm64)
  ✓ interchange.yaml interchange.yaml  (1 source(s), 6 generator(s))
  ✓ plugins         6 local plugin(s) built
  ✓ band            7 registered annotation(s)  (ix builtin annotation band)
  ✓ buf.lock        current
  ✓ contract        6 descriptor(s), 5 RPC(s), 8 extension(s)
  ✓ generated       up to date
```

On a fresh scaffold before the first generate, the last line is the one that matters:

```
  ✗ generated       stale -- gen/go/example/v1/example.pb.go is missing (and 3 more) · run `ix generate`
```

and the exit status is 1.

### `ix describe`

Not a check, but the one to run **in review**. It answers "what does this method actually expose,
and who can reach it?" in one screen, and "should this be on the public bus?" stops being a question
nobody thought to ask. [01](01-getting-started.md#ix-describe--before-you-write-any-go).

## `ix dev` day to day

```
$ ix dev
  ✓ dev             2 procedure(s) over driver/memory
      subscribed  rpc.todo.v1.TodoService.CreateTodo
      subscribed  rpc.todo.v1.TodoService.ListTodos  (queue group: todo)
  listening on .interchange/dev.sock
  stub responses only — this exercises the contract, not your handlers
```

It is for answering "does this contract dispatch?" without infrastructure and without a handler —
handy when you have just added an RPC and want to see the subscription plan, or when you want to
check that a request shape decodes. It is **not** a service mesh or a process manager, and it says
so.

Two things to know: the loopback socket lives at `<project>/.interchange/dev.sock`, so a deeply
nested project path can exceed the platform's ~104-byte socket limit and `ix dev` exits with
`bind: invalid argument` — while `ix dev call` keeps working, because it falls back to its own
loopback. And `ix dev call` on its own needs no running server at all.

## CI

`ix init` writes `.github/workflows/interchange.yml`:

```yaml
- name: lint
  run: ix lint

- name: breaking
  if: github.event_name == 'pull_request'
  run: ix breaking --against '.git#branch=${{ github.event.pull_request.base.ref }},subdir=.'

# The drift gate. This is the step that makes committed generated code
# trustworthy rather than merely present.
- name: verify
  run: ix verify
```

`fetch-depth: 0` on the checkout, because breaking-change detection needs the baseline branch.

### The CI workflow `ix init` writes does not pass today

The `install ix` step is:

```yaml
- name: install ix
  run: go install github.com/darmawan01/interchange/ix/cmd/ix@latest
```

That module is not published — verified 2026-08-30, `404 Not Found` from `proxy.golang.org`. Until
it is, replace that step with a build from a clone, or vendor the binary. Everything else in the
workflow is correct.

### This repository's own gates

`CONTRIBUTING.md` has the eight gates. `make all` runs the ones a machine can check:

```bash
make plugins ix     # build the local plugins and the CLI
make depcheck       # the dependency rule: core imports no broker, router, policy engine or auth
make vet test       # every module — a single `go test ./...` only covers the one you are in
make verify         # the drift gate — this is what CI runs
```

`make verify` is stricter than `ix verify` alone: it also runs
`git diff --exit-code -- '**/gen/**'`, because `ix verify` gates the example and the git diff gates
everything the framework generates for itself.

## Gotchas that are real here

### Remote plugins are rate limited

The `Makefile`'s comment states the decision:

> The Go generators are pinned and installed locally rather than pulled from the remote plugin
> registry. Remote plugins are rate limited, and a drift gate that fails because a registry was busy
> is a gate people learn to ignore — so the one command CI depends on most does not touch the
> network.

```make
plugins:
	GOBIN=$(CURDIR)/$(BIN) $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
	GOBIN=$(CURDIR)/$(BIN) $(GO) install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.20.0
	$(GO) build -o $(BIN)/protoc-gen-bus   ./tools/cmd/protoc-gen-bus
	$(GO) build -o $(BIN)/protoc-gen-cli   ./tools/cmd/protoc-gen-cli
	$(GO) build -o $(BIN)/protoc-gen-authz ./auth/cmd/protoc-gen-authz
```

`examples/catalog/interchange.yaml` then references them by path
(`../../bin/protoc-gen-go`), and `ix generate` reproduces the committed tree byte for byte with no
network call ([ADR-0048](../adr/0048-codegen-does-not-touch-the-network.md)).

**What `ix init` scaffolds does the opposite** — two remote plugins, pinned by version. That is the
right default for a first run and the wrong one for CI. Switch to local paths before you rely on the
gate, and remember: if `plugin:` is a path and `./cmd/<basename>` exists in the project, `ix
generate` builds it first, because a generator that is not rebuilt before it runs emits yesterday's
output.

**Pin every version.** An unpinned generator makes the committed output a function of when CI ran.

### Review diffs will be large

Committing generated output is the deliberate cost of the drift gate: adding one RPC to a contract
touches the message types, the Connect client, the ServiceDesc, the bus client, the CLI tree, the
permission table and the TypeScript. Two things make it survivable:

- **Mark generated paths in `.gitattributes`** (`gen/** linguist-generated=true`) so review tools
  collapse them by default.
- **Review the `.proto` and the `ix describe` output, not the generated Go.** The diff you actually
  need to read is one file, and `ix describe` renders its consequences. If the generated code
  contains a surprise, that is a plugin bug, not a review finding.

The generated tree is not there to be read. It is there so it can be *diffed* — so a contract change
that quietly widens a public surface is visible in a pull request rather than in someone else's
logs.

### A generator you leave out of `interchange.yaml` is a generator outside the gate

`ix verify` diffs exactly the `out` directories that `interchange.yaml` names. Anything generated by
another route is not gated, and that is easy to arrange by accident — usually because one generator
needs a buf option the config did not carry, so it gets moved to a hand-written `buf.gen.yaml` "for
now".

The one that comes up is `include_imports`. A protobuf-es file names its imports' descriptors at
runtime, so the annotation protos have to be emitted alongside it or the front end does not
typecheck. It is expressible:

```yaml
generate:
  - plugin: node_modules/.bin/protoc-gen-es
    out: gen/ts
    opt: [target=ts, import_extension=js]
    include_imports: true
```

(`Generate.IncludeImports` in `ix/internal/config/config.go`, threaded through to the synthesized
template in `ix/internal/gentmpl/gentmpl.go`.) `examples/catalog` declares exactly that, which is
why its `gen/ts` is inside the gate and `ix verify` reports **6 targets** rather than 5.

`include_imports` is **per plugin, and only that one wants it**: the Go plugins must not have it,
because core, `/auth` and `/tools` already ship those generated types and registering the same file
twice panics at init.

If a target is not in `interchange.yaml`, ask why — the answer should never be "because the gate
complained".

## Versioning and publishing the generated SDK

The contract is versioned in the proto package (`catalog.v1`), and `buf breaking` enforces
compatibility within it. What you publish is a *consequence* of that, not a second decision.

**There is no hand-written SDK wrapper** ([ADR-0052](../adr/0052-no-hand-written-sdk-wrapper.md)).
Whatever you publish is the generated output; a hand-written layer over it is a second contract with
a second drift problem.

**Go** — the generated tree is a normal Go package under your module. Consumers `go get` your
module at a tag. Nothing extra to publish, and the drift gate is what makes the tag trustworthy.

**TypeScript** — `gen/ts` is what a front end imports. Publish it as a package from CI on a tag,
with the version derived from the same tag as the Go module so a consumer can reason about which
contract they are on. The `examples/catalog` front end imports it by relative path, which is right
for a monorepo and not a distribution story.

**OpenAPI** — emit it from the same descriptors the REST binding routes on, commit it, and gate it.
`examples/catalog/openapi.json` has a drift test of its own
(`TestTheEmittedOpenAPIMatchesWhatPartnersCall`): a path that changes should be visible in a diff
before it is visible in someone else's logs.

**`ix` itself** — `.goreleaser.yaml` builds static binaries for darwin/linux/windows on
amd64/arm64, and `packages/cli` wraps them for npm:

```bash
goreleaser release --snapshot --clean      # builds dist/ix_<os>_<arch>/ix
npm --prefix packages/cli run build:platforms
npm publish packages/cli/npm/*             # then the launcher package itself
```

Publishing is deliberately not wired into CI while the name is still a working name. The npm channel
matters more than it looks ([ADR-0049](../adr/0049-the-npm-channel-is-first-class.md)): the front
end is the consumer with the most to gain from typed clients and the least tolerance for a Go
toolchain, and if getting them requires installing Go, adoption breaks at exactly the team the
project is most useful to.

`ix` does not host a registry ([ADR-0054](../adr/0054-ix-does-not-host-a-registry.md)). It can talk
to one.

## Maturity, stated plainly

Nothing here has served production traffic, and every extension point is public API that some of
which will move. The root [README's Maturity table](../../README.md#maturity) says which parts carry
which evidential weight — read it before planning against any of them.
