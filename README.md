<p align="center"><img src=".github/hero.svg" alt="Hanzo Cloud" width="880"></p>

# Hanzo Cloud

**The Open AI Cloud as one Go binary.** Identity, secrets, data, AI, gateway, observability, and the console — every Hanzo-native subsystem mounted into a single multi-org process. Per [HIP-0106](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0106-unified-hanzo-cloud-binary.md).

[![Status](https://img.shields.io/badge/status-beta-blue)]()
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)]()

The same artifact serves `api.hanzo.ai`, `api.lux.cloud`, `api.zoo.cloud`, `api.osage.cloud`, and every white-label reseller. Brand, enabled subsystems, and org scope are deployment configuration — one binary, one origin, no sidecars.

## Quick start

```bash
# Run the unified binary (pin a released version)
docker run -p 8080:8080 ghcr.io/hanzoai/cloud:v1.801.206

# Or install the CLI + server
go install github.com/hanzoai/cloud/cmd/hanzo@latest
brew install hanzoai/tap/hanzo
```

Open <http://localhost:8080> for the embedded console; the API is served under `/v1` on the same origin.

## What this is

`hanzoai/cloud` is one Go binary that mounts every Hanzo subsystem (iam, kms, base, gateway, ai, commerce, vfs, mq, dns, amqp, mcp, o11y, tasks, …) into a single multi-org process. The same artifact serves `api.hanzo.ai`, `api.osage.cloud`, `api.lux.cloud`, `api.zoo.cloud`, and every white-label reseller. Brand, enabled subsystems, and org scope are deployment configuration.

## `hanzo` — cloud control CLI

The same binary is also a gcloud/doctl-class CLI. The first token selects the mode:

- `hanzo <subsystem>` — **server mode**: serve a subsystem (`hanzo iam`, `hanzo cloud`, …).
- `hanzo <verb>` — **client mode**: control the live estate. A thin client over
  Hanzo IAM (`hanzo.id`), the platform control plane (`platform.hanzo.ai/v1`),
  and the cloud `/v1` API — it invents no parallel API.

```bash
hanzo login                       # IAM password grant against hanzo.id → token in ~/.hanzo (0600)
hanzo whoami                      # identity from the stored token (--verify hits IAM userinfo)
hanzo apps list                   # platform apps board: declared/running/latest tag + drift + health
hanzo apps get <org>/<app>/<env>  # one app row
hanzo deploy <container> --project <p> --env <e>   # rolling, zero-downtime redeploy
hanzo clusters list|get|create|select|target       # dedicated DOKS cluster lifecycle
hanzo build <repo> --sha <sha> --image <img>       # platform-native (arcd/Kaniko) build, no GitHub builders
hanzo k8s target                  # the org's resolved deploy target (kubeconfig never returned)
hanzo config set <k> <v>          # ~/.hanzo/config preferences
```

Global flags: `--org`, `-o/--output table|json`, `--platform-url`, `--iam-issuer`,
`--platform-token`. Tokens resolve from flag → env → `~/.hanzo` (never hardcoded):
the IAM user token is the identity; the platform control plane is service-token
authed (it cannot validate user tokens), so `apps`/`deploy`/`clusters` use
`--platform-token` / `HANZO_PLATFORM_TOKEN` / `PLATFORM_SERVICE_TOKEN`, and
`build` uses `HANZO_BUILD_TOKEN` / `PLATFORM_BUILD_CALLBACK_TOKEN`.

Install: `go install github.com/hanzoai/cloud/cmd/hanzo@latest`, or `brew install hanzoai/tap/hanzo`.

## Subsystems mounted

Each subsystem exposes `func Mount(app *zip.App, deps cloud.Deps) error` and wires its own `/v1/<name>/*` routes onto the shared app.

- `iam` — identity & access (users, orgs, roles, OIDC/JWKS per HIP-0026)
- `base` — per-org SQLite + in-process extension runtimes (HIP-0105)
- `kms` — secret custody (sealed secrets, HIP-0027)
- `commerce` — checkout, billing, pricing, invoicing (light router; NOT in PCI-DSS scope)
- `ai` — AI control plane: inference, RAG, model hub, agents, MCP management
- `gateway` — HTTP routing + policy
- `o11y` — metrics / traces / logs
- `vfs` — virtual filesystem / object-store abstraction
- `mq` — message queue
- `dns`, `amqp`, `mcp`, `auto`, `tasks`, … — full list per HIP-0106

## Deployment modes

Same binary; different startup configuration:

```bash
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=hanzo  --domain=hanzo.ai
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=osage  --domain=osage.cloud
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=lux    --domain=lux.cloud
cloud --enable=iam,base,kms,commerce,ai,gateway,o11y --brand=zoo    --domain=zoo.cloud
```

## Architecture

```
                 api.{org}.{brand}
                          |
                   hanzoai/cloud (one Go binary)
                          |
   +----------+----------+----------+----------+----------+
   |    iam   |   base   |   kms    |    ai    | gateway  | ...
   |  Mount() |  Mount() |  Mount() |  Mount() |  Mount() |
   +----------+----------+----------+----------+----------+
   per-org SQLite (HIP-0302)   |   Hanzo IAM JWKS (HIP-0026)
   replicate -> S3 (HIP-0107)  |   ZAP inter-subsystem RPC
```

Every subsystem mounts through the same `Mount(app, deps)` seam. Cross-subsystem calls ride a narrow in-process interface; no subsystem reaches into another's store.

## White-label fork pattern

Customers fork `hanzoai/cloud` to launch their own ecosystem in one binary. Brand
detection, enabled subsystems, and ZAP endpoints (payments / vault backends) are all
deployment configuration.

## Web framework

[hanzoai/zip](https://github.com/hanzoai/zip) — Sinatra-style Go web framework
built on Fiber v3. The ONE Go web framework. No `.Fast` escape hatch.

## Console UI — embedded in the ONE binary

The same `hanzoai/cloud` binary serves the [console](https://github.com/hanzoai/console)
(`@hanzo/gui`) UI at the web root AND the `/v1` API from one process — one
artifact, one origin, no separate console Service. The UI is compiled in via
`//go:embed` (see `webui.go`).

Pipeline (in the `Dockerfile`, before `go build`):

```
console stage  →  build console static bundle  →  /out
      COPY --from=console /out/ → src/webui/dist/     (overlays the fallback shell)
build stage    →  go build   →  //go:embed all:webui/dist bakes it into /cloud
```

Serving (`webui.go`, registered LAST in `Serve` so it never shadows the API):

- `GET /` and any client-side route (`/orgs`, `/models`, …) → the SPA shell
  (`index.html`) with `Cache-Control: no-cache`; fingerprinted assets under
  `assets/`/`_next/` are served `immutable` for a year, with brotli/gzip
  precompressed negotiation when the build emits `.br`/`.gz` siblings.
- `GET /v1/*` (and `/zap`, `/healthz`, …) → the API. Real subsystem routes are
  registered before the console catch-all, so they always win; an **unmatched**
  path under an API prefix returns a real 404 (JSON namespace), never HTML.
- Same-origin: the embedded console calls `/v1` on its own host, so the session
  cookie is first-party — no second origin, no CORS.

`webui/dist/index.html` is a committed **fallback shell** (a real same-origin
`/v1` bootstrap) so `go build` always compiles and the binary always serves a UI
even without the Node toolchain. The image build overwrites `webui/dist` with the
real console bundle. See `webui_test.go` for the boot-and-assert tests
(`/` → shell, deep link → shell 200, `/v1/*` → API, unmatched `/v1` → 404).

The `hanzoai/console` `build:embed` script (`scripts/build-embed.mjs`) stashes its
Next server route handlers (BFF proxies that collapse to the cloud `/v1/*` the SPA
calls same-origin), wraps the client catch-all pages for `output: 'export'`,
neutralizes the root layout's request-time `headers()` read, and emits a real
static export at `out/` (a ~360 KB `index.html` + `_next/` chunks). The image
build (and `make webui`) run it and overlay `webui/dist`, so `//go:embed` bakes
the FULL `@hanzo/gui` console into the ONE binary. The Dockerfile console stage
FAILS HARD if that bundle is missing or degenerate — the placeholder shell can
never silently ship to prod (escape hatch: `--build-arg ALLOW_PLACEHOLDER=1` for a
pure-Go dev image).

## Specs

Implements:
- HIP-0014 Application Deployment
- HIP-0026 IAM
- HIP-0027 KMS
- HIP-0037 AI Cloud Platform
- HIP-0105 In-Process Extension Runtime
- HIP-0106 Unified Cloud Binary
- HIP-0129 Open Cloud Planes
- HIP-0302 Encrypted SQLite + ZapDB Durability

## Status

In production. The unified binary serves `api.hanzo.ai` and the white-label cloud
surfaces today, with per-org SQLite (HIP-0302) and the embedded console. Subsystems
continue to land per HIP-0106's migration phases; `apps/apps.go:Wire()` is the one
ordered list of everything mounted. For repo-level engineering doctrine (module
graph, route-table projections, cross-subsystem seams), see [`LLM.md`](./LLM.md).

## Hanzo — the Open AI Cloud

Open source · every language · on-chain settlement. [hanzo.ai](https://hanzo.ai) · [docs.hanzo.ai](https://docs.hanzo.ai)

**SDKs in every language** — [Python](https://github.com/hanzoai/python-sdk) (flagship) · [TypeScript](https://github.com/hanzo-js/sdk) · [Go](https://github.com/hanzo-go/sdk) · [Rust](https://github.com/hanzo-rs/sdk) · [C++](https://github.com/hanzo-cpp/sdk) · [Swift](https://github.com/hanzo-swift/sdk) · [Kotlin](https://github.com/hanzo-kt/sdk) · [umbrella](https://github.com/hanzoai/sdk)
