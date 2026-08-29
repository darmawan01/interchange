# `docker run ghcr.io/darmawan01/ix generate` -- for CI without a local
# install. The image carries buf too, because ix shells out to it: an image
# that needs a second tool installed beside it is not a distribution channel.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ix ./ix && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/protoc-gen-bus ./tools/cmd/protoc-gen-bus && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/protoc-gen-cli ./tools/cmd/protoc-gen-cli && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/protoc-gen-authz ./auth/cmd/protoc-gen-authz

FROM bufbuild/buf:latest AS buf

FROM alpine:3.21
RUN apk add --no-cache git ca-certificates
COPY --from=buf /usr/local/bin/buf /usr/local/bin/buf
COPY --from=build /out/ /usr/local/bin/
WORKDIR /work
ENTRYPOINT ["ix"]
CMD ["--help"]
