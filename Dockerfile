# hanzoai/cloud — the ONE unified Hanzo Cloud binary (HIP-0106).
#
# This image is a SINGLE artifact that serves BOTH the /v1 API AND the console
# UI from one process: the console is compiled into the Go binary via
# //go:embed (see webui.go). The pipeline is:
#
#   1. console  stage → build the hanzoai/console static bundle
#   2. (copied) → into  webui/dist/  of the Go build context
#   3. build    stage → `go build` bakes webui/dist into the binary (go:embed)
#
# so the final `/cloud` binary already carries the UI. No separate console
# Service, no second origin — the embedded console calls /v1 on its own host.
#
# ── console UI stage ─────────────────────────────────────────────────────────
# Builds the console SPA and emits a STATIC bundle at /out. console is fetched
# at a pinned ref (CONSOLE_REF) using the same gh_token BuildKit secret the Go
# build uses for private modules.
#
# console exposes `npm run build:embed` (scripts/build-embed.mjs): it prunes the
# Next server route handlers (BFF proxies — they collapse to the cloud /v1/* the
# SPA calls same-origin), wraps the client catch-all pages for output:'export',
# and neutralizes the root layout's request-time headers() read (the per-host
# <title>, resolved client-side in the embed) so the STATIC export prerenders
# clean — emitting out/. This stage runs it and copies out/ into /out, which the
# Go build drops into webui/dist so //go:embed bakes the FULL @hanzo/gui console
# into the ONE binary. The stage still degrades to the committed fallback shell,
# non-fatally, if build:embed is ever absent or fails (a console export
# regression must never take down the cloud backend image) — but the intended,
# working path is the real bundle.
FROM public.ecr.aws/docker/library/node:24-alpine AS console
ARG CONSOLE_REPO=https://github.com/hanzoai/console.git
ARG CONSOLE_REF=main
RUN apk add --no-cache git
WORKDIR /console
# The static export prerenders every page (webpack compile + export prerender);
# give the heap headroom so a large @hanzo/gui build never OOMs into the stub.
ENV NEXT_TELEMETRY_DISABLED=1 NODE_OPTIONS=--max-old-space-size=8192
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    git clone --depth 1 --branch "${CONSOLE_REF}" "${CONSOLE_REPO}" . && \
    npm install --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-timeout=120000
# Always emit /out. When console exposes a static-embed target AND it builds, /out
# holds the real bundle; otherwise /out stays EMPTY so the Go build keeps the
# committed fallback shell. Never fail the image — a static target that is missing
# OR that fails to build is a degrade, not an error (the standalone console
# Deployment is the primary console; this embed is a same-origin convenience). A
# console prerender/export crash (e.g. /signin Server-Components error) must NOT
# take down the cloud backend image.
RUN mkdir -p /out && \
    if npm run 2>/dev/null | grep -q ' build:embed'; then \
      echo ">> console build:embed → static bundle"; \
      if npm run build:embed && [ -d out ]; then \
        cp -r out/. /out/; \
        echo ">> embedded console static bundle"; \
      else \
        echo ">> console build:embed FAILED — degrading to committed fallback shell (non-fatal)"; \
      fi; \
    else \
      echo ">> console has no static-embed target yet; cloud embeds the fallback shell"; \
    fi

# ── Go build stage ───────────────────────────────────────────────────────────
# ECR Public mirror of the Docker library image — Docker Hub's unauthenticated
# pull rate-limit (429 toomanyrequests) fails the build on shared CI runners.
FROM public.ecr.aws/docker/library/golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates tzdata git
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
WORKDIR /src
# hanzoai/* and luxfi/* are PUBLIC and resolve via the IMMUTABLE public proxy +
# sumdb — go.sum pins those canonical hashes, so a force-re-pointed tag can never
# break the build. Routing them DIRECT (the old GOPRIVATE approach) re-fetches a
# re-tagged tree (e.g. luxfi/age@v1.5.0) whose hash differs from go.sum's proxy
# hash → "checksum mismatch / SECURITY ERROR". This matches the drop-GOPRIVATE
# fix already shipped in hanzoai/iam + luxfi/kms. Only zap-proto/* stays first-
# party-direct (kept in GOPRIVATE) — authenticated git via gh_token. GOPROXY
# still routes nested-path monorepo tags (e.g. tencentcloud-sdk-go) through the
# proxy. The committed go.sum is the single source of truth.
ENV GOPRIVATE=github.com/zap-proto/* \
    GONOSUMDB=github.com/zap-proto/* \
    GOSUMDB=off \
    GOPROXY=https://proxy.golang.org,direct \
    GOFLAGS=-mod=mod
COPY go.mod go.sum ./
# With go.sum recorded against live tag content and our orgs routed direct, this
# verifies cleanly — no runtime go.sum regeneration. (The old `rm -f go.sum`
# self-heal masked a stale go.sum and silently re-recorded unverified hashes on
# ANY transient error; removed in favor of a correct, committed go.sum.)
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go mod download
COPY . .
# Drop the console static bundle into the embed path BEFORE `go build`, so
# //go:embed all:webui/dist bakes it into the binary. /out from the console stage
# is either the real static build (then it overlays the committed fallback shell)
# or empty (then webui/dist keeps the shell that `COPY . .` already brought). The
# committed assets/.gitkeep keeps the embed's assets/ dir present either way.
COPY --from=console /out/ /src/webui/dist/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /cloud ./cmd/cloud

# ── final image ──────────────────────────────────────────────────────────────
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /cloud /cloud
EXPOSE 8080 9090 9653
USER 65532:65532
ENTRYPOINT ["/cloud"]
