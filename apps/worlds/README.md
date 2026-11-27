# Worlds — private Sekai alpha gateway

`/v1/worlds` is distinct from the `/v1/world` news product. This app forwards
simulation requests to the private model service. It contains no weights or training.

Operator configuration:

- `SEKAI_UPSTREAM_URL`: private runtime HTTP(S) origin, never a client parameter.
- `SEKAI_UPSTREAM_KEY`: KMS-projected shared runtime credential.
- `WORLDS_INFERENCE_UPSTREAM_URL`: optional native Hanzo Engine HTTP(S) origin for
  `POST /v1/worlds/:id/inference` (falls back to `SEKAI_UPSTREAM_URL` when unset).
- `WORLDS_INFERENCE_UPSTREAM_KEY` (or `ENGINE_API_KEY`): optional backend credential
  for `WORLDS_INFERENCE_UPSTREAM_URL`.
- `SEKAI_ALPHA_ORGS`: approved org slugs, comma-separated; empty admits none.
- Enable the existing `worlds` alpha feature flag for each approved organization.

The ordinary Cloud IAM boundary must wrap this plugin. Do not expose a test
composition accepting identity headers directly. Brand + validated org + project
are hashed into the runtime tenant key. Runtime calls replace credentials and
identity, refuse redirects, cap responses and remove private event `capture`.
Planning and `/v1/worlds/:id/inference` are strictly non-executing: unknown
override fields fail closed with HTTP 400, host-owned admissibility constraints
remain authoritative, and `execution_authorized` is enforced as `false`.

Release host and plugin together to install the manifest prefix. The runtime
needs persistent storage and one writer. Do not set its single-tenant mode.

Tests: `GOWORK=off go test ./apps/worlds ./manifest`

The real checkpoint test is separate from unit and browser fixtures. Successful
local tests and merged source do not establish hosted availability.
