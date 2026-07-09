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
FROM public.ecr.aws/docker/library/node:24-alpine@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd AS console
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

# ── Go build stage (CGO=1 + SQLCipher — REAL at-rest encryption) ─────────────
# The unified binary embeds IAM (clients/iam) whose per-org store is SQLCipher-
# encrypted (orgIsolation=sqlite), and commerce's per-tenant money DBs likewise.
# A CGO=0 modernc build SILENTLY SHIPS PLAINTEXT. So this builds CGO=1 against
# system libsqlcipher — hanzoai/iam's proven recipe: the `libsqlite3` tag + a
# libsqlcipher symlink + -DSQLITE_HAS_CODEC, with the modernc double-registration
# guard, TestEncryptionProof, and the cek.go golden-vector KAT baked in — so a
# build that fails to link REAL SQLCipher, or that would decrypt existing stores
# differently, produces NO image. alpine3.22 MATCHES the runtime base so the
# libsqlcipher soname the binary links is the SAME one present at runtime. ECR
# Public mirror avoids Docker Hub's 429 rate-limit on shared CI runners.
# ---- agent-skills stage: regenerate the FULL /.well-known/agent-skills catalog
# from the hanzoai/openapi SOT (skills.py) and carry it into the Go embed path
# BEFORE `go build`, the SAME way the console bundle is produced. The committed
# catalog is only the tiny `ai` fallback; prod must embed the full set. FAIL-HARD:
# if the clone/generation can't produce the master index, the image is not built.
FROM public.ecr.aws/docker/library/python:3.12-alpine AS skills
ARG OPENAPI_REPO=https://github.com/hanzoai/openapi.git
ARG OPENAPI_REF=main
RUN apk add --no-cache git && pip install --no-cache-dir pyyaml
WORKDIR /openapi
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    git clone --depth 1 --branch "${OPENAPI_REF}" "${OPENAPI_REPO}" . && \
    python3 skills.py --no-services --out /catalog && \
    test -s /catalog/hanzo/index.json

FROM public.ecr.aws/docker/library/golang:1.26-alpine3.22@sha256:727cfc3c40be55cd1bc9a4a059406b28a059857e3be752aa9d09531e12c20c56 AS build
RUN apk add --no-cache ca-certificates tzdata git gcc musl-dev sqlcipher-dev pkgconfig binutils
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
# mattn/go-sqlite3's `libsqlite3` tag hard-codes `-lsqlite3`, but alpine's
# sqlcipher-dev ships ONLY libsqlcipher (no libsqlite3.so). Symlink so the link
# resolves -lsqlite3 to libsqlcipher — REAL encryption. Do NOT `apk add sqlite-dev`
# (a plaintext libsqlite3 would silently disable the codec; the gate below catches it).
RUN set -eux; \
    SC="$(find /usr/lib /lib -name 'libsqlcipher.so*' 2>/dev/null | sort | head -1)"; \
    test -n "$SC"; \
    ln -sf "$SC" /usr/lib/libsqlite3.so; \
    ln -sf "$SC" /usr/lib/libsqlite3.so.0
WORKDIR /src
# hanzoai/* and luxfi/* are PUBLIC and resolve via the IMMUTABLE public proxy +
# sumdb — go.sum pins those canonical hashes, so a force-re-pointed tag can never
# break the build. GOSUMDB stays ON (a money image must not blanket-disable the
# checksum database); only zap-proto/* is exempt (first-party-direct via GOPRIVATE,
# authenticated git over gh_token). -mod=readonly means the committed go.sum is the
# SOLE source of truth: any drift (a needed hash not present) FAILS the build
# instead of being silently re-recorded. CGO_CFLAGS/LDFLAGS enable the SQLCipher
# codec + URI keying.
ENV CGO_CFLAGS="-DSQLITE_HAS_CODEC -DSQLITE_USE_URI=1 -I/usr/include/sqlcipher" \
    CGO_LDFLAGS="-lsqlcipher" \
    GOPRIVATE=github.com/zap-proto/* \
    GONOSUMDB=github.com/zap-proto/* \
    GOPROXY=https://proxy.golang.org,direct \
    GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go mod download
COPY . .
# Drop the console static bundle into the embed path BEFORE `go build`, so
# //go:embed all:webui/dist bakes it into the binary (same-origin console).
COPY --from=console /out/ /src/webui/dist/
# Overlay the FULL agent-skills catalog before `go build` so //go:embed all:catalog
# bakes the complete set (all services × brands), not the committed `ai` fallback.
COPY --from=skills /catalog/ /src/clients/agentskills/catalog/
# RED gate — modernc double-registration guard: 0 modernc under CGO=1, else the
# "sqlite" driver is registered twice (mattn + modernc) → panic at init.
RUN MODERNC="$(CGO_ENABLED=1 go list -tags "libsqlite3 sqlite_fts5" -deps ./cmd/cloud 2>/dev/null | grep -c 'modernc.org/sqlite' || true)"; \
    [ "$MODERNC" = "0" ] || { echo "SQLITE-GATE FAIL: cmd/cloud links modernc.org/sqlite ($MODERNC pkgs) under CGO=1 — double-registers \"sqlite\" with hanzoai/sqlite(mattn) and panics at init."; exit 1; }
# RED gate — ENCRYPTION PROOF + the cek.go GOLDEN-VECTOR KAT, under the SAME CGO +
# libsqlcipher build this image ships. TestEncryptionProof asserts real
# ciphertext-at-rest (SQLITE_REQUIRE_CODEC=1 makes a plaintext link FAIL → NO
# image). TestUnwrapGoldenFixture asserts a FROZEN pre-luxfi-swap 61-byte DEK
# sidecar still decrypts under the shipped luxfi/crypto-AEAD code — existing
# encrypted stores stay readable, or NO image.
RUN SQLITE_REQUIRE_CODEC=1 CGO_ENABLED=1 go test -count=1 -tags "libsqlite3 sqlite_fts5" \
      -run 'TestEncryptionProof|TestUnwrapGoldenFixture|TestWrapUnwrapRoundTripPinsLayout' \
      github.com/hanzoai/sqlite
RUN CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5" -ldflags="-s -w" -o /cloud ./cmd/cloud
# Prove the SHIPPED binary binds sqlite3_* to libsqlcipher, not a plaintext libsqlite3.
RUN readelf -d /cloud | grep -qE 'NEEDED.*(sqlcipher|sqlite3)' || { echo "FATAL: /cloud links no sqlite/sqlcipher .so"; exit 1; }; \
    ! ldd /cloud 2>/dev/null | grep -E 'libsqlite3' | grep -vq 'libsqlcipher' || { echo "FATAL: /cloud resolves a NON-sqlcipher libsqlite3 (plaintext risk)"; exit 1; }

# ── final image (alpine, NOT scratch — CGO needs libc + libsqlcipher) ─────────
FROM public.ecr.aws/docker/library/alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
ARG REVISION=unknown
LABEL org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/hanzoai/cloud"
# Runtime needs libsqlcipher (the codec the binary links). It must NOT also carry
# a plaintext libsqlite3 — the binary's -lsqlite3 DT_NEEDED would then bind to
# plaintext sqlite and silently no-op PRAGMA key. sqlcipher-libs ships
# libsqlcipher.so.0; alias libsqlite3.so.0 to it so sqlite3_* binds there.
RUN apk add --no-cache ca-certificates tzdata sqlcipher-libs \
    && SC="$(find /usr/lib /lib -name 'libsqlcipher.so*' 2>/dev/null | sort | head -1)" \
    && test -n "$SC" \
    && ln -sf "$SC" /usr/lib/libsqlite3.so.0
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /cloud /cloud
EXPOSE 8080 9090 9653
USER 65532:65532
ENTRYPOINT ["/cloud"]
