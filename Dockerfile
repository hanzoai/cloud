FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates tzdata git
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
WORKDIR /src
# Private cross-org subsystem modules (hanzoai/*, luxfi/*, zap-proto/*) are
# FIRST-PARTY (we own them; now public on GitHub) and fetched directly via
# authenticated git: GOPRIVATE marks them so go fetches them DIRECT (never the
# public proxy, which serves a tag's FIRST-seen content that sum.golang.org pins
# immutably — stale after any tag re-point) and skips the sumdb for them ONLY.
# GOPROXY routes everything ELSE (public deps) through the module proxy — REQUIRED
# for monorepo modules whose tags are nested paths (e.g. tencentcloud-sdk-go's
# `tencentcloud/hunyuan/vX.Y.Z`), which `direct` VCS resolution CANNOT find
# ("unknown revision"). First-party still bypasses the proxy via GOPRIVATE, so no
# private path leaks and re-pointed first-party tags still resolve direct.
# First-party-scoped, NEVER global GONOSUMDB=* / GOINSECURE. The committed go.sum
# is the single source of truth. gh_token is the shared BuildKit secret.
ENV GOPRIVATE=github.com/hanzoai/*,github.com/luxfi/*,github.com/zap-proto/* \
    GONOSUMDB=github.com/hanzoai/*,github.com/luxfi/*,github.com/zap-proto/* \
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
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /cloud ./cmd/cloud

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /cloud /cloud
EXPOSE 8080 9090 9653
USER 65532:65532
ENTRYPOINT ["/cloud"]
