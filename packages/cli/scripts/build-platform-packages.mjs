#!/usr/bin/env node
// Builds one npm package per platform from the binaries goreleaser produced in
// dist/. Each is a package with nothing in it but the executable, which is why
// `npm install @interchange/cli` downloads exactly one of them.
//
// Run after `goreleaser release --snapshot --clean`.

import { mkdirSync, copyFileSync, writeFileSync, existsSync, chmodSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..', '..', '..')
const version = JSON.parse(
  await import('node:fs/promises').then((fs) => fs.readFile(join(here, '..', 'package.json'), 'utf8')),
).version

// goreleaser id -> npm platform package
const TARGETS = [
  { dist: 'ix_darwin_arm64', os: 'darwin', cpu: 'arm64' },
  { dist: 'ix_darwin_amd64_v1', os: 'darwin', cpu: 'x64' },
  { dist: 'ix_linux_arm64', os: 'linux', cpu: 'arm64' },
  { dist: 'ix_linux_amd64_v1', os: 'linux', cpu: 'x64' },
  { dist: 'ix_windows_amd64_v1', os: 'win32', cpu: 'x64', exe: '.exe' },
]

let built = 0
for (const t of TARGETS) {
  const exe = t.exe ?? ''
  const src = join(root, 'dist', t.dist, `ix${exe}`)
  if (!existsSync(src)) {
    console.warn(`skipping ${t.os}-${t.cpu}: ${src} does not exist (run goreleaser first)`)
    continue
  }
  const name = `@interchange/cli-${t.os === 'win32' ? 'win32' : t.os}-${t.cpu}`
  const out = join(here, '..', 'npm', `${t.os}-${t.cpu}`)
  mkdirSync(join(out, 'bin'), { recursive: true })
  copyFileSync(src, join(out, 'bin', `ix${exe}`))
  chmodSync(join(out, 'bin', `ix${exe}`), 0o755)
  writeFileSync(
    join(out, 'package.json'),
    JSON.stringify(
      {
        name,
        version,
        description: `ix binary for ${t.os} ${t.cpu}`,
        license: 'Apache-2.0',
        os: [t.os],
        cpu: [t.cpu],
        files: ['bin'],
      },
      null,
      2,
    ) + '\n',
  )
  built++
  console.log(`built ${name}`)
}
console.log(`${built} platform package(s) written to packages/cli/npm/`)
