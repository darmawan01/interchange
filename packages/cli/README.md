# `@interchange/cli`

`ix` for people who do not have a Go toolchain and should not need one.

```bash
npx @interchange/cli init
npx @interchange/cli generate
```

The package itself is a thin launcher. The binary arrives as a platform-specific optional
dependency, so `npm install` downloads one executable and nothing else — no build step, no
`node-gyp`, no postinstall download.

| Platform | Package |
| --- | --- |
| macOS arm64 | `@interchange/cli-darwin-arm64` |
| macOS x64 | `@interchange/cli-darwin-x64` |
| Linux arm64 | `@interchange/cli-linux-arm64` |
| Linux x64 | `@interchange/cli-linux-x64` |
| Windows x64 | `@interchange/cli-win32-x64` |

If a locally built `bin/ix` exists at the repo root, the launcher prefers it — so a contributor
running `make ix` exercises the code in front of them rather than a published release.

## Releasing

```bash
goreleaser release --snapshot --clean      # builds dist/ix_<os>_<arch>/ix
npm --prefix packages/cli run build:platforms
npm publish packages/cli/npm/*             # then the launcher package itself
```

Publishing is deliberately not wired into CI while the name is still a working name.
