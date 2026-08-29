# ADR-0049 — The npm channel is first-class

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

The front end is the consumer with the most to gain from a generated client and the least tolerance
for a Go toolchain. It is where a renamed field turns into a failed `tsc --noEmit` instead of a
broken page, and it is also where "first install Go, then `go install .../ix@latest`" is where the
conversation ends. If getting typed clients requires a Go toolchain, adoption breaks at exactly the
team the project is most useful to.

So npm cannot be an afterthought channel that lags the Go one. It has to install the same binary,
at the same version, with no build step, no `node-gyp` and no postinstall download.

## Decision

`@interchange/cli` is a launcher and nothing else. It ships no JavaScript implementation of any
command: `bin/ix.js` resolves a platform binary and `spawnSync`s it with stdio inherited and the
exit code passed through unchanged. The binary arrives as one of five optional dependencies —
`@interchange/cli-{darwin,linux}-{arm64,x64}` and `@interchange/cli-win32-x64` — each a package
containing an executable and a `package.json` with `os` and `cpu` set, so `npm install` downloads
exactly one of them. `scripts/build-platform-packages.mjs` builds those packages from the binaries
goreleaser already produces.

The launcher prefers a locally built `bin/ix` at the repository root when one exists, so a
contributor running `make ix` exercises the code in front of them rather than a published release.

## Consequences

`npx @interchange/cli generate` is a real path to a typed client, and it is the same `ix` a Go user
installs — same version, same behaviour, same `interchange.yaml`. Nothing has to be reimplemented
in JavaScript, so the npm channel cannot lag or diverge in behaviour; it can only lag in version.

Costs. Five platform packages must be published in lockstep with the launcher, and every one of
them has to carry the same version string or `npm install` resolves a binary from a different
release than the launcher expects. A platform nobody built for — linux/riscv64, say — gets a clear
refusal naming `go install` as the fallback, not a working install. And `npm install --no-optional`
silently skips the binary, which is why the launcher's failure path names that flag explicitly
rather than reporting "not found".

The local-`bin/ix`-wins rule is a convenience for contributors and a trap for anyone who happens to
have a stale `bin/ix` three directories above where they ran `npx`. It is scoped to the repository
layout (`../../../bin/ix` relative to the launcher) rather than searching upward, which bounds the
surprise but does not remove it.

Nothing is published. The artifacts exist and every upload is switched off: `.goreleaser.yaml` sets
`release.disable: true`, `brews[].skip_upload: true` and `dockers[].skip_push: true`, and the npm
publish is a documented two-command sequence rather than a CI step. That is deliberate while
`interchange` is still a working name — a name change after publishing is a deprecation notice and
a migration, and before publishing it is a rename.

## Alternatives

**A JavaScript reimplementation of `ix`.** A second implementation of the drift gate is a second
thing that can disagree with the first, which is the failure this whole project exists to remove.
It would also have to reimplement descriptor parsing and the buf integration.

**A postinstall script that downloads a binary.** Common, and hostile: it breaks behind proxies and
in air-gapped installs, needs a network fetch at install time, and is exactly the pattern
`--ignore-scripts` environments block. Optional dependencies get the same result through npm's own
resolution.

**One package containing all five binaries.** Every user downloads five executables to run one.

**Point front-end teams at the container image.** `docker run ghcr.io/darmawan01/ix generate` is a
real channel and is documented as one, but it is a CI answer. A front-end developer iterating on a
contract should not need Docker in the loop.

## Evidence

- `packages/cli/bin/ix.js` — the whole launcher: `PLATFORMS`, `resolveBinary()` with the local-
  binary preference, `spawnSync` with `stdio: 'inherit'`, and `process.exit(result.status ?? 1)`.
- `packages/cli/package.json` — `optionalDependencies` naming the five platform packages at the
  launcher's own version; `files` lists `bin/ix.js` and `README.md` and nothing else.
- `packages/cli/scripts/build-platform-packages.mjs` — `TARGETS` maps goreleaser output directories
  to npm platform packages and writes `os`/`cpu` into each generated `package.json`.
- `.goreleaser.yaml` — `release: disable: true`, `skip_upload: true` on the tap, `skip_push: true`
  on the image.
- `docs/11-cli.md` §Distribution — the channel table and the reason the npm one matters.

See ADR-0052 (no hand-written SDK wrapper over generated clients) — the launcher shipping no
implementation is the same rule applied to the tool.
