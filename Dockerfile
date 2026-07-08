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
# into the ONE binary. This stage FAILS HARD: the prod image MUST carry the real
# console — a missing/broken build:embed is a build ERROR, never a silent degrade
# to the placeholder shell. The one escape hatch is --build-arg ALLOW_PLACEHOLDER=1
# (pure-Go dev image with no Node console), which is NEVER set for prod.
FROM public.ecr.aws/docker/library/node:24-alpine AS console
ARG CONSOLE_REPO=https://github.com/hanzoai/console.git
ARG CONSOLE_REF=main
RUN apk add --no-cache git
WORKDIR /console
# The static export prerenders every page (webpack compile + export prerender);
# give the heap headroom so a large @hanzo/gui build never OOMs into the stub.
ENV NEXT_TELEMETRY_DISABLED=1 NODE_OPTIONS=--max-old-space-size=8192
# Hanzo Analytics: the console's <HanzoAnalytics/> (env-gated) renders the one
# native analytics.hanzo.ai tag only when a website-id is baked in. Default to the
# console.hanzo.ai property (7dce54ee, public per-site) so console+team track on
# the next cloud build. GA4/Pixel stay off (unset). Public id, not a KMS secret.
ARG NEXT_PUBLIC_ANALYTICS_WEBSITE_ID=7dce54ee-41f6-4751-96bf-fe005067c7c7
ENV NEXT_PUBLIC_ANALYTICS_WEBSITE_ID=$NEXT_PUBLIC_ANALYTICS_WEBSITE_ID
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    git clone --depth 1 --branch "${CONSOLE_REF}" "${CONSOLE_REPO}" . && \
    npm install --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-timeout=120000
# FAIL-HARD. build:embed MUST emit a REAL bundle — a non-empty out/index.html AND
# an out/_next/ chunk dir — and /out then carries it into the Go embed path. If the
# target is absent, the export fails, or the output is the placeholder shape, this
# is a build ERROR (exit 1): the prod image can NEVER silently ship the committed
# fallback shell. Escape hatch: --build-arg ALLOW_PLACEHOLDER=1 leaves /out empty
# (Go build keeps the committed shell) for a pure-Go dev image — NEVER set in prod.
ARG ALLOW_PLACEHOLDER=0
RUN mkdir -p /out; \
    ok=0; \
    if npm run 2>/dev/null | grep -q ' build:embed'; then \
      if npm run build:embed && [ -s out/index.html ] && [ -d out/_next ]; then \
        cp -r out/. /out/; \
        echo ">> embedded REAL console static bundle: $(wc -c < out/index.html)-byte index.html, $(du -sh out/_next | cut -f1) _next/"; \
        ok=1; \
      else \
        echo ">> console build:embed produced NO real bundle (missing/empty out/index.html or out/_next)"; \
      fi; \
    else \
      echo ">> console exposes no build:embed target"; \
    fi; \
    if [ "$ok" != "1" ]; then \
      if [ "$ALLOW_PLACEHOLDER" = "1" ]; then \
        echo ">> ALLOW_PLACEHOLDER=1 — keeping committed fallback shell (DEV image only; NEVER prod)"; \
      else \
        echo ">> FATAL: refusing to ship the placeholder console. Fix the console build:embed, or pass --build-arg ALLOW_PLACEHOLDER=1 for a pure-Go dev image."; \
        exit 1; \
      fi; \
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
