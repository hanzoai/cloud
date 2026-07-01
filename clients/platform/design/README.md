# `/v1/platform` — Goa design (design-first contract)

This nested module holds the **Goa v3 design** for the Hanzo Platform (PaaS)
API and the **OpenAPI 3** document generated from it. It is the authoritative,
design-first contract for `/v1/platform`.

## Why a nested module

`go.mod` here is deliberately separate from `github.com/hanzoai/cloud` so the
cloud binary NEVER takes a `goa.design/goa/v3` runtime dependency. The cloud
runtime mounts `/v1/platform` on the canonical `zip` router (one router, per
HIP-0106); this module exists only to express the API in the Goa DSL and to
`goa gen` the OpenAPI contract that console + external clients consume. Because
it is a nested module, `go build ./...` from the cloud root skips it.

## Files

- `design/design.go` — the Goa DSL (source of truth).
- `gen/http/openapi3.json` / `gen/http/openapi3.yaml` — the generated OpenAPI 3
  contract (committed; consumed by console2 + clients).

## Regenerate

```sh
cd clients/platform/design
go run goa.design/goa/v3/cmd/goa gen github.com/hanzoai/platform-design/design
```

This regenerates the full `gen/` tree (service interface, endpoints, HTTP
client/server, CLI, OpenAPI). Only `design/design.go` and the OpenAPI 3 outputs
are committed; the rest is `.gitignore`d as regenerable and is not used at
runtime.

## Runtime

The runtime handlers that implement this contract live one directory up
(`clients/platform/*.go`) as native `zip` handlers, mounted in the cloud binary
at `/v1/platform`, org-scoped by the validated `X-Org-Id`, deploying via the
operator `hanzo.ai/v1` Service CR into `tenant-<org>`.
