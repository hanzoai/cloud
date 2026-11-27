# LLM.md — apps/ai

Guidance for apps/ai. Moved from the root LLM.md; cross-cutting rules stay in the root.

## OpenRouter's model list is ai's, and its changes are pulses

hanzoai/ai keeps OpenRouter's whole model list in its own store, synced at start and
hourly fleet-wide under a lease (ai LLM.md "OpenRouter's list, kept in the store"):
every model, priced at OpenRouter's rates plus OpenRouter's fee (`ai_fee_openrouter`,
a percent, seeded 5.5, edited at admin.hanzo.ai Usage policy; `apps/ai/listing.go`
`fee` hands it to ai through `aiobject.SetFees`, read each time the catalog refreshes,
every five minutes), its free ones marked, a dropped one gone. A resold model carries
no margin of Hanzo's: Hanzo's margin is on Zen and Enso, priced in their own catalogs. ai lists, routes and bills from those rows. The free lane reads
them at `GET /v1/models/vendors/openrouter?free=1` (in cluster:
`http://cloud.hanzo.svc:8000/v1/models/vendors/openrouter?free=1`, anonymous) in
place of the vendor's list; `GET /v1/models/vendors/openrouter/events` is the history
and SuperAdmin `GET /v1/admin/ai/free` the free set with each model's last day per
account.

This host's part is `apps/ai/listing.go`: `aiobject.SetListingPublisher` puts each
change a sync reads on the SuperAdmin pulse (`bus.Announce`, kind `model`, key
`<vendor>/<model>`, title the change: listed, gone, listed again, repriced, turned
free, turned paid), which `GET /v1/admin/feed` streams. A sync that seeds an empty
store announces nothing; its rows are recorded all the same.

## A sibling's model call is bought for its org (`clients/aiplane.go`, `apps/ai/chat_rpc.go`, `apps/zen/plane.go`)

deps.AI and deps.Embed are the M2M client wherever the binary has an IAM identity
(`pickCompletionsClient`, `pickEmbedClient`); a static key (`CLOUD_AI_API_KEY`) serves only
where there is no identity. The M2M client crosses the plane (`OverPlane`) stating the
request's org, project and actor — completions as `ai_chat`, embeddings as `ai_embed`,
reranks as `ai_rerank` — so the model API is never reached as a key's own account. In the
`ai` process `buy` hands a zen SKU to the co-resident zen (`ai.ServeZen`, with the path),
which serves it with the stated org/project as its tenant (on the request context, never a header) —
same lanes, per-org free-lane share and failover walk as an HTTP caller. Zen's gate, plan
spend and debit stand down for a stated tenant because the sibling's meter (metered_ai.go)
already gated and debits it. Anything zen does not claim enters the model API as the
deployment with `X-User-Id` and `X-Project-Id` as attribution. Measured before the switch
(3 days of spans): in-process zen5-flash calls were 157 tok/s against an external call's
same lane; their 5 s median was a ~900-token reply, not routing.

No model call has a wall clock. The M2M client bounds only its token fetch
(`tokenTimeout`, 30 s) — `oauth2.Config.Client` would copy that Timeout onto the
client it returns, and did: every M2M completion and stream died at 30 s while the
model kept generating. A stream is cut by its idle gap (`streamIdle`, 2 min) or by
its reader: an SSE answer's failed write cancels its context, and the hangup closes
each hop's connection down to the engine.

## The model API on the MCP endpoint (`apps/ai/calls.go`)

`/v1/models`, `/v1/chat/completions` and `/v1/decisions` are hanzoai/ai's raw
handlers, so no registry entry describes them and the host's MCP server (which
offers only `x-tool` AND `x-public` operations) cannot carry them. Three typed
ops on the `ai` tool do: `aiModels` (GET `/v1/ai/mcp/models`: search the
catalog by `q`/`class`/`family`/`capability`, rows with class, family, list
price incl. `variable`, context window, capabilities), `aiChat` (POST
`/v1/ai/mcp/chat`: `{model?, prompt, system?, max_tokens?}` → `{answer, model,
receipt}`; empty model = `cloud.Model(ctx, RoleDefault, DefaultModel)`),
`aiDecide` (POST `/v1/ai/mcp/decisions`: the decisions body → `{decision,
receipt}`).

- **One path, no second biller.** Each op copies the caller's own request (every
  header, credential included) retargeted at the real route and runs it through
  this process's root router with `client.ServeHere(app, req)` — the identity
  boundary, edge gates, zen's Claim and ai's filters (auth, usage policy, wallet)
  see exactly what REST sees. Off the HTTP path (no `cloud.Request`) the op refuses
  401 rather than call as nobody. A plane caller (no bearer) is anonymous there.
- **The receipt is the response headers, read into the body** (MCP has none):
  `X-Hanzo-Served`, `X-Routed-Model`, `X-Hanzo-Fallback`, `X-Hanzo-Usage-Reason`,
  `X-Hanzo-Paid-By`, `X-Hanzo-Usage-Class`, `X-Hanzo-Usage`. `X-RateLimit-*` and
  `X-Spend-Warn` are never read; `paid_by` is absent when no payer was named.
- **A refusal is isError JSON with its code and no figure.** `{"error":{status,
  code, type, class, model, fallback, resets_at, upgrade_url, actions}}`; the
  route's sentence is dropped for 402 and 429 because those sentences can state an
  allowance (`Free tier: %d calls per day`). Other statuses keep it.
- **The address.** A typed op always publishes REST + OpenAPI + CLI + MCP; there
  is no MCP-only registration on the host. `/v1/ai/mcp/*` (beside `aiMCPTools`) is
  the agent projection — answers carry in the body what the REST routes carry in
  headers — not a second door to the three resources. Register under
  `Group("/v1/ai")` with `/mcp/...` leaves: a path literal ending in `/mcp` trips
  `manifest.TestNoSecondMCPEndpointInSource`.
- **`aiSetLimits` is withheld from MCP** by `client/surface.go`'s rule: `limit`/
  `limits` is an authority noun (a payer's consent to bill credit past the plan),
  so a mutating verb on it is refused, and an agent the plan stopped cannot grant
  itself the credit to continue. REST, CLI and SDK keep it. It removed exactly that
  one public op (o11y's `*IngestionKeyLimit` were already refused on `key`).
- **The host's hop to a child reads for `planeReadTimeout`** (`client/ask.go`
  `clientFor`), not zaphttp's 30s default: a completion outlasts 30s, and the hop
  had hung up on paid work.

## A model's capability record is asked before dispatch (`apps/ai/capability`, `apps/ai/capable.go`, #801)

A model is not a drop-in for another. `capability.Record` states, per ROUTE (the same
model through OpenRouter and through Anthropic's own key is two records, because the
account and the place data is kept are the route's): the retention it requires
(`Retention.Days`, waived by `ZeroAuthorized`), the thinking-block families it reads
and whether its blocks are bound to the account that wrote them (`Thinking.Reads`,
`AccountBound`; `capability.Reads(writer, reader)`), whether it takes disabled
thinking or a budget (`Off`, `Budget`), a forced tool choice (`Tools.Forced`), which
sampling parameters it answers 400 to (`Sampling.Rejects`), where it keeps data and
whose account it spends (`Residency`), and its list price: base, cache read, cache
write (5 m and 1 h) and fast mode (`Pricing`, USD per MTok as exact decimal strings,
the cost basis), and the plan entitlement a request to it needs (`Entitlement`:
`fable` on Fable 5.2's routes; the usage policy refuses it to a plan that does not
include it, apps/ai/limits LLM.md "Claude Fable 5.2 and fast mode need a plan
entitlement", #803). `records.go` is the table: every Claude
model api.hanzo.ai lists (`anthropic/…`, `…:batch`, `~anthropic/…-latest`) and the
first-party ids Enso's rungs name, from Anthropic's documented behavior. Fable 5.2:
30 days, no zero-retention authorization, no forced tool choice, no sampling, always
thinks, blocks read only by itself in the account that wrote them.

`capability.Gate` runs every check in one order — retention, residency, tool_choice,
sampling, thinking — and each passes, rewrites or blocks:

- **retention** blocks a route that keeps data when the caller requires zero data
  retention: 403 `model_ineligible`. **residency** blocks a route that states no
  region the caller requires (no Claude route states one): 403 `model_ineligible`.
- **tool_choice** blocks a forced choice (`required`/`any`, a named tool,
  `allowed_tools` in mode `required`) on a model that 400s it: 400
  `tool_choice_unsupported`, never rewritten to `auto`, since that changes what was
  asked.
- **sampling** drops the parameters the model 400s (the model runs its default);
  **thinking** drops `{"type":"disabled"}` on a model that always thinks and turns a
  budget into `{"type":"adaptive"}` on one that takes none.
- Thinking blocks are never stripped: the provider drops what a model cannot read,
  unbilled, and stripping them breaks signatures. A turn that moves model already
  starts a fresh reasoning context (zen `pin.go`, `apps/agents/pin.go`, #802); zen's
  switch log now says `reads=yes|no|unknown` (`ai.ReadsThinking`).

A refusal is in the caller's dialect (OpenAI's `{"error":{message,type,code,param}}`,
Anthropic's `{"type":"error","error":{type,message}}`). Every request a record
answered for logs one `ai: capability` line: door (`http`/`plane`), org, model,
record, route, verdict, `checks` (`retention=pass residency=pass tool_choice=block
sampling=rewrite thinking=pass`) and `why`.

Where it stands: `ai.Capable()` on HTTP, installed by apps/zen's `front` after
`Paced` and ahead of zen's Claim (so it holds `/v1/chat/completions`,
`/v1/responses` and `/v1/messages` whichever of zen or ai answers, the MCP door's
re-entry included), and `screen` in `buy` for a sibling's call. The policy is the
org's (`settings.Privacy`: the ZDR switches `ZDREnabled` reads, and `dataResidency`),
read once per request and only when a route asks; `X-Hanzo-ZDR: 1` adds zero
retention, `X-Hanzo-Residency` names a region when the org names none. A model with
no record (Enso tiers, Zen SKUs, every other resold model) passes untouched and logs
nothing. Enso's tiers are routers: the rung that answers is hanzo-inc/engine's
choice, and its catalog carries the per-rung rules (`unforced`, `drops`, `reasons`,
`efforts` in universe `charts/app/files/enso/catalog.yaml`); retention and
residency per rung are the engine's to add. Kai's role is untouched.

- **A frame request with an empty `{}` body is refused 400 by `planeMedia` before any default scene or model is applied (#773):** `media_rpc.go` decodes the body and runs `frameRequestError` for `/v1/frame/generations` and `/v1/frame/edits` on the media plane, matching `runFrame`.
