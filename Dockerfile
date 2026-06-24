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
#     here: every luxfi module we use IS on the public proxy, so it resolves
#     through proxy.golang.org — the immutable, checksum-DB-verified artifact.
#     That neutralises force-rewritten upstream tags: e.g. luxfi/age v1.5.0 was
#     re-pushed on GitHub with content differing from what sum.golang.org
#     recorded (h1:G69H... original vs h1:KEjq... rewritten), which made the old
#     direct fetch fail go.sum verification. Proxy fetch returns the original
#     go.sum-matching bits, so the build is deterministic again.
#   GONOSUMDB = all three orgs: skip the public sumdb lookup for cross-org repos
#     (private ones 404 the sumdb; luxfi is already in go.sum so this is a no-op
#     safety net for any future re-tag).
#   GOPROXY = proxy first, direct fallback. GONOPROXY still forces hanzoai/*
#     and zap-proto/* to direct; everything else (incl. luxfi/*) hits the proxy.
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
