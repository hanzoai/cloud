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
#   console-embed (hanzoai/console Dockerfile.embed)  → /dist    → webui/dist               (go:embed)
#   agent-skills  (hanzoai/openapi Dockerfile.skills) → /catalog → apps/skills/catalog (go:embed)
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
#
# CONSOLE IS PINNED BY SEMVER, not by sha. `sha-<sha7>-amd64` is what the builder
# publishes on every main push; `v<X.Y.Z>` is what it publishes on a cut v* tag,
# and that is the one to name here — the pin then says which RELEASE of the
# console a cloud image carries, which a sha cannot.
#
# The tradeoff is real and the discipline changes to match: a sha tag cannot be
# re-pushed to different bytes, whereas a semver tag CAN be moved (`:v8.4.118`
# was, in this fleet). So the rule that keeps this reproducible is now a rule
# about tags, not about tag SHAPE: a cut tag is never re-pointed. Cut the next
# patch instead — that is cheap, and it keeps "which console is in v1.801.N"
# answerable from git alone.
# 8.5.50 is the release whose assistant actually sends its credential: the
# streamed completion rides the client's one authorized door, preferences moved
# to cloud's /v1/prefs, the 401 card stopped claiming an expired session, and
# exactly one composer mounts per viewport. Cut as a release tag because a tag
# build publishes its name verbatim — which console is in this image is
# answerable from this line alone.
ARG CONSOLE_IMAGE=ghcr.io/hanzoai/console-embed:8.5.53
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
# AND confirm hanzoai/sqlite's TestUnwrapGoldenFixture still opens (format unchanged)
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
# The published tag, handed in by buildFrontendCmd (--opt build-arg:VERSION=<tag>)
# and linked into cloud.Version below, which is what the X-Api-Version response
# header serves. Without it the header reports the "dev" default forever — as
# cloud.hanzo.ai and console.hanzo.ai both did in production.
ARG VERSION=dev
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
# FORGE_TOKEN, when supplied, points our OWN modules at git.hanzo.ai. The module
# path stays github.com/hanzoai/* — a name, not an address — and git dials the
# canonical forge instead. The longer prefix wins in git, so only hanzoai/* is
# redirected and every other github.com module still goes to GitHub. go.sum is
# unchanged and still authoritative: the forge mirrors the same objects, so the
# zip hashes to the committed h1: line, and a forge serving different bytes fails
# the build rather than shipping them. Both secrets are optional; absent either,
# this falls back to exactly the previous behaviour.
RUN --mount=type=secret,id=GIT_AUTH_TOKEN \
    --mount=type=secret,id=FORGE_TOKEN \
    --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    if [ -s /run/secrets/FORGE_TOKEN ]; then \
      git config --global url."https://x:$(cat /run/secrets/FORGE_TOKEN)@git.hanzo.ai/hanzoai/".insteadOf "https://github.com/hanzoai/"; \
    fi && \
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
#
# The path is apps/skills/catalog because that is where the embed is
# (apps/skills/skills.go). It read apps/skills/catalog until
# now — the pre-f873d1a1 home of every subsystem — and COPY CREATES a missing
# destination, so the overlay landed in a directory no package embeds and nothing
# anywhere disagreed. Every image since that move has shipped the tracked fallback
# instead: one skill (ai_models) per brand, served as the whole of
# /.well-known/agent-skills/index.json. The RUN below is the gate that was missing.
COPY --from=skills /catalog/ /src/apps/skills/catalog/
# RED gate — the overlays landed WHERE THE EMBED READS. Both COPYs above write
# into a tracked fallback that exists precisely so a bare `go build` works, and
# `COPY` creates a missing destination rather than failing — so a stale path is
# not an error, it is a silently smaller binary. That is the whole failure above,
# and it survived because the only evidence was a number in a served document.
# Assert it here, where the destination is named, in the terms each fallback is
# defined by rather than a file count that drifts: the skills fallback is ONE
# skill per brand, and the console fallback is a hand-written index.html with no
# script at all — a static SPA export carrying zero JavaScript is not a build.
RUN set -eu; \
    n="$(sed -n 's/.*"skill_count":[[:space:]]*\([0-9]*\).*/\1/p' /src/apps/skills/catalog/hanzo/index.json)"; \
    [ "${n:-0}" -gt 1 ] || { echo "SKILLS-GATE FAIL: apps/skills/catalog holds the ${n:-0}-skill fallback — the overlay missed the //go:embed path"; exit 1; }; \
    j="$(find /src/webui/dist -type f -name '*.js' | wc -l)"; \
    [ "$j" -gt 0 ] || { echo "CONSOLE-GATE FAIL: webui/dist carries no JavaScript — the overlay missed the //go:embed path and the image would ship the fallback shell"; exit 1; }; \
    echo ">> overlays landed: $n skills/brand, $j console scripts"
# RED gate — modernc double-registration guard: 0 modernc under CGO=1 ACROSS EVERY
# per-app binary, else the "sqlite" driver is registered twice (mattn + modernc) →
# panic at init. The fused monolith that this once checked is gone; the union of
# the host and the per-app graphs (./cmd/... ./plugin/...) is the same package set
# it linked, so listing them together is the equivalent guard — one modernc import
# in ANY app fails here.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    MODERNC="$(CGO_ENABLED=1 go list -tags "libsqlite3 sqlite_fts5 sqlite_math_functions" -deps ./cmd/... ./plugin/... 2>/dev/null | grep -c 'modernc.org/sqlite' || true)"; \
    [ "$MODERNC" = "0" ] || { echo "SQLITE-GATE FAIL: a per-app binary links modernc.org/sqlite ($MODERNC pkgs) under CGO=1 — double-registers \"sqlite\" with hanzoai/sqlite(mattn) and panics at init. Find it: CGO_ENABLED=1 go list -tags 'libsqlite3 sqlite_fts5 sqlite_math_functions' -deps ./plugin/<app> | grep modernc"; exit 1; }
# RED gate — ENCRYPTION PROOF + the cek.go GOLDEN-VECTOR KAT, under the SAME CGO +
# libsqlcipher build this image ships. TestEncryptionProof asserts real
# ciphertext-at-rest (SQLITE_REQUIRE_CODEC=1 makes a plaintext link FAIL → NO
# image). TestUnwrapGoldenFixture asserts a FROZEN pre-luxfi-swap 61-byte DEK
# sidecar still decrypts under the shipped luxfi/crypto-AEAD code — existing
# encrypted stores stay readable, or NO image.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    SQLITE_REQUIRE_CODEC=1 CGO_ENABLED=1 go test -count=1 -tags "libsqlite3 sqlite_fts5 sqlite_math_functions" \
      -run 'TestEncryptionProof|TestUnwrapGoldenFixture|TestWrapUnwrapRoundTripPinsLayout' \
      github.com/hanzoai/sqlite
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
# The commit this image is built FROM, handed in by the SAME builder that already
# feeds it to the OCI label in the final stage (apps/platform buildFrontendCmdRev,
# `--opt build-arg:REVISION=<sha>`; the other lane passes github.sha).
#
# `ARG REVISION` already existed — but ONLY in that final stage, and an ARG is
# per-stage, so it was never in scope where `go build` runs and no binary in this
# image could name its commit. The wire was connected at one end.
#
# Do not "fix" it by trusting the label. A label is read by whoever thinks to open
# the registry; the PROCESS is read by whoever is holding the outage — and this
# fleet's revision label has itself read `unknown` on natively-built images
# without anyone noticing, which is what a label is worth.
#
# DECLARED HERE, AS LATE AS POSSIBLE, and deliberately not beside ARG VERSION at
# the top of the stage: everything below `COPY . .` is already re-keyed by any
# source change, so a per-commit value costs nothing from this line down. The same
# value in scope ABOVE would re-key `go mod download` and turn every build into a
# full one.
ARG REVISION=unknown
# ONE flag string for EVERY binary in this image. This is a build-stage variable —
# the final stage does not inherit it and nothing reads it at run time; it exists
# so the stamp cannot reach some binaries and miss others.
#
# It has to reach the PLUGINS. cmd/cloud is a router that links zip and the
# manifest, not the package these symbols live in, so `-X github.com/hanzoai/
# cloud.Version=` on /cloud has always been silently dropped — measured: the flag
# shows up in the binary's `go version -m` build record and the value is nowhere
# in the linked bytes. The plugins are what serve /v1/health, and they carried no
# -X whatsoever, so stamping only the entrypoint would have left the process that
# answers the question mute.
ENV GO_LDFLAGS="-s -w -X github.com/hanzoai/cloud.Version=${VERSION} -X github.com/hanzoai/cloud.revision=${REVISION}"
# THE LIGHT HOST (cmd/cloud) — ~400 packages, pure Go, no codec and no subsystem
# (it links zip + the manifest + the light webui console embed, and nothing else).
# It is the ENTRYPOINT. It knows only where each app lives and what path it
# answers, and loads each app as its OWN process (a plugin) on the first request
# that reaches it. There is no fused binary anymore: the fleet never links
# together, so no build in this image is the mega link that once dominated it.
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -ldflags="$GO_LDFLAGS" -o /cloud ./cmd/cloud
# The functional smoke prober (plugin/smoke) — a stdlib-only static binary shipped
# alongside the host so the release gate can `docker exec` it against the freshly-
# built image (and any deployment can be smoked via `docker run --entrypoint /smoke`).
RUN --mount=type=cache,id=cloud-gomod-v4,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cloud-gobuild-v4,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -ldflags="$GO_LDFLAGS" -o /smoke ./plugin/smoke
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
# CGO_ENABLED=1 + libsqlite3 + sqlite_fts5 + sqlite_math_functions, exactly as the
# fused binary was built:
#
# sqlite_math_functions is not optional under cgo. hanzoai/base's search layer
# generates SQL calling acos/cos/sin/radians/sqrt (the geoDistance token in
# tools/search); SQLite only has those with SQLITE_ENABLE_MATH_FUNCTIONS, which
# the cgo backend gets ONLY behind that tag. base v1.5.11 turned the mismatch
# into a compile error on purpose (core/sqlite_math_required.go, //go:build cgo
# && !sqlite_math_functions) rather than let a cgo build ship a smaller SQL
# surface than the code above it writes against — the failure otherwise is a
# customer's search returning "no such function: acos" from an endpoint that
# works in production. Without the tag every plugin build dies with
#   base@v1.5.11/core/sqlite_math_required.go:30:6:
#   undefined: cgoBuildNeedsSQLiteMathFunctions
# The CGO_ENABLED=0 builds below do not need it: the pure-Go backend always has
# the functions.
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
      CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5 sqlite_math_functions" -ldflags="$GO_LDFLAGS" -o "/plugins/$p" "./plugin/$p"; \
    done
# THE STAMP LANDED — asked of the ARTIFACT, not of the flag string.
#
# `-X` naming a path or symbol the linker cannot resolve is not an error: it is
# dropped, the build succeeds, and every binary then reports the entirely
# legitimate-looking "unknown" forever. A renamed package or variable would fail
# in exactly the one way nobody looks at, which is how this started.
#
# `go version -m` is NOT a witness — it echoes the -ldflags string that was
# REQUESTED, and that string is present even when the symbol was never set
# (measured on /cloud, whose Version stamp has been dropped all along). Only the
# linked bytes answer.
#
# strings|grep rather than a bare grep: grep treats binary input as non-text and
# its exit status there is not portable across implementations, so a plain
# `grep -qF` can report no match on a binary that demonstrably contains the sha.
# strings normalises to text lines first; binutils is already installed above.
#
# An image built with no REVISION is not a failure — it is a build that cannot
# name its commit, and it says so here and on every health response it serves.
RUN set -eu; \
    if [ "$REVISION" = "unknown" ]; then \
      echo ">> no REVISION build-arg: this image cannot name its commit, and every health response it serves will report revision=unknown"; \
    else \
      strings -a /plugins/base | grep -qF "$REVISION" || { echo "FATAL: -X did not reach /plugins/base — github.com/hanzoai/cloud.revision was not resolved, so it was dropped and every health response would report 'unknown'"; exit 1; }; \
      echo ">> revision $REVISION linked into the plugins"; \
    fi
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
