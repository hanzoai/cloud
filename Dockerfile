# syntax=docker/dockerfile:1
# Multi-arch OSS container build for github.com/hanzoai/cloud.
# Pure-Go CGO_ENABLED=0 links github.com/hanzoai/sqlite + sqlcipher so encryption
# at rest (CLOUD_KMS_MASTER_KEY_REF) works out of the box with no C libraries.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/hanzoai/cloud.Version=${VERSION}" \
    -o /out/cloud ./cmd/cloud

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/hanzoai/cloud" \
      org.opencontainers.image.title="Hanzo Cloud (OSS)" \
      org.opencontainers.image.description="Unified local-first Hanzo Cloud server (HIP-0106)" \
      org.opencontainers.image.licenses="Apache-2.0 OR MIT"

COPY --from=build --chown=65532:65532 /out/cloud /cloud

ENV CLOUD_LISTEN=:8080 \
    CLOUD_DATA_DIR=/var/lib/cloud

EXPOSE 8080 9090 9653
USER 65532:65532
ENTRYPOINT ["/cloud"]
