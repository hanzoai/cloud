# syntax=docker/dockerfile:1
# Hanzo Commerce - E-commerce Platform
# Multi-stage build for minimal production image

# ── Stage 1: Admin SPA — use pre-built static export from app/admin/out ──
# The admin pnpm workspace has known lockfile drift against an upstream
# ui-docs dep that 404s on npm — rebuilding it inside the image would
# block every commerce build until that workspace is repaired. The
# committed app/admin/out/ tree IS the canonical Next.js static export
# (rebuilt out-of-band via `pnpm -F admin build` from a clean checkout
# with the lockfile freshly regenerated). Copying it directly here
# keeps the image deterministic without depending on the npm registry's
# moods. Re-enable the in-Docker build by uncommenting the original
# stage when the workspace lockfile is restored.
FROM busybox AS admin-build
COPY app/admin/out /web/admin/out

# ── Stage 2: Build pay UI (Vite SPA from hanzoai/pay) ────────────────────
# Canonical source lives at github.com/hanzoai/pay. Forks override PAY_REPO
# and PAY_VERSION via --build-arg; default tracks the latest tagged release.
FROM node:22-alpine AS pay-build
ARG PAY_REPO=https://github.com/hanzoai/pay.git
ARG PAY_VERSION=v0.1.2
WORKDIR /pay
RUN apk add --no-cache git && corepack enable pnpm
# hanzoai/pay is private. Mount the netrc build secret so git over HTTPS
# authenticates; the secret never lands in a layer.
RUN --mount=type=secret,id=netrc,target=/root/.netrc \
    git clone --depth=1 --branch=${PAY_VERSION} ${PAY_REPO} /pay
RUN pnpm install --frozen-lockfile && pnpm build

# ── Stage 3: Build billing admin UI (Next.js export from hanzoai/billing) ─
# Canonical source lives at github.com/hanzoai/billing. Forks override
# BILLING_REPO + BILLING_VERSION via --build-arg; default tracks the latest
# tagged release. The Next config emits a static bundle under out/ with
# basePath=/admin/billing, which commerce serves under the same prefix from
# billing/ui/dist (go:embed target).
#
# pnpm pinned to 9.15.4 (matches billing's own Dockerfile + lockfile
# config schema). corepack's default pnpm 11.5.2 rejects v9-shaped
# `overrides` blocks with ERR_PNPM_LOCKFILE_CONFIG_MISMATCH; pinning
# back to 9.x keeps the schema validator on the right side of the
# v10 break.
FROM node:22-alpine AS billing-build
ARG BILLING_REPO=https://github.com/hanzoai/billing.git
ARG BILLING_VERSION=v0.1.2
WORKDIR /billing
# python3/make/g++ needed for node-gyp on arm64 where bufferutil/utf-8-validate
# have no prebuilt aarch64 binary and fall back to source compile.
RUN apk add --no-cache git python3 make g++ && corepack enable && corepack prepare pnpm@9.15.4 --activate
# hanzoai/billing is private — mount the netrc build secret for git over HTTPS.
RUN --mount=type=secret,id=netrc,target=/root/.netrc \
    git clone --depth=1 --branch=${BILLING_VERSION} ${BILLING_REPO} /billing
RUN pnpm install --frozen-lockfile && pnpm build

# ── Stage 4: Build Go binary (with embedded admin + pay + billing SPAs) ──
FROM golang:1.26.4-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata gcc musl-dev

ARG TARGETARCH

WORKDIR /build

# Copy go mod files first for layer caching. thirdparty/ethereum (the LGPL-
# adjacent go-ethereum nested module) was ripped out; on-chain HUSD/ERC-20 now
# rides luxfi/cevm (Apache-2.0), a normal go-module dep resolved via the proxy —
# no nested-module go.mod/go.sum to COPY.
COPY go.mod go.sum ./

# Private hanzoai Go modules (cloud, zip, base, tasks, …) need git auth to
# resolve. Mount the netrc build secret so git over HTTPS authenticates for
# private fetches; GOPRIVATE skips the public proxy + sumdb for hanzoai/* so
# those go straight to git. Public modules still flow through the proxy with
# normal sum verification. GOTOOLCHAIN=local pins the builder's own
# golang:1.26.4 toolchain so go does NOT try to download+verify a toolchain
# module (which fails as a sumdb "SECURITY ERROR"). The netrc is mounted only
# for the duration of the step and never lands in a layer.
# GOWORK=off is critical: the repo commits a go.work (use . ./metering).
# Without disabling it, `go mod download` runs in workspace mode, reads the
# stale committed go.work.sum, and fails sum verification ("SECURITY ERROR").
# The image builds the single root module, so force module mode.
ENV GOPRIVATE=github.com/hanzoai/* \
    GOTOOLCHAIN=local \
    GOWORK=off
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=secret,id=netrc,target=/root/.netrc \
    go mod download

# Copy source code (note: api/billing/plans/ is gitignored — the pre-build
# step in the CI workflow curls the @hanzo/plans npm tarball into
# api/billing/plans/, so this COPY picks up the canonical SOT for
# go:embed at compile time. Local dev mirrors via scripts/fetch-plans.sh —
# public package, no auth needed).
COPY . .

# Populate api/billing/plans/*.json (gitignored, so absent from the git
# build context) from the public @hanzo/plans npm tarball. api/billing/
# plans.go go:embed's plans/dns.json + plans/subscription.json at compile
# time, so this MUST run before `go build`. No auth (public npm).
# Pin to the highest published version (the script's internal default tracked
# an unpublished tag and 404'd); override with --build-arg PLANS_VERSION=.
ARG PLANS_VERSION=1.1.4
RUN apk add --no-cache curl && sh scripts/fetch-plans.sh

# Replace placeholder dist/ with the real Next.js export so go:embed picks up
# the actual SPA bundle at compile time.
RUN rm -rf admin/dist
COPY --from=admin-build /web/admin/out admin/dist

# Overlay the pay UI build into checkout/ui/dist so go:embed in
# checkout/embed.go picks up the real SPA bundle.
RUN rm -rf checkout/ui/dist && mkdir -p checkout/ui/dist
COPY --from=pay-build /pay/dist/ checkout/ui/dist/

# Overlay the billing admin UI build into billing/ui/dist so go:embed in
# billing/embed.go picks up the real Next.js export.
RUN rm -rf billing/ui/dist && mkdir -p billing/ui/dist
COPY --from=billing-build /billing/out/ billing/ui/dist/

# Build the binary with CGO for SQLite support.
#
# `-tags cloud` compiles in the HIP-0106 cloud-mount path (cloud_boot.go);
# the binary remains backwards-compatible (legacy direct-Gin is the
# default boot mode) but operators can now flip --cloud or
# COMMERCE_MODE=cloud to serve commerce through a zip.App with gin
# adapted as the inner handler. Phase 1 of the staged Gin → zip
# migration — see mount.go for the contract and cmd/commerce/main.go
# for the dispatcher.
#
# Pass `./cmd/commerce` (the package) instead of `./cmd/commerce/main.go`
# so cloud_boot.go / cloud_stub.go / legacy.go get compiled into the
# same binary alongside main.go.
# VERSION is injected by CI (docker-deploy.yml) from the immutable image
# tag so the binary's /healthz version == its deployed tag. Empty default
# keeps commerce.Version's in-source default for local builds.
ARG VERSION=""
# -mod=mod lets the Linux build complete go.sum itself (adding any go.mod
# hashes the build graph needs but that are absent from the committed go.sum).
# Private hanzoai modules are direct-git (GOPRIVATE) and their zip hashes differ
# by host OS, so go.sum cannot be regenerated reliably off-Linux — letting the
# build add the correct Linux hashes is the robust fix.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=secret,id=netrc,target=/root/.netrc \
    CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} \
    CGO_CFLAGS="-D_LARGEFILE64_SOURCE -D_GNU_SOURCE" \
    VER="${VERSION#v}" && \
    go build -mod=mod -p=8 \
    -tags "cloud sqlite_omit_load_extension" \
    -ldflags="-s -w \
      ${VER:+-X github.com/hanzoai/commerce.Version=${VER}} \
      -X github.com/hanzoai/commerce.GitCommit=$(git rev-parse --short HEAD 2>/dev/null || echo sandboxfix) \
      -X github.com/hanzoai/commerce.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /build/commerce \
    ./cmd/commerce

# Production stage
FROM alpine:3.21

LABEL org.opencontainers.image.source="https://github.com/hanzoai/commerce"

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata curl

# Create non-root user
RUN addgroup -S hanzo && adduser -S hanzo -G hanzo

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/commerce /app/commerce

# Copy templates and static assets
COPY --from=builder /build/templates /app/templates
COPY --from=builder /build/api/templates /app/api/templates

# Create data directories
RUN mkdir -p /app/data /app/logs && \
    chown -R hanzo:hanzo /app

USER hanzo

# Expose default port
EXPOSE 8001

# Environment variables
ENV COMMERCE_DIR=/app/data
ENV COMMERCE_DEV=false
ENV PORT=8001

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8001/healthz || exit 1

# Default command. cmd/commerce/main.go is flag.Parse() only; positional
# args are ignored. Pass the listen address as a --http flag so the
# binary doesn't fall back to the 127.0.0.1:8090 default and CrashLoopBackOff
# at rollout. ENTRYPOINT + CMD form a single argv: /app/commerce --http 0.0.0.0:8001.
ENTRYPOINT ["/app/commerce"]
CMD ["--http", "0.0.0.0:8001"]
