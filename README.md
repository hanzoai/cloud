<p align="center"><img src=".github/hero.svg" alt="Hanzo Cloud" width="880"></p>

# Hanzo Cloud

**The Open AI Cloud as one deployment.** Identity, secrets, data, AI, gateway, observability, and the console — every Hanzo subsystem behind one origin and one `/v1`, each its own binary, composed by a light host router through the plugin contract in [HIP-0106](https://github.com/hanzoai/hips/blob/main/HIPs/hip-0106-hanzo-plugin-contract.md).

[![Status](https://img.shields.io/badge/status-beta-blue)]()
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)]()

The same artifact serves `api.hanzo.ai`, `api.lux.cloud`, `api.zoo.cloud`, `api.osage.cloud`, and every white-label reseller. Brand, enabled subsystems, and org scope are deployment configuration — one binary, one origin, no sidecars.

## Quick start

```bash
# Run the unified binary. `:latest` to try it; pin a v1.x.y tag for anything real
# — the tags are cut per build, so any number written here is stale by tomorrow.
docker run -p 8080:8080 ghcr.io/hanzoai/cloud:latest
```

Open <http://localhost:8080> for the embedded console; the API is served under `/v1` on the same origin.

Build this repo's own client binary with `go build ./cmd/hanzo` — see below for what it
serves and what it delegates. It is NOT what `curl -fsSL https://hanzo.sh | sh` installs;
that gets the Rust CLI (`hanzoai/cli`), which is the primary `hanzo` on a developer's
machine and whose verbs are different.

## What this is

`hanzoai/cloud` serves the whole API from one origin. `cmd/cloud` is the entry point: it
links `zip`, the app manifest and the console embed — and nothing else. It knows only
where each app lives and what path it answers, never what the app does. Each subsystem
(iam, kms, base, gateway, ai, commerce, vfs, mq, dns, amqp, mcp, o11y, tasks, …) is its
own `plugin/<name>` binary serving its own prefixes through the same `cloud.Listen`
middleware it would serve standalone.

Apps start **lazily**, on the first request that reaches their prefix; the four that own
a listener (`pubsub`, `kafka`, `amqp`, `o11y`) say so and start with the host. That is what makes the whole fleet affordable — an app nobody calls
costs a route entry and a struct, not a process and a resident set.

This was one fused process once, and that binary is gone: it linked every subsystem's
graph into a ~3105-package build, and `apps.Wire()` went with it.

The same deployment serves `api.hanzo.ai`, `api.osage.cloud`, `api.lux.cloud`,
`api.zoo.cloud`, and every white-label reseller. Brand, enabled subsystems, and org
scope are deployment configuration.

## `hanzo` — cloud control CLI

`cmd/hanzo` is the **client-only** control binary: a thin client over Hanzo IAM
(`hanzo.id`), the platform control plane (`platform.hanzo.ai/v1`) and the cloud
`/v1` API, inventing no parallel API. It cannot serve a subsystem — that is
`cmd/cloud`'s job.

**Two different programs answer to `hanzo`, and this is the one almost nobody has.**
A developer installs the Rust CLI (`hanzoai/cli`) from `hanzo.sh`; it becomes their
`hanzo`, and it writes `hanzo-node` as a symlink to itself. THIS binary is the Go
control CLI, built from this repo. When it is the `hanzo` on a machine, a verb it does
not own is handed to whatever `hanzo-node` resolves to (`cli.Passthrough`), so the
single name is a superset of both — but that delegation runs in this direction only.
Read the verbs below as `cmd/hanzo`'s, not as "what `hanzo` does": on a normal
developer machine `hanzo login` and `hanzo deploy` reach the Rust CLI, which has
neither, and it reads them as a task for the coding agent.

`cli.IsControlVerb` draws the line off the cobra command tree itself, so the router and
the tree cannot drift apart. The complete set it owns:

```bash
hanzo login                       # IAM password grant against hanzo.id → token in ~/.hanzo (0600)
hanzo logout
hanzo whoami                      # identity from the stored token (--verify hits IAM userinfo)
hanzo auth …                      # token / switch / status
hanzo apps list                   # platform apps board: declared/running/latest tag + drift + health
hanzo apps get <org>/<app>/<env>  # one app row
hanzo deploy <container> --project <p> --env <e>   # rolling, zero-downtime redeploy
hanzo clusters …                  # dedicated DOKS cluster lifecycle
hanzo build <repo> --sha <sha> --image <img>       # platform-native Kaniko build, no GitHub builders
hanzo run <task>                  # one-off task on the platform
hanzo agent … | hanzo bot …       # managed agents and bot nodes
hanzo engine … | hanzo runner …   # local engine, and this machine as a CI runner
hanzo link | hanzo unlink         # attach this machine to the fleet (`hanzo gpu connect` rides here)
hanzo security …                  # rules / scan
hanzo config set <k> <v>          # ~/.hanzo/config preferences
hanzo version
hanzo completion bash|zsh|fish    # shell completion for every verb above
```

Global flags: `--org`, `-o/--output table|json`, `--platform-url`, `--iam-issuer`,
`--platform-token`. Tokens resolve from flag → env → `~/.hanzo` (never hardcoded):
the IAM user token is the identity; the platform control plane is service-token
authed (it cannot validate user tokens), so `apps`/`deploy`/`clusters` use
`--platform-token` / `HANZO_PLATFORM_TOKEN` / `PLATFORM_SERVICE_TOKEN`, and
`build` uses `HANZO_BUILD_TOKEN`, falling back to the IAM login — a build is
attributed to the organization its credential carries, so it presents one that
names an organization.

Install the Rust CLI: `curl -fsSL https://hanzo.sh | sh`, or
`brew install hanzoai/tap/hanzo`. It is `hanzoai/cli`; this module serves `/v1`, ships
plugins, and builds the control half above (`go build ./cmd/hanzo`).

## Subsystems mounted

`manifest/apps.go` is the source of truth: every app that ships as its own binary, in
mount order — which IS the routing order, first matching prefix wins. Three facts per
row and no more (name, the paths it answers, whether it must already be running), because
that is the whole of what the light host needs to know. What an app DOES it states once
in its own `plugin/<name>/main.go`.

- `iam` — identity & access (users, orgs, roles, OIDC/JWKS per HIP-0026)
- `base` — per-org SQLite + in-process extension runtimes (HIP-0105)
- `kms` — secret custody (sealed secrets, HIP-0027)
- `commerce` — checkout, billing, pricing, invoicing (light router; NOT in PCI-DSS scope)
- `ai` — AI control plane: inference, RAG, model hub, agents, MCP management
- `gateway` — HTTP routing + policy
- `o11y` — metrics / traces / logs
- `vfs` — virtual filesystem / object-store abstraction
- `mq` — message queue
- `dns`, `mq`, `tasks`, `auto`, `git`, … — the rest are rows in `manifest/apps.go`

## Deployment modes

Same artifact; different startup configuration:

```bash
cloud --brand=hanzo  --domain=hanzo.ai
cloud --brand=osage  --domain=osage.cloud
cloud --brand=lux    --domain=lux.cloud
cloud --brand=zoo    --domain=zoo.cloud
```

## Architecture

```
                 api.{org}.{brand}
                          |
              cmd/cloud — the host router
              (links zip + manifest + webui, nothing else)
                          |
   +----------+----------+----------+----------+----------+
   |    iam   |   base   |   kms    |    ai    | gateway  | ...
   |  its own |  its own |  its own |  its own |  its own |
   |  process |  process |  process |  process |  process |
   +----------+----------+----------+----------+----------+
   per-org SQLite (HIP-0302)   |   Hanzo IAM JWKS (HIP-0026)
   replicate -> S3 (HIP-0107)  |   ZAP inter-subsystem RPC
```

Every app is loaded through the same `Mount` client and answers on its own prefix; the
host takes the first prefix that matches and starts the app if it is not up yet. The
console is registered LAST so no app prefix can be shadowed. Cross-subsystem calls ride
ZAP; no subsystem reaches into another's store.

The host owns three things no app can: it serves the white-labelled console at `/`, it
threads the deployment's operator flags to the children as `CLOUD_*` env, and it SCOPES
CREDENTIALS — it scrubs the KMS root key from its own environment so no child inherits
it, and hands it to the kms broker child alone.

## White-label fork pattern

Customers fork `hanzoai/cloud` to launch their own ecosystem. Brand detection, enabled
subsystems, and ZAP endpoints (payments / vault backends) are all deployment
configuration.

## Web framework

[zap-proto/zip](https://github.com/zap-proto/zip) — Sinatra-style Go web framework built
on Fiber v3. The ONE Go web framework. No `.Fast` escape hatch. That is the module path
this repo imports (`github.com/zap-proto/zip`, currently v1.18.22); `hanzoai/zip` is the
old home and is not what `go.mod` resolves.

## Console UI — embedded in the host

The host binary serves the console (`@hanzo/gui`, `hanzoai/console` — private) UI at the
web root AND routes `/v1` — one origin, no separate console Service. The UI is compiled
in via `//go:embed` (see `webui.go`).

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
the FULL `@hanzo/gui` console into the host binary. The Dockerfile console stage
FAILS HARD if that bundle is missing or degenerate — the placeholder shell can
never silently ship to prod (escape hatch: `--build-arg ALLOW_PLACEHOLDER=1` for a
pure-Go dev image).

## Specs

Implements, by the filenames in [hanzoai/HIPs](https://github.com/hanzoai/HIPs/tree/main/HIPs):

- [HIP-0014](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0014-application-deployment-standard.md) Application Deployment
- [HIP-0026](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0026-identity-access-management-standard.md) Identity & Access Management
- [HIP-0027](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0027-secrets-management-standard.md) Secrets Management
- [HIP-0105](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0105-in-process-extension-runtime-standard.md) In-Process Extension Runtime
- [HIP-0106](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0106-hanzo-plugin-contract.md) Hanzo Plugin Contract
- [HIP-0107](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0107-streaming-replication-over-vfs.md) Streaming Replication over VFS
- [HIP-0129](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0129-eval-the-judgment-plane.md) Eval — the Judgment Plane
- [HIP-0302](https://github.com/hanzoai/HIPs/blob/main/HIPs/hip-0302-encrypted-sqlite-replication-standard.md) Encrypted SQLite Replication

## Status

In production. It serves `api.hanzo.ai` and the white-label cloud surfaces today, with
per-org SQLite (HIP-0302) and the embedded console. `manifest/apps.go` is the one ordered
list of everything mounted — every app, four of them eager. For repo-level engineering
doctrine (module graph, route-table projections, cross-subsystem clients), see
[`LLM.md`](./LLM.md).

## Hanzo — the Open AI Cloud

Open source · every language · on-chain settlement. [hanzo.ai](https://hanzo.ai) · [docs.hanzo.ai](https://docs.hanzo.ai)

**SDKs in every language** — [Python](https://github.com/hanzoai/python-sdk) (flagship) · [TypeScript](https://github.com/hanzo-js/sdk) · [Go](https://github.com/hanzo-go/sdk) · [Rust](https://github.com/hanzo-rs/sdk) · [C++](https://github.com/hanzo-cpp/sdk) · [Swift](https://github.com/hanzo-swift/sdk) · [Kotlin](https://github.com/hanzo-kt/sdk) · [umbrella](https://github.com/hanzoai/sdk)
