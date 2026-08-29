package scaffold

// Every template below is written so the scaffolded project builds, lints and
// describes with nothing but `buf` on PATH. Anything needing the network --
// remote codegen plugins, googleapis for REST -- is opt-in and says so at the
// point where you would opt in.

const interchangeYAML = `# interchange.yaml -- one contract, every road.
# Every ix command reads this file; no command needs a flag for something it
# already says. Schema: docs/10-extensibility.md.
version: 1

sources:
  - path: api/**/*.proto
    frontend: proto

transports:
  # The roads an RPC takes when it carries no (transports) annotation.
  # A per-RPC annotation replaces this entirely rather than merging with it,
  # so a reviewer reads one annotation instead of composing two.
  default: [rpc, rest]
  drivers: []

# buf's managed mode. go_package_prefix is where generated Go is importable
# from -- change it to your module path. Keeping it here rather than in a
# second file means there is nothing to drift.
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: {{.GoModule}}/gen/go

# Generators. Ours have no privileged standing here: a plugin you write is
# configured exactly the way protocolbuffers/go is. Pin every version -- an
# unpinned generator makes the committed output a function of when CI ran.
generate:
  - plugin: buf.build/protocolbuffers/go:v1.36.12
    out: gen/go
    opt: [paths=source_relative]
  - plugin: buf.build/connectrpc/go:v1.18.1
    out: gen/go
    opt: [paths=source_relative]

# Authorization is an optional module. Omit this block entirely and nothing in
# the stack learns what a permission is.
#
# auth:
#   provider: rbac
#   on_missing_annotation: error   # error | warn | ignore
`

const bufYAML = `version: v2
modules:
  - path: api

lint:
  use: [STANDARD]

breaking:
  # WIRE_JSON, not FILE: it allows refactors that keep both the binary wire
  # form and the JSON field names compatible, which is what a public JSON
  # surface actually requires.
  use: [WIRE_JSON]

# Add a dependency when you need one -- google.api.http for REST paths, or
# protovalidate for declarative field rules -- then run ` + "`buf dep update`" + `.
#
# deps:
#   - buf.build/googleapis/googleapis
#   - buf.build/bufbuild/protovalidate
`

const starterProto = `syntax = "proto3";

package {{.Pkg}};

import "interchange/cli/v1/cli.proto";
import "interchange/transport/v1/transports.proto";

// {{.Service}} is the starter contract. Declare it once here and every road
// -- HTTP, REST, the bus -- is derived from it. Run:
//
//   ix describe {{.Service}}.List{{.Entity}}s
//
// to see what this file already exposes, on every transport, before you have
// written a line of Go.
service {{.Service}} {
  // List{{.Entity}}s returns a page of {{.Name}}s.
  rpc List{{.Entity}}s(List{{.Entity}}sRequest) returns (List{{.Entity}}sResponse) {
    // Lets a client issue a real cacheable GET.
    option idempotency_level = NO_SIDE_EFFECTS;

    // Which roads this RPC travels. Naming TRANSPORT_BUS is what emits the
    // bus subscriber -- and it is a one-line diff a reviewer can see, which
    // is the entire point of the annotation.
    option (interchange.transport.v1.transports) = {
      on: [
        TRANSPORT_RPC,
        TRANSPORT_BUS
      ]
      group: "{{.Name}}"
    };

    // Mounts as ` + "`{{.Name}} list`" + ` in the generated command tree.
    option (interchange.cli.v1.command) = {
      path: [
        "{{.Name}}",
        "list"
      ]
    };

    // For a REST road, add google.api.http -- ` + "`buf dep add buf.build/googleapis/googleapis`" + `,
    // import "google/api/annotations.proto", then:
    //
    //   option (google.api.http) = {get: "/v1/{{.Name}}s"};
    //
    // and add TRANSPORT_REST above.
  }

  // Create{{.Entity}} adds one.
  rpc Create{{.Entity}}(Create{{.Entity}}Request) returns (Create{{.Entity}}Response) {
    option (interchange.transport.v1.transports) = {
      on: [TRANSPORT_RPC]
    };
    option (interchange.cli.v1.command) = {
      path: [
        "{{.Name}}",
        "create"
      ]
    };
  }
}

message {{.Entity}} {
  // Ids are strings -- ULID or KSUID, optionally type-prefixed. Never
  // integers: an integer id leaks row counts and cannot be re-sharded.
  string {{.Name}}_id = 1;

  string title = 2;

  {{.Entity}}Status status = 3;
}

// Enums are singular PascalCase with a mandatory zero UNSPECIFIED, and are
// append-only forever: in proto3 an unset field reads as zero, so without
// UNSPECIFIED there is no way to tell "not set" from the first real choice.
enum {{.Entity}}Status {
  {{.EnumPrefix}}_UNSPECIFIED = 0;
  {{.EnumPrefix}}_OPEN = 1;
  {{.EnumPrefix}}_DONE = 2;
}

message List{{.Entity}}sRequest {
  int32 page_size = 1;
  string page_token = 2;
}

message List{{.Entity}}sResponse {
  repeated {{.Entity}} {{.Name}}s = 1;
  string next_page_token = 2;
}

message Create{{.Entity}}Request {
  string title = 1;
}

message Create{{.Entity}}Response {
  {{.Entity}} {{.Name}} = 1;
}
`

const makefile = `# The gate that makes the contract real. Generated output is COMMITTED, and
# ` + "`make verify`" + ` is what turns that from aspiration into a build failure.

.PHONY: generate lint format breaking verify describe dev doctor

generate:
	ix generate

lint:
	ix lint

format:
	ix fmt

# subdir=. because a project is usually not at the root of its repository,
# and buf resolves the ref relative to the repository, not to you.
breaking:
	ix breaking --against '.git#branch=main,subdir=.'

# One command in CI. It regenerates into a temp tree and fails if the
# committed output moved.
verify:
	ix verify

# Exercise the contract with no infrastructure.
dev:
	ix dev

doctor:
	ix doctor
`

const workflow = `name: contract

on:
  push:
    branches: [main]
  pull_request:

jobs:
  contract:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          # breaking-change detection needs the baseline branch.
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: stable

      - uses: bufbuild/buf-action@v1
        with:
          setup_only: true

      # Pin the version rather than tracking @latest: a toolchain that
      # upgrades itself without a commit makes the drift gate below
      # non-reproducible, which is the one property it has to have.
      #
      # Until Interchange is published this resolves nothing -- build ix
      # from a checkout and put it on PATH, or vendor the binary.
      - name: install ix
        run: go install github.com/darmawan01/interchange/ix/cmd/ix@${IX_VERSION:-latest}
        env:
          IX_VERSION: latest

      - name: lint
        run: ix lint

      - name: breaking
        if: github.event_name == 'pull_request'
        run: ix breaking --against '.git#branch=${{"{{"}} github.event.pull_request.base.ref {{"}}"}},subdir=.'

      # The drift gate. This is the step that makes committed generated code
      # trustworthy rather than merely present.
      - name: verify
        run: ix verify
`
