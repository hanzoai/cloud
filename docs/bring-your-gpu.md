# Bring your GPU

One fleet. Two ways to add a GPU. Two things a GPU does.

```
                 ┌─────────────────────── your org's GPU fleet ───────────────────────┐
   CONNECT (BYO) │                                                                     │
  hanzo gpu      │   ┌── a GPU node ──────────────┐      ┌── a GPU node ────────────┐  │
  connect  ─────▶│   │ studio.render  (diffusion) │      │ engine.serve             │  │
                 │   │ engine.serve   (models)    │      │  hanzo-engine :1234      │  │
   DEPLOY (cloud)│   └────────────────────────────┘      │  OpenAI + Anthropic      │  │
  console →      │                                       └──────────┬───────────────┘  │
  Deploy GPU ───▶│                                                  │ advertised        │
                 └──────────────────────────────────────────────────┼───────────────────┘
                                                                     │  GET /v1/fleet/workers
                          Studio renders ◀── studio.render           │  POST /v1/add-provider
                          api.hanzo.ai model calls ◀── engine.serve ─┘  (Type=Local → engine)
```

A GPU in your fleet can run a **Studio diffusion worker** (`studio.render`) and/or a
**hanzo-engine model server** (`engine.serve`) — the two job types coexist on one
fleet. Where the GPU comes from is orthogonal: **Connect** hardware you already own,
or **Deploy** one in the cloud.

---

## Add a GPU — two ways, same fleet

### Connect (BYO — bring your own)

Any machine with a GPU joins the fleet with an outbound, NAT-safe worker — nothing
listens for inbound connections.

```sh
hanzo login
hanzo gpu connect
```

Or flip the toggle in **Hanzo Desktop → Settings → Cloud GPU → "Connect this device's
GPU to Hanzo Cloud."** (The toggle spawns the same `hanzo gpu connect` CLI, bridging
the desktop's IAM token via `HANZO_TOKEN`.)

The machine registers a heartbeating presence record and shows up on
`console.hanzo.ai` **GPUs** and **Machines** pages with a **BYO** badge
(`provider=byo`). Stop with Ctrl-C (offline after ~90s) or `hanzo gpu disconnect`
(removes the row).

### Deploy (cloud)

On the console **GPUs** page, click **Deploy GPU** — the existing Visor/DOKS provision
flow (`POST /v1/machines/launch`). Cloud GPUs are prepay-only (card required, 24-hour
minimum). They show up with a **Cloud** badge.

Both actions live together on the GPUs page: bring your own **or** spin up cloud.

---

## What a connected GPU does

### `studio.render` — Studio diffusion worker (default)

`hanzo gpu connect` claims `studio.render` jobs from the org's `gpu-jobs` queue and
drives a local ComfyUI/Studio server at `127.0.0.1:8188` (the same prompt graph
`studio.hanzo.ai` submits). Always on — the worker claims jobs the moment it connects.

### `engine.serve` — hanzo-engine model server (`--serve-engine`)

```sh
hanzo gpu connect --serve-engine
```

This advertises a **hanzo-engine** running on the node. hanzo-engine is the
OpenAI-**and**-Anthropic-compatible model server: one axum port (default
`0.0.0.0:1234`) serves both wire formats:

| API | Routes |
|-----|--------|
| **OpenAI** | `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/images/generations`, `/v1/audio/speech`, `/v1/responses`, `/v1/models`, `/v1/models/reload\|unload\|status` |
| **Anthropic** | `/v1/messages` (Claude Code / Anthropic SDK); the handler translates Anthropic → OpenAI internally |

On connect the worker probes the local engine (`GET {engine-url}/v1/models`), then
advertises its endpoint + model list in the fleet presence record. Confirm it:

```sh
hanzo gpu status                 # shows "↳ engine <url> — ready · N models"
curl -sS https://api.hanzo.ai/v1/fleet/workers \
  -H "Authorization: Bearer $HANZO_TOKEN"       # workers[].engine.{url,apis,models,status}
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--serve-engine` | off | advertise a local hanzo-engine |
| `--engine-url` | `http://localhost:1234` | where the node probes the engine (`GET /v1/models`) |
| `--engine-endpoint` | = `--engine-url` | the URL advertised to the gateway (must be reachable from `api.hanzo.ai`) |
| `--register-provider` | off | auto-`POST /v1/add-provider` for the engine |

---

## Route api.hanzo.ai model calls to a connected GPU

hanzo-engine is OpenAI-compatible, so the gateway registers it as a **`Type=Local`**
provider — it speaks the OpenAI wire format to the engine and auto-appends `/v1` to
`providerUrl`:

```sh
curl -sS https://api.hanzo.ai/v1/add-provider \
  -H "Authorization: Bearer $HANZO_TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "name": "gpu-<host>",
    "category": "Model",
    "type": "Local",
    "providerUrl": "http://<reachable-engine-endpoint>:1234",
    "subType": "<served-model>",
    "compatibleProvider": "<served-model>"
  }'
```

`hanzo gpu connect --serve-engine` prints this exact command ready to run, or
`--register-provider` POSTs it for you. Once registered, `api.hanzo.ai` routes model
calls for that provider to your GPU — powering **chat**, the **API**, **Studio's
LLM/copilot**, and **custom models**. (Want Anthropic wire format upstream instead?
Register `Type=Claude` with the same `providerUrl` — the engine serves both.)

### Reachability & auth — the honest caveats

- **The advertised endpoint must be reachable from `api.hanzo.ai`.** A **cloud** GPU is
  in-cluster (use its Service DNS / private address). A **BYO** node behind NAT is
  reachable by the outbound worker but **not** by the gateway — give it a public URL or
  a tunnel via `--engine-endpoint`. (The `studio.render` path needs no inbound
  reachability — the worker dials out.)
- **`POST /v1/add-provider` is gated to a platform-admin token today.** Org self-service
  BYOK is wired (each org owns its provider rows, keys stored in KMS, never plaintext)
  but fronted by a global-admin filter. Until that gate opens to org admins, register a
  connected GPU with an admin token or from the platform console.

---

## The mental model in one line

**Connect** your GPU or **Deploy** one → it runs **hanzo-engine** (OpenAI + Anthropic
model serving) and/or a **Studio** diffusion worker → powering `api.hanzo.ai` model
calls, Studio renders, and custom models — `engine.serve` and `studio.render`, one
fleet.
