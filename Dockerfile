# hanzoai/cloud — the ONE unified Hanzo Cloud binary (HIP-0106).
#
# This image is a SINGLE artifact that serves BOTH the /v1 API AND the console
# UI from one process: the console is compiled into the Go binary via
# //go:embed (see webui.go). The pipeline is:
#
#   1. console  stage → build the hanzoai/console2 static bundle
#   2. (copied) → into  webui/dist/  of the Go build context
#   3. build    stage → `go build` bakes webui/dist into the binary (go:embed)
#
# so the final `/cloud` binary already carries the UI. No separate console
# Service, no second origin — the embedded console calls /v1 on its own host.
#
# ── console UI stage ─────────────────────────────────────────────────────────
# Builds the console2 SPA and emits a STATIC bundle at /out. console2 is fetched
# at a pinned ref (CONSOLE2_REF) using the same gh_token BuildKit secret the Go
# build uses for private modules.
#
# STATE: console2 is a STATIC EXPORT (v8.4.9+). Its 18 Next BFF server routes were
# collapsed to same-origin /v1 — the browser calls its OWN origin /v1/<head> and the
# cloud binary serves it directly, validating the first-party IAM session cookie
# (middleware_identity.go) — so `output: 'export'` emits a pure SPA at out/ and
# `npm run build:embed` produces it. This stage runs build:embed and copies out/ into
# the Go embed path; the SAME image then serves the full @hanzo/gui console + /v1 from
# one binary. The build:embed detection is retained so the stage NEVER fails the image:
# an older console2 ref without build:embed degrades to the committed fallback shell
# (webui/dist/index.html) rather than erroring. Source maps are stripped from the
# embed (not needed to serve; keeps the binary lean).
FROM public.ecr.aws/docker/library/node:24-alpine AS console
ARG CONSOLE2_REPO=https://github.com/hanzoai/console2.git
ARG CONSOLE2_REF=main
RUN apk add --no-cache git
WORKDIR /console
ENV NEXT_TELEMETRY_DISABLED=1 NODE_OPTIONS=--max-old-space-size=6144
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    git clone --depth 1 --branch "${CONSOLE2_REF}" "${CONSOLE2_REPO}" . && \
    npm install --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-timeout=120000
# Always emit /out. When console2 exposes a static-embed target it holds the real
# bundle; otherwise /out stays EMPTY so the Go build keeps the committed fallback
# shell. Never fail the image (a missing static target is a degrade, not an error).
RUN mkdir -p /out && \
    if npm run 2>/dev/null | grep -q ' build:embed'; then \
      echo ">> console2 build:embed → static bundle"; \
      npm run build:embed && \
      find out -name '*.map' -delete && \
      cp -r out/. /out/; \
    else \
      echo ">> console2 has no static-embed target yet; cloud embeds the fallback shell"; \
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
