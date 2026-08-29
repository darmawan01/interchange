# ADR-0033 — Generated output is committed and drift-gated

**Status:** Accepted
**Date:** 2026-08-30 · **Phase:** 1

## Context

The project's central claim is that the contract cannot drift: one `.proto` declares a service and
every client, server binding, permission table and command tree is derived from it. Nothing about
having generators makes that true. A repository that generates into `gen/` at build time and
ignores the directory has a contract that is *asserted* to be current, and a reviewer looking at a
pull request cannot see what the annotation they just changed actually produced. The failure this
project exists to close — a field rename caught by a runtime failure rather than a build failure —
comes straight back if the generated tree is invisible.

Two things had to be settled together: whether generated output lives in the repository, and what
makes it stay current.

## Decision

Generated output is committed, and `ix verify` is the gate. It regenerates every configured target
into a temporary tree and compares it byte for byte with the committed output, touching nothing in
the working copy, so it is safe to run on a dirty checkout and safe to run in CI. A difference is a
failure with the list of files that moved and the instruction to run `ix generate`. `.gitignore`
carries a comment saying `gen/` is deliberately not ignored, so the next person to tidy the ignore
file knows it is load-bearing. `make verify` runs the same check twice over: `ix verify` for the
example, and `git diff --exit-code -- '**/gen/**'` for everything the framework generates for
itself.

## Consequences

Generated code is reviewable. Changing `(transports)` on one RPC shows up in the diff as the bus
client method that appeared, and a reviewer can see the fan-out rather than infer it. A contributor
without buf or the plugins installed can still read the whole surface. `go build` works on a fresh
clone with no generate step.

The cost is real and permanent. Diffs are large — a one-line annotation change can move hundreds of
generated lines, and reviewers learn to skim them, which is its own risk. Merge conflicts in
generated files are common and are resolved by regenerating rather than by editing, which is a
convention people have to be told. And the gate is only as good as the determinism of every plugin
under it: a generator that shuffles its output fails `verify` on runs where nothing changed, and a
gate that flaps is a gate people learn to bypass. That constraint is what forced ADR-0036.

`ix verify` also checks that the committed `buf.gen.yaml` still matches `interchange.yaml`, because
two files disagreeing about what CI generates is the same failure the gate exists to stop.

## Alternatives

**Generate in CI and publish as a build artifact.** Rejected: it makes the generated surface
invisible at review time, which is exactly the property being bought. It also means the artifact
and the source can differ for a whole CI run without anyone noticing.

**Commit but do not gate.** Rejected: an unchecked committed tree drifts within weeks, and a stale
generated file that still compiles is worse than none, because it is trusted.

**Gate with a checksum manifest rather than regeneration.** Rejected: it detects hand-edits to
generated files but not a plugin whose behaviour changed, which is the case that actually bites.

## Evidence

- `.gitignore` — `bin/` and `dist/` are ignored; a comment
  states that `gen/` deliberately is not, and why.
- `Makefile` — the `verify` target: `ix verify` for the example,
  `git diff --exit-code -- '**/gen/**'` for the framework's five proto modules.
- `ix/internal/cmd/verify.go` — `Project.verify` regenerates
  into `os.MkdirTemp` and `diffOutputs` reports every file that is missing, no longer generated, or
  differs, deduplicated across nested out directories so one file is not reported twice.
- The gate is proven by mutation, not by inspection: `BUILD-PLAN.md` phase 1 records the exit
  criterion as "verified by mutation", and commit `2f73677` records that `ix verify` "fails with
  exit 1 on a one-byte mutation" after the five framework modules were made to regenerate with zero
  drift.

See ADR-0036 for the determinism this gate depends on, and ADR-0048 for why the generate step does
not touch the network — a gate that goes red because a plugin registry was busy is the same kind of
flap.
