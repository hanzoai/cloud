# syntax=docker/dockerfile:1
# Multi-arch OSS container build for github.com/hanzoai/cloud (ghcr.io/hanzoai/cloud-oss:main).
#
# Ships the light-host router (/cloud) plus one static pure-Go binary per OSS
# subsystem in /, using ONLY unauthenticated public Go modules from proxy.golang.org.
# Pure-Go CGO_ENABLED=0 links github.com/hanzoai/sqlite's pure-Go SQLCipher VFS
# (-tags "sqlite_fts5 sqlite_math_functions") so at-rest encryption works out of
# the box with CLOUD_KMS_MASTER_KEY_REF and zero C shared libraries.

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown

ENV GOTOOLCHAIN=auto \
    GOENV=off \
    GOWORK=off \
    GOPROXY=https://proxy.golang.org \
    GOSUMDB=sum.golang.org \
    GOFLAGS=-mod=readonly \
    CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=cloud-oss-gomod,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,id=cloud-oss-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=cloud-oss-gobuild,target=/root/.cache/go-build \
    set -eu; \
    mkdir -p /out; \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -tags "sqlite_fts5 sqlite_math_functions" \
      -ldflags="-s -w -X github.com/hanzoai/cloud/sdk/env.version=${VERSION} -X github.com/hanzoai/cloud/sdk/env.revision=${REVISION}" \
      -o /out/cloud ./cmd/cloud; \
    spawned="$(sed -n '/Coresident: *true/d; s/.*{Name: "\([^"]*\)".*/\1/p' manifest/apps.go)"; \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -tags "sqlite_fts5 sqlite_math_functions" \
      -ldflags="-s -w" \
      -o /out/ $(printf './plugin/%s ' $spawned); \
    touch -d @0 /out/*

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata git tini && \
    addgroup -g 65532 -S nonroot && \
    adduser -u 65532 -S nonroot -G nonroot && \
    mkdir -p /var/lib/cloud/run && \
    chown -R 65532:65532 /var/lib/cloud

LABEL org.opencontainers.image.source="https://github.com/hanzoai/cloud" \
      org.opencontainers.image.title="Hanzo Cloud (OSS)" \
      org.opencontainers.image.description="Light-host ZAP plugin router and OSS Cloud fleet (HIP-0106)" \
      org.opencontainers.image.licenses="Apache-2.0 OR MIT"

COPY --from=build --chown=65532:65532 /out/ /

ENV CLOUD_LISTEN=:8080 \
    CLOUD_DATA_DIR=/var/lib/cloud \
    ZIP_RUNTIME_DIR=/var/lib/cloud/run

EXPOSE 8080 9090 9653 9092 5672
USER 65532:65532
ENTRYPOINT ["/sbin/tini", "--", "/cloud"]
