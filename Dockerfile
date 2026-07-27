# hanzoai/cloud — the ONE unified Hanzo Cloud binary (HIP-0106).
#
# This image is a SINGLE artifact that serves BOTH the /v1 API AND the console
# UI from one process: the console is compiled into the Go binary via
# //go:embed (see webui.go). The final `/cloud` binary already carries the UI —
# no separate console Service, no second origin; the embedded console calls /v1
# on its own host.
#
# ── prebuilt decomplection artifacts (cloud compiles ONLY Go) ────────────────
# The console SPA, the agent-skills catalog, and the native flags staticlib are
# each built by THEIR OWN CI as a versioned immutable image and PULLED here,
# instead of rebuilding node + python + rust from scratch every cloud release.
# The heavy one (console: a cold `npm install` + full Next.js static export,
# force-cache-busted every build) used to dominate the ~20-min build; it is now
# a registry pull.
#   console-embed (hanzoai/console Dockerfile.embed)  → /dist             → webui/dist                  (go:embed)
#   agent-skills  (hanzoai/openapi Dockerfile.skills) → /catalog          → clients/agentskills/catalog (go:embed)
#   cloud-flags   (native/flags    Dockerfile)        → /libhanzo_flags.a → CGO link (clients/featureflags)
# Pinned to ghcr.io so BOTH buildx lanes (release.yml + platform arcbuild) pull
# it directly; the SAME tags are mirrored to registry.hanzo.ai (S3-backed) for
# GET-flow consumers (docker/kaniko/crane). Override any pin with
# --build-arg <NAME>_IMAGE=… .
#
# IMMUTABLE per-commit tags, never `:latest`. These defaults are LOAD-BEARING:
# the builder that actually runs our releases is the native one (POST /v1/runner
# → launchDirectBuild → BuildKit), and it passes no --build-arg, so whatever is
# written here is what gets baked. `release.yml`, which the previous comment said
# would resolve a fresh digest, is a stub and resolves nothing.
#
# With `:latest` the embedded console was therefore decided by WHEN the build ran,
# not by what we shipped — and it bit: cloud v1.801.215 was built ~12 minutes
# before console CI finished publishing the console-embed carrying v8.5.26, so a
# release whose whole purpose was that console change silently baked the previous
# one and shipped green. Same image, two contents, no diff to show for it.
#
# BUMP: when a console/skills/flags change must reach production, move its pin
# here in the same commit that claims it. That is what makes a cloud release
# reproducible and makes "what console is in v1.801.N" answerable from git.
ARG CONSOLE_IMAGE=ghcr.io/hanzoai/console-embed:sha-147ecd3-amd64
ARG SKILLS_IMAGE=ghcr.io/hanzoai/agent-skills:sha-b931a11-amd64
ARG FLAGS_IMAGE=ghcr.io/hanzoai/cloud-flags:sha-e1ca02a-amd64

# ── toolchain base images: the golang + alpine FROMs below pull from our own
# GHCR mirror (ghcr.io/hanzoai/mirror/*), pinned by digest. WHY: public.ecr.aws
# rate-limits anonymous pulls (HTTP 429) on shared CI runners and a 429 on ANY
# base pull aborts the release. The mirror packages are 1:1 amd64 copies of the
# upstream public images, digest-pinned for immutability; release.yml logs the
# build into ghcr.io (GH_PAT) before building so they resolve. REFRESH on a
# toolchain bump: crane/regctl copy the new upstream into
# ghcr.io/hanzoai/mirror/<name>:<tag> and repoint the digest below. Canonical
# long-term home is registry.hanzo.ai/hanzoai/mirror/* — repoint once the runners
# carry its IAM pull credentials (follow-up).

# ── console SPA static export (prebuilt → /dist) ─────────────────────────────
FROM ${CONSOLE_IMAGE} AS console

# ── agent-skills catalog (prebuilt → /catalog) ──────────────────────────────
FROM ${SKILLS_IMAGE} AS skills

# ── native flags evaluator staticlib (prebuilt → /libhanzo_flags.a) ──────────
FROM ${FLAGS_IMAGE} AS flagslib

FROM ghcr.io/hanzoai/mirror/golang:1.26-alpine3.22@sha256:47d47cb5cc3c7dac409dcb6c3a98a6263571218046cd02d709527feef804a77c AS build
# CIPHER-FORMAT FREEZE (cek depends on this). The data-plane stores are
# SQLCipher pages in a fixed on-disk format (cipher_compatibility 4). An at-open
# compat pin is infeasible (mattn keys via URI before any pragma), so the format
# is frozen by pinning sqlcipher-dev to an EXACT version. A repo bump then fails
# the build LOUDLY (never a silent prod brick); on such a failure, bump the pin
# AND confirm cek's frozen-fixture test still opens (format unchanged)
# before shipping. A MAJOR bump (4.x → 5.x) changes the default format and would
# orphan existing encrypted stores — migrate/rewrap them first.
RUN apk add --no-cache ca-certificates tzdata git gcc musl-dev sqlcipher-dev=4.6.1-r0 pkgconfig binutils
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
# zap-proto/* (all 55 repos) and luxfi/* (all 37 deps here) are PUBLIC and resolve
# via the IMMUTABLE public proxy + sumdb — go.sum pins those canonical hashes, so a
# force-re-pointed tag can never break the build. GOSUMDB stays ON (a money image
# must not blanket-disable the checksum database); github.com/hanzoai/* is the
# exempt namespace — ai, account, commerce, orm, xorm, beego, csqlite and ~30 more
# are PRIVATE repos, so they resolve direct+authenticated (git over gh_token) and
# skip a sumdb that cannot see them. GOPRIVATE named zap-proto until now, which is
# public and was never the reason anything was direct; the private namespace it
# stood for went unnamed and worked only on the GOPROXY `direct` fallback.
# -mod=readonly means the committed go.sum is the SOLE source of truth: any drift
# (a needed hash not present) FAILS the build instead of being silently
# re-recorded. CGO_CFLAGS/LDFLAGS enable the SQLCipher codec + URI keying.
ENV CGO_CFLAGS="-DSQLITE_HAS_CODEC -DSQLITE_USE_URI=1 -I/usr/include/sqlcipher" \
    CGO_LDFLAGS="-lsqlcipher" \
    GOPRIVATE=github.com/hanzoai/* \
    GOPROXY=https://proxy.golang.org,direct \
    GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
# The cache mounts carry an EXPLICIT id so they can be busted. Without one,
# BuildKit keys the cache by target path alone, and a poisoned entry is immortal:
# a module resolved while its tag did not yet exist is remembered as "unknown
# revision" forever, so `go mod download` keeps failing on a tag that now exists
# and resolves fine from a clean cache. That is exactly what wedged the release
# on otel-collector v0.144.10. BUMP THE SUFFIX (-v4 -> -v5) to force a cold
# module cache the next time a phantom pin poisons it.
RUN --mount=type=secret,id=GIT_AUTH_TOKEN \
    --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    if [ -s /run/secrets/GIT_AUTH_TOKEN ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/GIT_AUTH_TOKEN)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    go mod download
COPY . .
# Drop the console static bundle into the embed path BEFORE `go build`, so
# //go:embed all:webui/dist bakes it into the binary (same-origin console).
COPY --from=console /dist/ /src/webui/dist/
# Overlay the FULL agent-skills catalog before `go build` so //go:embed all:catalog
# bakes the complete set (all services × brands), not the committed `ai` fallback.
COPY --from=skills /catalog/ /src/clients/agentskills/catalog/
# The native flags staticlib at the exact ${SRCDIR}-relative path the cgo
# directive in clients/featureflags/engine.go links.
COPY --from=flagslib /libhanzo_flags.a /src/native/flags/target/release/libhanzo_flags.a
# RED gate — modernc double-registration guard: 0 modernc under CGO=1, else the
# "sqlite" driver is registered twice (mattn + modernc) → panic at init.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    MODERNC="$(CGO_ENABLED=1 go list -tags "libsqlite3 sqlite_fts5" -deps ./cmd/cloud 2>/dev/null | grep -c 'modernc.org/sqlite' || true)"; \
    [ "$MODERNC" = "0" ] || { echo "SQLITE-GATE FAIL: cmd/cloud links modernc.org/sqlite ($MODERNC pkgs) under CGO=1 — double-registers \"sqlite\" with hanzoai/sqlite(mattn) and panics at init."; exit 1; }
# RED gate — ENCRYPTION PROOF + the cek.go GOLDEN-VECTOR KAT, under the SAME CGO +
# libsqlcipher build this image ships. TestEncryptionProof asserts real
# ciphertext-at-rest (SQLITE_REQUIRE_CODEC=1 makes a plaintext link FAIL → NO
# image). TestUnwrapGoldenFixture asserts a FROZEN pre-luxfi-swap 61-byte DEK
# sidecar still decrypts under the shipped luxfi/crypto-AEAD code — existing
# encrypted stores stay readable, or NO image.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    SQLITE_REQUIRE_CODEC=1 CGO_ENABLED=1 go test -count=1 -tags "libsqlite3 sqlite_fts5" \
      -run 'TestEncryptionProof|TestUnwrapGoldenFixture|TestWrapUnwrapRoundTripPinsLayout' \
      github.com/hanzoai/sqlite
# RED gate — cek FROZEN-FORMAT guard, run INSIDE the image under the pinned Alpine
# libsqlcipher: opens the committed encrypted fixture and reads its canary row. A
# sqlcipher-dev pin/base bump that changes the on-disk format fails the IMAGE build
# HERE (not only Go CI) → a silent prod brick of existing stores becomes a red build.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    SQLITE_REQUIRE_CODEC=1 CGO_ENABLED=1 go test -count=1 -run TestFrozenFixtureOpens \
      -tags "libsqlite3 sqlite_fts5" ./cek
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5" -ldflags="-s -w" -o /cloud ./cmd/cloud
# The PLUGIN binaries — the subsystems that are no longer linked into /cloud and
# therefore have to ship next to it, or the host fails to mount them and refuses
# to boot. Same toolchain, same tags, same codec as /cloud: a plugin owns a store
# too (o11y's annotation queues), so a pure-Go plugin beside a sqlcipher host
# would be a second, weaker storage posture in one image.
#
# The list is READ FROM apps/apps.go, the single source — a Dockerfile cannot link
# Go to ask Wire(), but it can read the same text, and apps.TestPluginBinaries
# proves this exact pattern yields exactly cloud.PluginNames(apps.Wire()). A
# second hand-maintained list here is how "it built, it just does not start" ships.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    set -eu; mkdir -p /plugins; \
    for p in $(grep -oE 'PluginSpec."[a-z0-9-]+"' apps/apps.go | cut -d'"' -f2); do \
      echo ">> building plugin $p"; \
      CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5" -ldflags="-s -w" -o "/plugins/$p" "./cmd/$p"; \
    done; \
    test -n "$(ls -A /plugins)" || { echo "FATAL: no plugin binaries built — apps.go declares none, or the pattern drifted"; exit 1; }
# The functional smoke prober (cmd/smoke) — a stdlib-only, static binary shipped
# alongside /cloud so the release gate can `docker exec` it against the freshly-built
# image (and any deployment can be smoked via `docker run --entrypoint /smoke ...`).
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /smoke ./cmd/smoke
# Prove every SHIPPED binary binds sqlite3_* to libsqlcipher, not a plaintext
# libsqlite3 — the plugins too: an unencrypted store is not less of a problem for
# being in a child process.
RUN set -eu; for b in /cloud /plugins/*; do \
      readelf -d "$b" | grep -qE 'NEEDED.*(sqlcipher|sqlite3)' || { echo "FATAL: $b links no sqlite/sqlcipher .so"; exit 1; }; \
      ! ldd "$b" 2>/dev/null | grep -E 'libsqlite3' | grep -vq 'libsqlcipher' || { echo "FATAL: $b resolves a NON-sqlcipher libsqlite3 (plaintext risk)"; exit 1; }; \
    done

# ── final image (alpine, NOT scratch — CGO needs libc + libsqlcipher) ─────────
FROM ghcr.io/hanzoai/mirror/alpine:3.22@sha256:7c8cb692ae09657cbc4a3f3cbd0e8d5a2690ba38386aaaf252dbb060bf5eb2e6
ARG REVISION=unknown
LABEL org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/hanzoai/cloud"
# Runtime needs libsqlcipher (the codec the binary links). It must NOT also carry
# a plaintext libsqlite3 — the binary's -lsqlite3 DT_NEEDED would then bind to
# plaintext sqlite and silently no-op PRAGMA key. sqlcipher-libs ships
# libsqlcipher.so.0; alias libsqlite3.so.0 to it so sqlite3_* binds there.
#
# `git` backs the git object plane (clients/git): the heavy paths — clone/fetch
# serve, push receive, mirror-in — shell out to the streaming git CLI
# (upload-pack / receive-pack --stateless-rpc / fetch) so multi-GB packs stream
# to and from disk with bounded memory instead of buffering whole packs in RAM.
# The `git` apk package carries upload-pack/receive-pack/http-backend/git-remote-https.
# libgcc: the hanzo-flags Rust staticlib (clients/featureflags FFI) references the
# _Unwind_* unwinder symbols; musl needs libgcc_s at load time or the binary fails
# relocation ("Error relocating /cloud: _Unwind_GetIP: symbol not found").
# tini: /cloud runs as PID 1, and PID 1 inherits every orphaned descendant in the
# container. git is not a single process — fetch/clone fan out to git-upload-pack,
# git-index-pack, git-rev-list and git-pack-objects. When cloud Kill()s a wedged
# direct child (gitPackStream.Close does exactly that, correctly), those
# grandchildren orphan and reparent to PID 1. A Go program never reaps adopted
# orphans, so each one becomes a permanent zombie holding a PID slot.
# Measured 2026-07-26 on worker-xl-37bw71: 18,553 zombie `git` out of 18,741
# processes, all parented to /cloud, which drove the node to PID pressure and
# got cloud ITSELF evicted. A zombie costs no CPU and no memory, so nothing but
# an eviction ever surfaces it. tini reaps them.
RUN apk add --no-cache ca-certificates tzdata sqlcipher-libs git libgcc tini \
    && SC="$(find /usr/lib /lib -name 'libsqlcipher.so*' 2>/dev/null | sort | head -1)" \
    && test -n "$SC" \
    && ln -sf "$SC" /usr/lib/libsqlite3.so.0 \
    && test -x /sbin/tini
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /cloud /cloud
# The plugin binaries land BESIDE /cloud, which is exactly where apps.pluginAt
# looks (os.Executable's dir, not $PATH) — so a host always starts the plugin it
# was built and shipped with. Still one image, still one artifact to ship.
COPY --from=build /plugins/ /
COPY --from=build /smoke /smoke
EXPOSE 8080 9090 9653
USER 65532:65532
# tini as PID 1 forwards signals to /cloud unchanged (so SIGTERM still drains
# normally) and reaps the orphans described above. `--` keeps cloud's own args
# untouched; the CR passes none today, but that stays true if it ever does.
ENTRYPOINT ["/sbin/tini", "--", "/cloud"]
