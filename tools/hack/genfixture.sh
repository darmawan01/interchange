#!/usr/bin/env bash
# Regenerates the Go types for the plugin test fixture.
#
# The fixture imports two annotations that live in two different buf modules,
# so it is compiled in a scratch tree that puts every .proto at the import
# path it is written as. That keeps the fixture out of the workspace: a test
# input in a published module would be generated into core's tree by the
# repo's own `buf generate`.
#
# Run from anywhere:  tools/hack/genfixture.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/interchange/transport/v1" "$work/interchange/cli/v1" "$work/interchange/fixture/v1"
cp "$root"/api/interchange/transport/v1/*.proto              "$work/interchange/transport/v1/"
cp "$root"/tools/api/interchange/cli/v1/*.proto              "$work/interchange/cli/v1/"
cp "$root"/tools/testdata/proto/interchange/fixture/v1/*.proto "$work/interchange/fixture/v1/"

cat > "$work/buf.gen.yaml" <<'YAML'
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/darmawan01/interchange/tools/testdata/gen
    # The annotations already have committed Go. protoc-gen-go emits a blank
    # import of an annotation's package so its extensions are registered, so
    # these two must point at the real packages or the fixture will not build.
    - file_option: go_package_prefix
      path: interchange/transport
      value: github.com/darmawan01/interchange/gen/go
    - file_option: go_package_prefix
      path: interchange/cli
      value: github.com/darmawan01/interchange/tools/gen/go
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.12
    out: gen
    opt: [paths=source_relative]
YAML

# --path limits generation to the fixture: the annotations already have
# committed Go elsewhere, and a second copy of an extension type in one binary
# is a registry conflict at init.
(cd "$work" && buf generate --template buf.gen.yaml --path interchange/fixture/v1)

# Only the message packages are replaced: the plugin goldens live in
# sub-packages of the same tree and are rewritten by UPDATE_GOLDEN=1 go test.
rm -f "$root"/tools/testdata/gen/interchange/fixture/v1/*.pb.go
mkdir -p "$root/tools/testdata/gen/interchange/fixture/v1"
cp "$work"/gen/interchange/fixture/v1/*.pb.go "$root/tools/testdata/gen/interchange/fixture/v1/"
echo "regenerated tools/testdata/gen"
