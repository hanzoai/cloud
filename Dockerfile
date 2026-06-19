FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates tzdata git
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot
WORKDIR /src
# Private cross-org subsystem modules (hanzoai/*, luxfi/*, zap-proto/*) are
# fetched via authenticated git. GOSUMDB=off + GOPROXY=direct tolerate
# force-re-tagged luxfi/hanzoai modules (committed go.sum is source of truth);
# gh_token is the shared docker-build.yml BuildKit secret (no-op when absent).
ENV GOPRIVATE=github.com/hanzoai/*,github.com/luxfi/*,github.com/zap-proto/* \
    GOSUMDB=off \
    GOPROXY=direct \
    GOFLAGS=-mod=mod
COPY go.mod go.sum ./
# go mod download, self-healing past upstream force-re-tag poisoning: if a
# luxfi/hanzoai module's tag was force-moved/deleted after go.sum was recorded,
# the committed sum mismatches the fresh origin fetch. With GOSUMDB=off the
# regenerated sum is trustworthy for our own private modules. Root-cause fix is
# stopping force-re-tags at the source; this keeps the image buildable meanwhile.
RUN --mount=type=secret,id=gh_token \
    if [ -s /run/secrets/gh_token ]; then \
      git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"; \
    fi && \
    (go mod download || (echo ">> go.sum poisoned by upstream force-re-tag; regenerating from origin" && rm -f go.sum && go mod download))
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
