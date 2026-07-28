# hanzoai/cloud — the light host + one binary per app (HIP-0106).
#
# This image is ONE directory: the light router /cloud (ENTRYPOINT) plus a /plugins
# binary for every subsystem beside it. The host knows only where each app lives
# and what path it answers; it loads each as its OWN process on the first request
# that reaches it. There is no fused binary — no build in this image links the
# fleet together, which is the whole point of this layout. cmd/cloud IS the one
# real binary; the fused monolith it replaced is gone.
#
# The console UI is compiled into the host via //go:embed (the light webui package,
# which cmd/cloud imports directly), so cmd/cloud — the front door — owns "/" and
# serves the white-labelled SPA for every path no app prefix claims. cmd/cloud also
# threads the deployment's brand/domain/data-dir/iam-issuer flags to the per-app
# children (it re-publishes them as CLOUD_* env the children read), and scopes
# credentials: it scrubs the KMS root key from its own environment and hands it to
# the kms broker child alone — see cmd/cloud.
#
# ── prebuilt decomplection artifacts (cloud compiles ONLY Go) ────────────────
# The console SPA and the agent-skills catalog are each built by THEIR OWN CI as
# a versioned immutable image and PULLED here, instead of rebuilding node +
# python from scratch every cloud release.
# The heavy one (console: a cold `npm install` + full Next.js static export,
# force-cache-busted every build) used to dominate the ~20-min build; it is now
# a registry pull.
#   console-embed (hanzoai/console Dockerfile.embed)  → /dist    → webui/dist                  (go:embed)
#   agent-skills  (hanzoai/openapi Dockerfile.skills) → /catalog → clients/agentskills/catalog (go:embed)
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
# BUMP: when a console/skills change must reach production, move its pin here in
# the same commit that claims it. That is what makes a cloud release
# reproducible and makes "what console is in v1.801.N" answerable from git.
ARG CONSOLE_IMAGE=ghcr.io/hanzoai/console-embed:sha-147ecd3-amd64
ARG SKILLS_IMAGE=ghcr.io/hanzoai/agent-skills:sha-b931a11-amd64

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

FROM ghcr.io/hanzoai/mirror/golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
# go.mod is the ONE place the Go version is declared.
#
# The golang image ships GOTOOLCHAIN=local, which makes the image's own Go the
# authority and refuses to honour a newer `go` directive. That put the version in
# TWO places that had to be kept in agreement by hand — this digest pin and
# go.mod — and they drifted: go.mod went to 1.26.5 while this digest stayed on
# 1.26.4, and every release then died at `go mod download` with
#   go: go.mod requires go >= 1.26.5 (running go 1.26.4; GOTOOLCHAIN=local)
# after the image had already built for a minute. Tags were not minted, so
# nothing shipped broken — releases simply stopped, quietly, for everyone.
#
# `auto` makes go.mod authoritative and this pin a floor: bumping the directive
# is now a one-line change that cannot desync. The toolchain is fetched into
# GOMODCACHE, which the `go mod download` step below already mounts as a shared
# build cache, so it costs one download per cache generation and nothing after.
ENV GOTOOLCHAIN=auto
# CIPHER-FORMAT FREEZE (cek depends on this). The data-plane stores are
# SQLCipher pages in a fixed on-disk format (cipher_compatibility 4). An at-open
# compat pin is infeasible (mattn keys via URI before any pragma), so the format
# is frozen by pinning sqlcipher-dev to an EXACT version. A repo bump then fails
# the build LOUDLY (never a silent prod brick); on such a failure, bump the pin
# AND confirm cek's frozen-fixture test still opens (format unchanged)
# before shipping. A MAJOR bump (4.x → 5.x) changes the default format and would
# orphan existing encrypted stores — migrate/rewrap them first.
RUN apk add --no-cache ca-certificates tzdata git gcc musl-dev sqlcipher-dev=4.6.1-r1 pkgconfig binutils
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
# RED gate — modernc double-registration guard: 0 modernc under CGO=1 ACROSS EVERY
# per-app binary, else the "sqlite" driver is registered twice (mattn + modernc) →
# panic at init. The fused monolith that this once checked is gone; the union of
# the host and the per-app graphs (./cmd/... ./plugin/...) is the same package set
# it linked, so listing them together is the equivalent guard — one modernc import
# in ANY app fails here.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    MODERNC="$(CGO_ENABLED=1 go list -tags "libsqlite3 sqlite_fts5" -deps ./cmd/... ./plugin/... 2>/dev/null | grep -c 'modernc.org/sqlite' || true)"; \
    [ "$MODERNC" = "0" ] || { echo "SQLITE-GATE FAIL: a per-app binary links modernc.org/sqlite ($MODERNC pkgs) under CGO=1 — double-registers \"sqlite\" with hanzoai/sqlite(mattn) and panics at init. Find it: CGO_ENABLED=1 go list -tags 'libsqlite3 sqlite_fts5' -deps ./plugin/<app> | grep modernc"; exit 1; }
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
# Go drops comments at compile time, so this pass is the ONLY way a typed handler's
# prose reaches the document: zipdoc lifts it into zipdoc_gen.go, which registers it
# with zip.Describe at init. It must run BEFORE every build below, because the
# generated file is compiled INTO each binary — running it after would be too late.
#
# mk/plugin.mk makes this a prerequisite of the per-app `build`, so the per-app path
# has always had it. This path did not, and the omission is measurable in production:
# api.hanzo.ai/v1/openapi.json serves 1441 operations with ZERO descriptions, which
# is exactly the binary mk/plugin.mk warns about. The SDK repos and the CLI read that
# document, so the prose never reached any of them either.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    go generate -run zipdoc ./...
# THE LIGHT HOST (cmd/cloud) — ~400 packages, pure Go, no codec and no subsystem
# (it links zip + the manifest + the light webui console embed, and nothing else).
# It is the ENTRYPOINT. It knows only where each app lives and what path it
# answers, and loads each app as its OWN process (a plugin) on the first request
# that reaches it. There is no fused binary anymore: the fleet never links
# together, so no build in this image is the mega link that once dominated it.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /cloud ./cmd/cloud
# The functional smoke prober (plugin/smoke) — a stdlib-only static binary shipped
# alongside the host so the release gate can `docker exec` it against the freshly-
# built image (and any deployment can be smoked via `docker run --entrypoint /smoke`).
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /smoke ./plugin/smoke
# EVERY subsystem, each as its OWN binary in /plugins beside the host. The host
# fork/execs a sibling <dir>/<name> (manifest.App.Plugin) on the first request that
# reaches its prefix, so the binary must be in the image or the mount aborts:
#
#   host: mount o11y: zip: Load(o11y): start: fork/exec /o11y: no such file
#
# The list is DERIVED from manifest/apps.go — the SAME hand-authored source the
# host reads and gen-app-cmds validates plugin/<app> against — so adding an app is a
# one-line manifest edit and this Dockerfile does not change. An app with no
# plugin/<app> fails HERE (the generator's bijection would have caught it first).
#
# Each link is the ONE app's own graph (~600–2200 packages), NEVER the ~3040-pkg
# fleet union the fused binary was. 112 lean links, sequential, none of them mega —
# which is the whole point of this change.
#
# CGO_ENABLED=1 + libsqlite3 + sqlite_fts5, exactly as the fused binary was built:
# every app that opens a store needs the SQLCipher codec (a plaintext link silently
# no-ops PRAGMA key), so they are built uniformly — one contract for all, the
# non-sqlite apps merely carrying a libc dep they do not use. The modernc gate above
# already proved none of them double-registers "sqlite" under this tag.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    set -eu; mkdir -p /plugins; \
    names="$(sed -n 's/.*{Name: "\([^"]*\)".*/\1/p' manifest/apps.go)"; \
    [ -n "$names" ] || { echo "FATAL: no apps parsed from manifest/apps.go — the derivation broke, not the app list"; exit 1; }; \
    for p in $names; do \
      [ -d "./plugin/$p" ] || { echo "FATAL: manifest app '$p' has no plugin/$p — run 'make generate' and commit"; exit 1; }; \
      echo "building plugin $p"; \
      CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5" -ldflags="-s -w" -o "/plugins/$p" "./plugin/$p"; \
    done
# Prove a SHIPPED sqlite-backed plugin binds sqlite3_* to libsqlcipher, not a
# plaintext libsqlite3. /plugins/base opens per-org stores under the SAME CGO=1 +
# libsqlite3 build every plugin above got, so it is a real witness for the set.
RUN readelf -d /plugins/base | grep -qE 'NEEDED.*(sqlcipher|sqlite3)' || { echo "FATAL: /plugins/base links no sqlite/sqlcipher .so"; exit 1; }; \
    ! ldd /plugins/base 2>/dev/null | grep -E 'libsqlite3' | grep -vq 'libsqlcipher' || { echo "FATAL: /plugins/base resolves a NON-sqlcipher libsqlite3 (plaintext risk)"; exit 1; }

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
RUN apk add --no-cache ca-certificates tzdata sqlcipher-libs git tini \
    && SC="$(find /usr/lib /lib -name 'libsqlcipher.so*' 2>/dev/null | sort | head -1)" \
    && test -n "$SC" \
    && ln -sf "$SC" /usr/lib/libsqlite3.so.0 \
    && test -x /sbin/tini
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /cloud /cloud
COPY --from=build /smoke /smoke
# The per-app plugin binaries, landing beside /cloud because that is where the host
# looks: manifest.App.Plugin resolves dir(os.Executable())+"/<name>". Copying the
# DIRECTORY's contents keeps this generic — a new app needs no line here, same as
# the build step above.
COPY --from=build /plugins/ /
EXPOSE 8080 9090 9653
USER 65532:65532
# tini as PID 1 forwards signals to /cloud unchanged (so SIGTERM still drains
# normally) and reaps orphans — which matters MORE for the host than it did for the
# fused binary, not less: the host's children are per-app processes that fork git
# and friends of their own, and when the host Kill()s a wedged child those
# grandchildren reparent to PID 1. `--` keeps the host's own args untouched.
#
# ENTRYPOINT is /cloud — the light host IS the shipped binary now (the fused
# monolith is gone), so there is no alternative, and host mode is the shipped
# topology rather than an opt-in. THREE properties:
#
#  1. CREDENTIALS ARE SCOPED IN HOST MODE. The fused binary called credz.Boot,
#     which took CLOUD_KMS_MASTER_KEY_REF OUT of its environment before spawning
#     anything; /cloud reaches the same end without linking the codec. It does NOT
#     call credz.Boot (a light host that imports credz would drag cek →
#     modernc/sqlite + sqlcipher into a ~400-package build whose whole reason to
#     exist is being small). Instead it does the scrub itself, with stdlib only
#     (credz/launch): at boot it reads the root key, os.Unsetenv's it from its OWN
#     environment so no child inherits it through os.Environ(), stamps every child
#     a scoped launch token (credz/launch.Env) on that child's zip.Plugin.Env, and
#     re-injects the root key onto the kms broker child's Env ALONE. Every generic
#     child therefore comes up with a per-app token and NO root key, and must ask
#     the broker for its scoped bundle — the boundary credz was built for, now the
#     default entrypoint. (cmd/cloud/main_test.go pins it: a generic child's env
#     carries CREDZ_TOKEN and not CLOUD_KMS_MASTER_KEY_REF; the broker's carries
#     the key.)
#  2. The host binds :8080 and :9653 but NOT the :9090 ops port (serve.go leaves it
#     unbound for a plugin, since N children cannot share one port), so anything
#     scraping 9090 must move to the child or be dropped.
#  3. The children need a writable directory for their unix sockets, which as uid
#     65532 on a read-only rootfs means mounting one.
ENTRYPOINT ["/sbin/tini", "--", "/cloud"]
