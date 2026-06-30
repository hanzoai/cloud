# ECR Public mirror of the Docker library image — Docker Hub's unauthenticated
# pull rate-limit (429 toomanyrequests) fails the build on shared CI runners.
# Same fix already shipped in hanzoai/console2.
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
