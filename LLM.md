# LLM.md — hanzoai/cloud

**What**: Open-source Hanzo Cloud local dev & self-hosted server (`cmd/cloud` -> `./cloud`).
One Go binary that mounts every subsystem from a single composition root (`apps.Wire()`):
Base (`/v1/base` document/KV/SQLite store), Tasks (`/v1/tasks` durable lease/ack queue),
Functions (`/v1/functions` staged runner), KMS (`/v1/kms` embedded `luxfi/kms`), S3, Search,
Edge, Feature Flags, Audit, Plugin runtime (`/v1/plugins`), OpenAPI (`/v1/openapi.json`),
and MCP (`/mcp` JSON-RPC 2.0).

**Canonical AI instructions**: `LLM.md` is the canonical instruction file in this repository.
`AGENTS.md`, `CLAUDE.md`, and `GEMINI.md` are tracked symlinks to `LLM.md`.

## Build, Run & Test

```bash
make dev      # CGO_ENABLED=0 go build ./cmd/cloud, mints .dev/master.key (0600), serves 127.0.0.1:8080
make build    # CGO_ENABLED=0 go build -o cloud ./cmd/cloud
make test     # CGO_ENABLED=0 go test ./...
make vet      # CGO_ENABLED=0 go vet ./...
make hooks    # git config core.hooksPath .githooks (public-only pre-push guard)
```

- **Pure-Go SQLCipher Encryption at Rest (`CGO_ENABLED=0`)**:
  - `Makefile` and `Dockerfile` build with `CGO_ENABLED=0` so the pure-Go SQLite backend (`cek/`) encrypts all SQLite stores under `CLOUD_KMS_MASTER_KEY_REF` (32 random bytes, base64-encoded) with zero C toolchain dependencies.
  - A default `CGO_ENABLED=1` build links unencrypted C SQLite and refuses to start with a master key unless built with `-tags libsqlite3` against `libsqlcipher` (or run explicitly with `CLOUD_DEV_UNENCRYPTED=1`).
- **Optional Rust Flag Evaluator**:
  - `make native` builds `native/flags/Cargo.toml`; linking it requires `CGO_ENABLED=1 make build TAGS=flags_native` (plus `libsqlite3` linked against `libsqlcipher` if encryption is required).

## Listeners & Ports

| Env / Flag | Default | Purpose |
|---|---|---|
| `CLOUD_LISTEN` / `-listen` / `PORT` | `:8080` (`make dev`: `127.0.0.1:8080`) | HTTP API (`/v1/*`), MCP (`/mcp`), embedded console |
| `CLOUD_HEALTH_LISTEN` | `127.0.0.1:9090` | `/healthz`, `/readyz`, `/metrics` |
| `CLOUD_ZAP_LISTEN` | `127.0.0.1:9653` | Binary ZAP transport exposing the same typed ops |

## OCI Container Image & `hanzo up`

- [`Dockerfile`](Dockerfile) builds a multi-arch (`linux/amd64`, `linux/arm64`) static `CGO_ENABLED=0` `/cloud` binary on `gcr.io/distroless/static-debian12:nonroot` (`USER 65532:65532`, `VOLUME ["/var/lib/cloud"]`).
- [`.github/workflows/image.yml`](.github/workflows/image.yml) publishes public **`ghcr.io/hanzoai/cloud-oss:main`** (and `:v*` release tags), which `hanzo up` (`github.com/hanzoai/cli`) pulls anonymously into its measured local k3s microVM (`127.0.0.1:3690` -> container `:8080`).

## Architecture Invariants

1. **Typed Operations Only**: Declare operations as typed ops so a single registration projects simultaneously into REST routes, `/v1/openapi.json`, `/mcp` JSON-RPC tools, CLI commands, and SDKs. Never add untyped ad-hoc HTTP handlers for API operations.
2. **Namespace Isolation**: Authenticated requests (JWTs verified against `{CLOUD_IAM_ISSUER}/v1/iam/.well-known/jwks`) run inside the token's `owner` org namespace; unauthenticated requests operate in the local namespace (`""`), which is why `make dev` binds `127.0.0.1`.
3. **Staged Subsystems**: Subsystems that execute arbitrary code or alter ingress (`functions`, `iam`, `ingress`) only mount when explicitly listed in `CLOUD_ENABLE_STAGED`.
4. **Fail-Closed External Integrations**: Optional sibling services (`S3_*`, `HANZO_DNS_URL`, `CODE_EXEC_UPSTREAM`, `CLOUD_AI_ZAP_ADDR`) report 503 clearly when unconfigured rather than faking success.
5. **Public-Only Repository Guard**: `.githooks/pre-push` enforces that `github.com/hanzoai/cloud` carries zero commits from non-public remotes and zero non-public Go module imports (`Apache-2.0 OR MIT`).
