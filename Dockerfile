# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates tzdata git
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
WORKDIR /src

# Private cross-org modules (hanzoai/*, luxfi/*, zap-proto/*) are fetched via
# authenticated git, bypassing the public proxy (which 404s on private repos).
# gh_token is the BuildKit secret the release workflow injects from GH_PAT;
# no-op when absent (local/dev builds with a warm module cache).
ENV GOPRIVATE=github.com/hanzoai/*,github.com/luxfi/*,github.com/zap-proto/*
# Resolve through the public proxy FIRST, falling back to direct (authenticated
# git) only when the proxy 404s — i.e. for genuinely private repos. This pins
# PUBLIC modules to the immutable, checksum-DB-verified bits the proxy serves,
# so a force-rewritten upstream tag (e.g. luxfi/age v1.5.0 re-pushed on GitHub
# with different content than sum.golang.org recorded) can never poison the
# build: the proxy still serves the original go.sum-matching artifact. GOPRIVATE
# keeps private paths off the sumdb; the proxy never sees them (it 404s → direct).
ENV GOPROXY=https://proxy.golang.org,direct
# -mod=mod lets `go` re-record go.sum entries at build time for private modules
# whose tags are re-pushed upstream (e.g. luxfi/threshold), instead of failing
# on a stale checksum. GOPRIVATE already keeps these off the public sumdb.
ENV GOFLAGS=-mod=mod
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /cloud ./cmd/cloud

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
# --chmod=0755 guarantees the binary is executable in the scratch image. A prior
# build shipped its binary at mode 0644 -> "exec: permission denied" CrashLoop;
# be explicit so the unified binary can never regress to non-executable.
COPY --from=build --chmod=0755 /cloud /cloud
EXPOSE 8080 9090 9653
USER 65532:65532
ENTRYPOINT ["/cloud"]
