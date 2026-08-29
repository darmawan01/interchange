# The gate that makes the contract real. Every target here is also a CI step;
# nothing in CI does something you cannot run locally with one word.

GO      ?= go
BIN     := bin
MODULES := . ./auth ./binding/rest ./driver/mqtt ./driver/nats ./driver/ws ./errors \
           ./examples/catalog ./frontend/dsl ./frontend/openapi ./ix ./tools ./validate

.PHONY: all
all: plugins generate lint test

## plugins: build the local protoc plugins into ./bin, which buf invokes by path.
.PHONY: plugins
plugins:
	$(GO) build -o $(BIN)/protoc-gen-bus   ./tools/cmd/protoc-gen-bus
	$(GO) build -o $(BIN)/protoc-gen-cli   ./tools/cmd/protoc-gen-cli
	$(GO) build -o $(BIN)/protoc-gen-authz ./auth/cmd/protoc-gen-authz

## ix: build the CLI.
.PHONY: ix
ix:
	$(GO) build -o $(BIN)/ix ./ix/cmd/ix

.PHONY: generate
generate: plugins ix
	$(BIN)/ix generate

.PHONY: fmt
fmt:
	$(BIN)/ix fmt
	$(GO) fmt ./...

.PHONY: lint
lint: ix
	$(BIN)/ix lint

.PHONY: breaking
breaking: ix
	$(BIN)/ix breaking --against 'origin/main'

## verify: the drift gate. Generated output is COMMITTED; this is what makes
## that true rather than aspirational, and it is the whole reason the contract
## cannot drift.
.PHONY: verify
verify: plugins ix
	$(BIN)/ix verify

## test: every module. A single `go test ./...` only covers the one you are in.
.PHONY: test
test:
	@set -e; for m in $(MODULES); do \
	  echo "== $$m"; (cd $$m && $(GO) test ./...); \
	done

.PHONY: vet
vet:
	@set -e; for m in $(MODULES); do (cd $$m && $(GO) vet ./...); done

## depcheck: the dependency rule, enforced. Core imports no broker client, no
## HTTP router, no policy engine and no auth module.
.PHONY: depcheck
depcheck:
	./hack/depcheck.sh

.PHONY: clean
clean:
	rm -rf $(BIN)
