# apps/tools — the tool plane

One registry where everything an ORG can call is a `Tool` with a `Source`, a
JSON-Schema, a per-`(org, project)` activation state and an optional price.
Cloud's own typed ops are NOT here: they are code, projected at build time onto
the fleet's MCP door. What lives here is ROWS.

Read `tool.go` first — the package comment is the design. This file is the rest:
the shelf, the door, and the one thing that does not exist yet.

## The shelf — `catalog.go`

Our canonical copy of what the public MCP registries publish
(`registry.modelcontextprotocol.io`, `GET /v0/servers?version=latest`, cursor
paginated). Keyed by the publisher's reverse-DNS name, so a sync is idempotent by
construction.

A row has two halves and they never mix:

- **upstream** — name, vendor, description, repo, site, version, transports,
  packages, remotes. Replaced wholesale on every sync.
- **curation** — `hidden`, `featured`, `official`, `logo`. The sync statement does
  not NAME these columns, so it cannot touch them. `official` is the one that is
  both: derived on every sync until an admin sets it, at which point `curated=1`
  and the derivation stops running over the decision.

`official` is mechanical, not editorial (`isOfficial`): the namespace must attest a
DOMAIN — not an account on a code forge — and that domain must serve the listing's
own endpoint, site or repository. `com.stripe` serving `mcp.stripe.com` is
official; a proxy on someone's `workers.dev` is not; `io.github.alice` is not. What
the data cannot settle — a re-hoster's own-brand wrapper — is what the admin
override is for.

## Enablement is registration — `external_mcp.go`

There is ONE way an org gains a server: `POST /v1/mcp/servers`, with either a
`url` it typed or a `listing` it picked. Both write the same row; `source` is
derived from whether a listing is recorded. There is no second store, no second
lifecycle, and no second place to forget the credential is in KMS.

The server id PREFIXES every tool name the server contributes. A hand-registered
server keeps its random handle; an enabled listing asks for the vendor's `brand`,
so the tools read `stripe_charge` rather than `m4f21c8_charge`. `Dispatch` cuts the
tool name on the FIRST underscore, which is why `sanitize` drops underscores from
an id rather than mapping them.

## The door — `door.go`

The fleet serves ONE MCP door at `POST /v1/mcp`. Its build-time half is 549 typed
ops across 112 lazily-mounted plugins, projected into `plugin/<app>/mcp.json` and
served as bytes. Its PER-CALLER half is this package: `Door()` returns a
`zip.Source` (zip >= v1.18.14) whose `Tools` is the registry's activated,
dispatchable set for the caller and whose `Call` IS `callTool` — so the door adds
no policy of its own.

Wiring, end to end:

- `plugin/tools/main.go` — `Door: tools.Door()` on the `cloud.Plugin`.
- `build.go` `door(plugins)` — a binary has ONE per-caller source; two is a
  composition error.
- `serve.go` — passes it as `zip.MCPConfig{Source: …}`.
- `manifest/apps.go` — the tools row is `Open: true`, and `manifest/plugin.go`
  `App.Plugin()` stamps it onto the `zip.Plugin`.
- `cmd/cloud/main.go` `run()` — `zip.MCPConfig{Path: "/v1/mcp"}`, unchanged.

The host asks the open plugin only on a `tools/list` that NAMES a caller
(`X-Org-Id`). An anonymous list is still a memcpy and still starts no child.

## Running a stdio package in our cloud — NOT BUILT, and why

The catalog carries `packages[]` with `runtimeHint: npx|uvx|docker` because that
is what upstream publishes. **Nothing here runs one.** There is no
`POST /v1/tools/catalog/{id}/run`, and there is no stub of one: a route that
answered would be a lie about a capability the fleet does not have.

What was checked, and what is actually there. Lines are as of `c7a82cd1`; the
SYMBOL is the durable handle, because a doc commit moves every number below it:

| seam | where | verdict |
|---|---|---|
| org-scoped container launch | `apps/platform/platform.go:236` routes `POST /v1/run` → `apps/platform/run.go:62` `run` → `k8s.go:603` `applyService` → `k8s.go:628` `Create` on `k8s.Apps` | REAL. Billing-gated, KMS-sealed env, `tenant-<org>` namespace. |
| the CR it writes | `apps/platform/k8s.go:524` `serviceCR` | **No `command`, no `args`.** It carries `image`, `replicas`, `ports`, `imagePullSecrets`, optional `env`/`volumes`/`ingress`/`autoscaling` — nothing else. |
| arbitrary-argv execution | `apps/platform/k8s.go:845`, `:1103`, `:1179` — `Resource(jobsGVR).Namespace(k.buildNS).Create` | REAL `batchv1` Jobs, but platform-internal: build/smoke, `buildNS`, not an org-addressable workload. |
| user code execution | `apps/functions/invoke.go:39` `CODE_EXEC_UPSTREAM`, POST `/exec` | A PROXY. The executor is another repo; unset ⇒ 503. `runtime:"container"` is accepted and `image` is stored, but neither is read at invoke time. |
| in-process execution | `apps/connectorruntime/runtime.go` (goja), `apps/goja` | REAL but JS-only: no filesystem, no process spawn. |
| stdio ↔ HTTP MCP bridge | — | **Does not exist.** No `stdio`, `npx`, `uvx` or `streamable` in any non-CLI Go path. |
| the transport we speak | `external_mcp.go` `rpc` | Single-shot JSON-RPC POST. NOT streamable HTTP: no SSE, no `Mcp-Session-Id`, no resumption. |
| reaching an in-cluster address | `external_mcp.go` `validateServerURL` + `guardedHTTPClient` | Refuses private/loopback at registration AND at dial. A sandbox pod is exactly what it is built to refuse. |

So the missing pieces are four, and three are outside this repo:

1. **A `command`/`args` path on the App CR.** `serviceCR` can grow the fields in an
   afternoon; the `hanzo.ai/v1` CRD and the operator that reconciles it are a
   separate repo (`apps/platform/k8s.go:3-4` names it). Until the operator
   accepts them, writing them is writing into a field nothing reads.
2. **A bridge image.** `ghcr.io/hanzoai/mcp-bridge` does not exist: a container
   that takes `{registry, identifier, version, runtime, args, env}`, spawns the
   stdio process, and serves streamable-HTTP MCP in front of it. It is the whole
   of the new code, it belongs in its own repo, and it is built by the fleet's own
   CI — never on a developer's box.
3. **A streamable-HTTP client here.** `rpc` speaks one-shot JSON-RPC. A bridged
   stdio server is stateful (`initialize` then a session), so `mcpProvider` needs
   SSE + `Mcp-Session-Id` before it can talk to one. This is the only piece that
   is genuinely ours and genuinely missing.
4. **An in-cluster exemption in the SSRF guard.** Today's guard is right for a URL
   an ORG supplies. A sandbox endpoint is one WE minted, so it must be recorded as
   such on the server row — never by widening the guard for everyone.

The shape it should take when those land, so it stays one way to do everything:

    POST /v1/tools/catalog/{id}/run   (SuperAdmin-allowlisted listings first)
      → platform.applyService(org, project, bridge image, env from the package)
      → the endpoint it returns is written as an MCPServer row with
        listing=<id> and source=catalog, exactly as an enable does
      → the door composes it like any other remote, with no new code in door.go

That last line is the test of the design: running a package must produce the SAME
registration record an enablement does, or the plane has grown a second kind of
server and everything downstream has to learn about it.
