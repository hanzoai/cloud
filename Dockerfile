# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates tzdata git
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
WORKDIR /src

# Module resolution is split by trust/availability, NOT lumped into one
# GOPRIVATE (which forces BOTH direct-fetch and sumdb-bypass for every match):
#
#   GONOPROXY = hanzoai/* + zap-proto/* ONLY. These pseudo-versions are pinned
#     to just-pushed commits the public proxy can't serve (404), so they must be
#     fetched via authenticated git (gh_token rewrite below). luxfi/* is NOT
#     here: most luxfi modules we use ARE on the public proxy, so they resolve
#     through proxy.golang.org — the immutable, checksum-DB-verified artifact.
#     That neutralises force-rewritten upstream tags: e.g. luxfi/age v1.5.0 and
#     luxfi/threshold v1.9.4 were re-pushed on GitHub with content differing
#     from what sum.golang.org recorded, which made the old direct fetch fail
#     go.sum verification. Proxy fetch returns the original go.sum-matching bits,
#     so the build is deterministic again. (go.sum is pinned to the proxy /
#     checksum-DB hashes for these, NOT the rewritten GitHub bits.)
#   GONOSUMDB = all three orgs: skip the public sumdb lookup for cross-org repos.
#     This is REQUIRED for luxfi too — a few luxfi module versions are NOT on the
#     proxy/sumdb (e.g. luxfi/constants@v1.5.8 → 404); GONOSUMDB lets those fall
#     through to direct without a sumdb-lookup error, while go.sum still pins them.
#   GOPROXY = proxy first, direct fallback. GONOPROXY still forces hanzoai/*
#     and zap-proto/* to direct; everything else (incl. luxfi/*) tries the proxy
#     first and only falls to direct when the proxy 404s.
#
# gh_token is the BuildKit secret the release workflow injects from GH_PAT;
# no-op when absent (local/dev builds with a warm module cache).
ENV GONOPROXY=github.com/hanzoai/*,github.com/zap-proto/*
ENV GONOSUMDB=github.com/hanzoai/*,github.com/luxfi/*,github.com/zap-proto/*
ENV GOPROXY=https://proxy.golang.org,direct
# -mod=mod lets `go` re-record go.sum entries at build time for private modules
# whose tags are re-pushed upstream (e.g. luxfi/threshold), instead of failing
# on a stale checksum. GONOSUMDB already keeps these off the public sumdb.
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
