# apps/tools — the tool plane

One registry where everything an ORG can call is a `Tool` with a `Source`, a
JSON-Schema, a per-`(org, project)` activation state and an optional price.
Cloud's own typed ops are NOT here: they are code, projected at build time onto
the fleet's MCP endpoint. What lives here is ROWS.

Read `tool.go` first — the package comment is the design. This file is the rest:
the shelf, the endpoint, and the one thing that does not exist yet.

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

## Known gap: a credential can be destroyed, not deleted

`types.KMSClient` (`types/types.go:206`) has `GetSecret`, `PutSecret` and `Sign`
and no removal. The KMS service HAS one — `apps/kms` `Client.Delete` — so the gap
is the interface and `KMSPeer` (`kmspeer.go`), not the store.

So `deleteServer` OVERWRITES a deregistered server's credential with empty rather
than removing the ref (`http.go` `forget`). That destroys the material, which is
the part that matters for a deletion request or a rotation; it leaves an empty ref
behind, which nothing reads because the row is gone.

Closing it properly means adding `DeleteSecret` to `types.KMSClient`, implementing
it on `KMSPeer` over the existing `plane.KMS*` ops, and updating the fifteen
implementations (mostly test fakes across twelve packages). That is a change to a
fleet-wide client and it belongs to whoever owns that client — not smuggled in behind
a catalog feature.

## The endpoint — `door.go`

The fleet serves ONE MCP endpoint at `POST /v1/mcp`. Its build-time half is 549 typed
ops across 112 lazily-mounted plugins, projected into `plugin/<app>/mcp.json` and
served as bytes. Its PER-CALLER half is this package: `Endpoint()` returns a
`zip.Source` (zip >= v1.18.14) whose `Tools` is the registry's activated,
dispatchable set for the caller and whose `Call` IS `callTool` — so the endpoint adds
no policy of its own.

Wiring, end to end:

- `plugin/tools/main.go` — `Endpoint: tools.Endpoint()` on the `cloud.Plugin`.
- `build.go` `endpoint(plugins)` — a binary has ONE per-caller source; two is a
  composition error.
- `serve.go` — passes it as `zip.MCPConfig{Source: …}`.
- `manifest/apps.go` — the tools row is `Open: true`, and `manifest/plugin.go`
  `App.Plugin()` stamps it onto the `zip.Plugin`.
- `cmd/cloud/main.go` `run()` — `zip.MCPConfig{Path: "/v1/mcp"}`, unchanged.

The host asks the open plugin only on a `tools/list` that NAMES a caller
(`X-Org-Id`). An anonymous list is still a memcpy and still starts no child.

## Paying for a tool — `registry.go` `Charger`, and where it binds

A priced tool used to be unpayable. `tools.SetCharger` had no production caller and
`x402.Publish` had none either, so every monetized listing resolved to
`ErrChargerUnset` — a permanent 402 with no terms in it, which no client could ever
satisfy. Prices were in the catalog and revenue was not.

The client is now closed in ONE place, `apps/marketplace/payments.go`, wired from
`marketplace.Mount`:

- `x402.Publish(&registry{store})` — the price table. Resource ids are `tool:<name>`
  (`resourceOf`), because every tool call arrives on the same route and a path
  cannot say which capability is being bought. The prefix also keeps the table from
  ever pricing a URL by accident.
- `tools.SetCharger(charger{})` — settlement, which is `x402.Settle` on that id.

The `Charger` takes a TOOL NAME and nothing else. Who pays is `principal.Ledger` on
the request; what it costs and who is paid are the payment layer's table. Note the
old shape billed `p.Owner` — the HOME org — which is the rule `principal.BillingOrg`
repudiates: the SELECTED org pays. Deleting the field deleted the bug.

Every dispatch is offered to the charger, free ones included: "is this priced" is one
lookup in the same table that settles, and asking it twice is how a gate and a
settlement come to disagree.

**Prices are exact.** `Price.Amount` is `money.Amount`, 18-dp. A per-call price of
`$0.0025` is a quarter of a cent and stays one; in the old `AmountCents int64` it was
`0`, i.e. free. Per-token prices are the normal case on a tool plane, so cents were
never the right type. `CheapestPublicForTool` also compares in Go rather than SQL:
`$10` is `10^19` atto, past int64, so `ORDER BY CAST(price AS INTEGER)` would
mis-order the expensive half of the shop.

### The process boundary — CLOSED, over the internal plane

The three clients are process-globals (`x402.reg`, `tools.std`, wallets' mounted
singleton), so the wiring above binds **within one process**. The shipped fleet runs
**one process per app**, and that is the only topology there is:

- `manifest/apps.go` declares `tools`, `marketplace`, `x402`, `wallets` as ordinary
  prefix-routed rows. `Coresident` — the one flag that puts an app in another's
  process — is set by exactly one app, `zen` (`manifest/apps.go:194`).
- `Dockerfile` builds one binary per row into `/plugins`; `cmd/cloud` is a router
  that `zip.Load`s each as a **child process** (`cmd/cloud/main.go:257`).
- The fused monolith that linked all subsystems is **deleted**
  (`cmd/cloud/main.go:1-9`).

So the fleet bound nothing, and the failure was worse than the earlier reading of it.
The `tools` process refused a dispatch whose *registry row* declared a price — but a
marketplace price is a row in the LISTING store, not on the tool, so a listed tool
with no declared price was dispatched **free**. Proven, not asserted: remove the first
hop below and `TestSplitFleetSettlesAPricedTool` answers `200 {"ran":true}` to an
unpaid call for a $0.0025 tool.

The fix is the one this fleet already had a pattern for. `resource_billing_peer.go`
documents the identical bug — *"Splitting apps into their own binaries turned every
priced create free without changing a line of billing code"* — and the answer was to
ASK the owning process. Four ops, each in the process that owns the answer:

| # | op | who serves it | where |
|---|---|---|---|
| 1 | `x402_settle` | x402 | `apps/x402/rpc.go`, called from `apps/tools/charge_peer.go` |
| 2 | `market_price` | marketplace | `apps/marketplace/rpc.go`, called from `apps/x402/peer.go` |
| 3 | `wallets_payee` | wallets | `apps/wallets/rpc.go`, called from `apps/x402/peer.go` |
| 4 | `finance_credit` | commerce | `apps/commerce/credit_rpc.go`, called from `apps/x402/peer.go` |

The payer debit reuses `finance_record`, which already existed. **One policy, two
transports**: the in-process global stays the fast path where the owner is
co-resident, the plane answers where it is not, and `apps/marketplace/payments_test.go`
(co-resident) and `apps/marketplace/split_test.go` (five real processes) assert the
same properties over each.

Fail-closed at every hop, and the one exception is a decided fact rather than a
guess. `cloud.ErrNoPeer` means the router does not know the app — it is not part of
this deployment — and only that restores the old "nothing is priced" answer. An
unreachable peer that IS deployed is an outage: the call is refused, never served
free. `TestSplitFleetPriceOutageIsNotFree` kills the marketplace process and asserts
the priced tool is refused rather than given away.

**Waking a peer.** A lazy app starts on a request reaching its prefix, and a plane
call never touches the router — so before this, an app reached only over its socket
was never started. `cmd/cloud/wake.go` publishes `zip.App.Start` as `host_start` on
the router's own socket, and `cloud.Ask` asks it when a socket is unbound
(`cmd/cloud/wake_test.go`). "This fleet runs no such app" comes back as a FIELD on a
200, never as a 404: three different things answer 404 on that wire, one of them is
an older host pod answering "unknown op" during a rolling deploy, and reading that as
absence gives away every priced tool for the length of the window.

**Strings from the plane must be copied before they are kept.** ZAP decodes text
zero-copy over a buffer the transport reuses, so a retained reply string mutates
under its owner — that is what turned a recorded payee address into the bytes of a
later message's amount. `cloud.Ask` detaches every reply. The REQUEST side has no
such client: a handler's decoded input aliases the server's body buffer, which
fasthttp recycles, so anything a plane op keeps past its return must be cloned at the
op (`apps/x402/rpc.go` does, for the resource it writes to the settlement row).

### Still NOT closed — a stranded debit has no sweeper

`settleLedger` moves the payer's side first. If the payee credit then fails, nothing
is served and no settlement row is recorded, so the settlement has not happened — but
the debit has. The client's retry completes it, because the same authorization yields
the same id and both writes are idempotent on it.

That recovery is not unconditional. The authorization carries `validBefore`
(`DefaultValidFor`, 300s), so a client that stops retrying for five minutes can never
complete that settlement and the buyer is permanently out the money with nothing
served. A `store.record` failure after both sides landed has the same shape.

The sweep is exact and is written down so it can be built rather than rediscovered:
the debit is keyed `RequestID` = the settlement id in commerce's usage rows, and the
settlement store is keyed on the same id, so it is *every usage row with provider
`x402` whose RequestID has no `settlements` row, older than the validity window* —
complete it or credit the payer back. It needs a reader of two stores in two
processes plus a schedule, which is its own piece of work.

### Still NOT closed — a listing claims a tool NAME, and names are fleet-wide

`CheapestPublicForTool` (`apps/marketplace/store.go`) resolves a price by tool NAME
across every publisher, and takes the cheapest. That was inert while nothing could
pay; with the rail live it is a live client, so state the shape plainly:

- tool names are a flat fleet-wide namespace (`tool.go`), but an org registering an
  external MCP server contributes names prefixed by that server's `brand` — so two
  orgs registering a server branded `stripe` both contribute `stripe_charge`, and
  those are different capabilities with one name;
- any org may publish a PUBLIC listing for any name. The cheapest wins. So a listing
  published by someone who does not own the capability can undercut the real one and
  receive its payments.

The payee is still always a wallet in the LISTING's own publisher org — a buyer
cannot redirect anything, and no cross-tenant read is possible — so the exploit is a
seller-side one: divert a small payment, and suppress the genuine price.

Closing it is a decision about the listing KEY, not about the rail: either the
resource id carries the publishing org, or a name with two public claimants is
refused as unroutable exactly as zip refuses two plugins owning one tool name. Both
change what a listing means, which is the marketplace's call to make.

### Still NOT closed — the tool REGISTRY across the boundary

Same bug class, different client, and it is why a seller cannot list and a buyer cannot
install in the shipped fleet:

- `marketplace.publish` calls `tools.Default().Exists`, and the marketplace binary
  registers no providers — so every publish answers `422 unknown tool`.
- `marketplace.install` calls `tools.Default().Activate` against a registry with no
  activation store — so every install answers `500 activation store not configured`.

Both need their own ops on `tools` (an existence check and an activation write), and
they belong to whoever owns the activation client, not smuggled in behind a payment
fix. `split_test.go` seeds the listing ROW and activates in the tools process, and
says so at the line rather than faking a provider to hide it.

## Running a stdio package in our cloud — NOT BUILT, and why

The catalog carries `packages[]` with `runtimeHint: npx|uvx|docker` because that
is what upstream publishes. **Nothing here runs one.** There is no
`POST /v1/tools/catalog/{id}/run`, and there is no stub of one: a route that
answered would be a lie about a capability the fleet does not have.

What was checked, and what is actually there. Lines are as of `c7a82cd1`; the
SYMBOL is the durable handle, because a doc commit moves every number below it:

| client | where | verdict |
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
      → the endpoint composes it like any other remote, with no new code in door.go

That last line is the test of the design: running a package must produce the SAME
registration record an enablement does, or the plane has grown a second kind of
server and everything downstream has to learn about it.
