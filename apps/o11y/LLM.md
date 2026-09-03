# apps/o11y — embedded o11y (scoped reads + query + ingest)

cloud embeds the o11y subsystem in-process against the shared Datastore
`datastore` (cluster `insights`). Three planes, one datastore.

## An untenanted row is `anon`, and that value CHANGED

A span, log or trace carrying no tenant of its own is stamped `cloud.Anon`
(`"anon"`), declared once in `tally.go` and imported here. It used to be stamped
`"hanzo"`.

That was not a spelling problem, which is why it moved: **`hanzo` is also a real
tenant.** Fleet infrastructure telemetry and that tenant's own telemetry landed in
one bucket, and no query could tell them apart — the org label answered two
different questions with one word.

**Before this reaches a cluster**, grep the dashboards and alert rules for
`org="hanzo"`. Any panel or rule filtering on it silently changes meaning at
deploy: it stops seeing untenanted fleet rows and starts seeing only that
tenant's, which is what it should have meant all along but is not what it meant
yesterday. Rows written before the deploy keep the old value, so a query spanning
the cutover needs both.

## ONE registration, one public concept (decomplected)

The whole plane is registered as a SINGLE subsystem — one
`RegisterWithShutdown` of the name `o11y` (order 69, `mountO11y` / `shutdownO11y`,
`HealthOwner`) in `o11y.go`. `mountO11y` performs the ordered sub-mounts in-process:
`mountScope` → `mountRuntime` → `mountIngest` → `mountTraceSink`. This replaced
FIVE separately-registered subsystems
(`o11yscope` 69, `o11y-runtime` 71, `o11y-event-ingest` 68, `o11y-otlp-ingest`
72, `o11y-trace-inproc` 73) whose names leaked five public concepts (five config
toggles + five `/v1/<name>/health` routes). The k8s-style ordering was an internal
impl detail. Behavior is preserved EXACTLY: every specific `/v1/o11y/*` route still
registers inside the one order-69 mount, hence BEFORE the upstream `hanzoai/o11y`
wildcard (order 70) — so Fiber's in-order match still gives the specific routes
precedence. The upstream module ALSO registers the name `o11y` (order 70, the
wildcard); the two are co-owners of ONE concept, and this order-69 entry is
`HealthOwner` so `/v1/o11y/health` is registered exactly once (by the order-70
co-entry).

## Flat, version-less public surface (one /v1/, no nested /api/vN)

The public contract is FLAT — the upstream engine version is an internal
impl detail resolved inside the handlers, never leaked into a route:

- `/v1/o11y/{logs,metrics,status}` — tenant-scoped reads (`scope.go`).
- `/v1/o11y/availability` — platform-sudo fleet availability (`availability.go`):
  the current per-service inventory plus an up/reporting trend, read from
  `event.metric` (`hanzo_service_up`, written by `probes.go` and carried in by
  `metricspush.go`). It REPLACES the SuperAdmin VictoriaMetrics proxy that stood
  at `/v1/o11y/vm/{query,query_range}` — VM is gone, so a route named for it and
  speaking its envelope went with it. Of that proxy's 19 allowlisted PromQL
  strings only `up`/`sum(up)`/`count(up)` are still MEASURED; the other 16 lost
  their producer (node-exporter, kube-state-metrics, cAdvisor, the lux exporter's
  federation, vmalert's `ALERTS` remote-write) and `availability.go`'s header is
  the ledger of what each one would have to measure to come back.
- `/v1/o11y/{query,query_range}` — the flat builder query (`query.go`); resolves
  to the v3 engine route INTERNALLY (the version-less alias would resolve to v5,
  which 400s the v3 composite payload the console speaks), delegating to the same
  gated runtime handler the wildcard uses.
- `/v1/o11y/sessions` — the flat, org-pinned LLM-obs sessions list (`sessions.go`);
  delegates to the runtime's `/v1/o11y/llm/sessions` (traces grouped by
  `session.id` on the gen_ai span plane) and refuses an org-less caller at the
  cloud boundary. Session DETAIL is composed CLIENT-side (list + traces filtered
  by session); the runtime serves only the list, so there is deliberately no
  `/sessions/:id` route.
- `/v1/o11y/annotation-queues*` — the NATIVE human-review queues (`annotation_queues.go`
  + `annotation_store.go`); see below.
- `/v1/o11y/{services,dependency_graph,dashboards,rules,…}` — resolved by the
  upstream module's version-less alias (highest engine version wins).

## The unified endpoint: it was shut TWICE, and the locks were different

`api.hanzo.ai/v1/o11y/version` answered `403 {"status":403,"error":"no validated
principal"}` while `o11y.hanzo.ai` answered it 200. Fixing the obvious cause
turned the 403 into a 404. Two independent defects sat at one client, and the
second was invisible for as long as the first refused the request ahead of it.
Both are fixed; this section exists so the next 4xx here is diagnosed by SHAPE
instead of re-guessed.

**1. The gate exempted routes nobody served.** `gate()` kept its own list of the
paths needing no principal, and it named `/v1/o11y/api/v1/health` plus three
`/api/v2` siblings — the INTERNAL namespace the module stopped rewriting onto at
v1.5.37. Four names, zero routes: the exemption matched nothing, so every public
op was refused. The list is gone (with `isHealthPath`, `isErrorIngestPath` and
`isSentryIngestPath`); the answer is `o11y.Anonymous(method, path)`, which lives
beside the routes it describes. A copy of a route fact kept one repo away drifts
the moment the routes move.

**2. relay handed the runtime a request with no request-target.** `relay` builds
its call with `http.NewRequestWithContext` — a CLIENT request, whose
`RequestURI` is empty by design — and passes it straight to an `http.Handler`.
The EMBEDDED runtime is `adaptor.FiberApp`, which copies `RequestURI` into
fasthttp verbatim, so the path was erased, fasthttp normalized it to `/`, no API
route matched, and the request fell through to the console web provider's
`http.NotFound`. Fixed in o11y **v1.5.49** (`req.RequestURI = target`). The
out-of-process backing never showed it — a reverse proxy re-derives the target
from `URL` — so this only ever bit the embed, which is what production runs.

**READ THE SHAPE, NOT THE STATUS.** Three different bodies mean three different
hops, and they are how this gets diagnosed next time:

| body | who wrote it |
|---|---|
| `{"status":"error","msg":"…"}` | `gate()` itself — a route that FALLS THROUGH to the runtime handler (`livez`, `healthz`, `readyz`) |
| `{"status":403,"error":"…"}` | a TYPED op: `relay` re-wrapped the gate's refusal as a `zip.HTTPError` |
| `{"status":404,"error":"404 page not found"}` | the runtime's web provider — the request reached it with no path |

The three probes are the control: `mountHealth` dispatches them itself and never
calls `relay`, so "probes 200 but everything else 404" means a lost path, never
an auth problem.

**What is exempt, and why each needs no principal.** The set is
`o11y.Anonymous` — the runtime's own `OpenAccess` routes plus the two
public-dashboard reads it gates with `CheckWithoutClaims`. The rule is this
gate's purpose read backwards: it exists only because the runtime trusts
`X-Org-Id` as gateway-minted, so an op whose own gate reads NO tenant from the
request has nothing for a forged tenant to reach, and gating it can only remove
an answer.

- `GET /v1/o11y/{livez,healthz,readyz,version,health}` — the process describing
  itself. A kubelet probe and an external status check hold no principal, and a
  gate that hides whether the process is up makes an incident invisible.
- `GET /v1/o11y/global/config` — branding, ingest URL, which sign-in methods
  exist. Read to RENDER the sign-in page, so requiring a session is circular.
  Deployment-global; no tenant.
- the sign-in family (`register`, `sessions/email_password`, `sessions/context`,
  `sessions/rotate`, `DELETE sessions`, `complete/{google,oidc,saml}`,
  `reset_password_tokens/verify`, `resetPassword`, `factor_password/forgot`) —
  the path to HAVING a principal. Each still presents its own credential.
- the self-addressed reads/writes (`user/me`, `users/me`,
  `users/me/factor_password`, `service_accounts/me`) — the subject is the
  caller's own credential, resolved from the runtime's claims and never from a
  header, so there is no tenant to forge. No claims ⇒ the runtime's own 401.
- `GET /v1/o11y/public/dashboards/{id}` and `…/widgets/{idx}/query_range` — the
  tenant comes from the SHARE's scope, not from a header.
- the DSN ingest wires (`POST …/{envelope,store}` under `/v1/o11y/api/` and
  `/v1/sentinel/`) — the DSN key is the credential and the org comes from the
  project segment. The gateway waives its JWT check on exactly these, so a
  request it lets through tokenless must not be refused here for having no token.

Exemption is not authorization: every one of these still faces its own admission
test one layer in. Everything else — every read of a tenant's telemetry — stays
gated HERE and at the runtime, which is one rule enforced twice.

**`/v1/o11y` is the API, and only the API.** There is no SPA under it and there
must not be one: the module names all 367 routes precisely so an unconverted
route 404s instead of falling through a wildcard, so `/v1/o11y/` is a 404 and
every answer on the prefix is JSON. The console is `o11y-site` at
`o11y.hanzo.ai` / `obs.hanzo.ai` behind `admin-guard`, which 302s a browser to
hanzo.id PKCE and 401s a machine. One endpoint per concern.

**The tests.** `red_forge_test.go` calls `gate()` directly against a backend that
answers 200 to anything — it proves the predicate and CANNOT see the chain, which
is why it passed throughout the outage. `endpoint_test.go` drives the real
`Mount` route table against a runtime that routes: the tenant-free reads must
return the RUNTIME's bytes anonymously, tenant reads must still be refused with
the ENDPOINT's own reason (the runtime's 401 would mean the request got through), and
the four dead `/api/v1|v2` names must NOT be exempt. A fake more forgiving than
production is not a test of production.

## The o11y pin is at v1.5.67 (was BLOCKED at v1.5.34 — that blocker is gone)

> ✅ **ALL THREE FORWARDS BELOW ARE RESOLVED.** `go.mod` pins **v1.5.67**. Read
> off the composed router (381 addresses): `"/api/sessions"` is registered
> NOWHERE — the list is `/v1/o11y/llm/sessions` — and the only `/api` addresses
> are the two Sentry SDK ingest wires under `/v1/o11y/api/{project_id}/`. Both
> forwards were ORG-PINNED, so neither could be probed anonymously and neither
> showed up in the endpoint work above.
>
> `query.go` is deleted. `sessions.go` now delegates to
> `/v1/o11y/llm/sessions` NATIVELY — no adapter, request built server-shaped —
> and `sessions_test.go` drives it over BOTH backings.
>
> The second half of that defect is worth keeping in view, because it is a whole
> class: rewriting `r.URL.Path` alone redirects nothing at the in-process
> runtime. That backing is `adaptor.FiberApp`, which routes on `RequestURI`, so a
> handler that edits `URL.Path` is routed by whatever `RequestURI` still says —
> and `zip.AdaptNetHTTP` converts with `forServer=false`, which leaves
> `RequestURI` EMPTY. fasthttp normalises empty to `/`, the console route answers
> `/`, and the caller gets the SPA shell with a 200. Measured on both backings by
> mutating the fix and watching it go red.

`go.mod` USED TO PIN `github.com/hanzoai/o11y v1.5.34`. v1.5.37 renamed the module's
INTERNAL route literals (`/api/vN/<rest>` → `/v1/o11y/<rest>`) and deleted
`mount.go`'s `rewriteExternalPath`. The PUBLIC contract did not move — the client
existed precisely so `/v1/o11y/<resource>` stayed fixed while the internals
churned — so nothing in production broke, and nothing here needs "fixing" to
restore a path. What the bump does break is this package's three in-handler
forwards, which name the internal spelling:

- `sessions.go` forwarded to `/api/sessions`; at v1.5.37+ that list is
  `/v1/o11y/llm/sessions` (llmobs moved under `/llm/` to stop `sessions` and
  `users` colliding with the sign-in and IAM nouns that already own those words).
  **FIXED**, and the adapter went with it.
- `query.go` forwarded to `/api/v3/<resource>` — **this was the blocker, and it
  is now DELETED** (see "the three addresses" below). The bump landed before the
  console migration this file prescribed, so the ordering it assumed is gone:
  v1.5.37 stopped mounting the v3 builder POST route (`RegisterQueryRangeV3Routes`
  no longer registers `/query_range`; `QueryRangeV3` lost its last reference and
  `queryRangeV3` survives only as an unreferenced method), and by v1.5.57 the
  runtime registers every route at its full public path and has dropped prefix
  stripping — so it serves **no `/api/*` route at all**. The forward did not 400,
  it fell through to the runtime's terminal `/*` catch-all. A route pointing at an
  address nothing serves is dead, so it went. `/v1/o11y/query_range` is the **v5**
  querier's now, and v5 still refuses the console's v3 composite with
  `unknown field "queryType"`, so the console migration below is still owed.
- `o11y.go`'s `isHealthPath` allowlisted `/api/v{1,2}/{health,healthz,readyz,livez}`;
  v1.5.37 moved the probes to `/v1/o11y/{healthz,readyz,livez}`. **This one shipped.**
  The pin reached v1.5.46 while that list stayed, so the exemption named four
  addresses nothing served and the gate refused EVERY public op — `/version`,
  `/health`, the three probes, sign-in and the shared-dashboard reads all answered
  `403 {"status":403,"error":"no validated principal"}` at api.hanzo.ai while
  o11y.hanzo.ai served them 200. That gap is the whole reason o11y still had an endpoint
  of its own. FIXED by deleting the list: `gate()` asks `o11y.Anonymous(method, path)`,
  which lives beside the routes it describes, and `anonymous_test.go` there fails on
  any exemption that names a path Mount does not register.

`o11y.go`'s other forward, `eventToRuntimePath` (`/v1/event/<p>/envelope|store`
→ `/v1/sentinel/<p>/…`), is unaffected: both families survive the rename verbatim.
The DSN ingest wires (`/v1/o11y/api/<project>/envelope|store` and the clean
`/v1/sentinel/<project>/…`) also stay — that `/api/` segment is the Sentry SDK's own
wire format, received as-is, not our spelling of a route — and they are now
`o11y.IngestWire`, exported precisely because the gateway's JWT bypass must match
it byte-for-byte.

## The published document carries o11y's typed ops — the surface is one composed app

`plugin/o11y/openapi.json` is the build-time subset the fleet document is composed
from. It was STALE at 34 operations (including a `/v1/o11y/{wildcard1}` and three
`/api/v2` probes that v1.5.46 deleted); it now carries 381, and `make -f
mk/fleet.mk openapi` composes them into `openapi.yaml`.

It could not, for one commit-and-a-half, because `openapi.Compose` refused —
rightly:

    schema "Service" means different things in "ingress" and "o11y"

Six names collided across the fleet with DIFFERENT shapes, all six from o11y's
internal type packages: `Account` (cloudintegrationtypes), `Channel`
(alertmanagertypes), `Event` (spantypes/sentrytypes), `Host` (zeustypes),
`Service` (cloudintegrationtypes) and `TLSConfig`. The fleet's schema namespace is
flat and `openapi/compose.go` fails closed on one name with two shapes, because a
generated SDK would bind whichever it read last. And it was not only o11y's
document that was stuck: `openapi.yaml` is regenerated by ONE command for the
whole fleet, so while it refused, nobody could add or change any API surface and
prove it.

**The fix is the client, not the six names.** Renaming `Service` upstream would
have revealed the next collision, and the one after that — `Account`, `Channel`,
`Event`, `Host` and `TLSConfig` are ordinary words, six other apps use them, and
a type name that has to stay unique against every app in the fleet is a name
nobody can choose safely. So `Mount` composes instead: it builds one
`zip.App{AppName: "o11y"}`, mounts the whole surface on it — cloud's own scoped
reads and hanzoai/o11y's relay table both — and hands it to `host.Use`. Every
op then carries `Origin = "o11y"` and zip qualifies each type it reaches as
`o11y.<Type>`, unconditionally rather than on collision, so a published name is
never a function of who else is in the room. All 777 of o11y's schemas are
`o11y.*`; none of the six bare names moved, and each stays with the app that
already owned it (`Service`/`TLSConfig` → ingress, `Account` → books, `Channel` →
content, `Event` → analytics, `Host` → plugins).

This is `apps/iam`'s mechanism, unchanged — identity's 91 schemas have been
`iam.*` since it was composed, and there is exactly one way to namespace a
composed surface. The rename is a PURE one: all 381 operationIds and operation
objects, at all 287 addresses, are byte-identical once the `o11y.` prefix is undone.

Two facts composing made local, both load-bearing:

- **Nothing is left on the HOST but the child.** `zip.App.Declaration` expands
  `All` into the seven methods a host must route and leaves HEAD and OPTIONS out
  — under `All` they are indistinguishable from the shadows fiber generates — so
  an endpoint opened with `All` cannot reach the host intact. One needed that
  exception: the `/v1/sentinel` proxy, which answered OPTIONS as a real method,
  and stayed on the host for it. It is gone — the error face is twelve named
  `/v1/o11y/sentinel/*` paths with typed ops — so there is no `All` here.
- **the three shared addresses are GONE — they were told apart, not assigned.**
  `GET /v1/o11y/logs`, `GET /v1/o11y/metrics` and `POST /v1/o11y/query_range` were
  declared by both halves. While the module had a `/v1/o11y/*` catch-all, in-order
  matching hid that; once it named every route, a second declaration became a
  refusal to compose and the subsystem crash-looped. `o11y.Claimed(…)` made the
  binary boot, but a claim SUPPRESSES the module's declaration, so each of the
  three silently cost the fleet the module's real read at that address. What the
  addresses needed was to answer different questions under different names:

  | address | before | now |
  |---|---|---|
  | `GET /v1/o11y/logs` | cloud's per-product read (**no caller** — the console reaches logs through the query engine) | the module's log-record read; cloud's is deleted |
  | `GET /v1/o11y/metrics` | cloud's per-product RED window | the module's metric-NAME catalog; cloud's RED moved to `GET /v1/o11y/product/metrics` |
  | `POST /v1/o11y/query_range` | cloud's forward to the non-existent `/api/v3/query_range` | the module's v5 querier; cloud's is deleted |

  `mount()` now calls `o11y.Mount(a)` with **no `Claimed` option** — cloud takes no
  address it does not own, and the boot log says `claimed:0`. The gates are
  `TestTheThreeAddressesAreToldApartNotShared` (typed_wire_test.go: cloud answers
  at its address, the module answers at the three bare ones) and
  `TestScopedReadsOwnTheirAddressesAndOnlyTheirs` (scope_test.go: re-adding a cloud
  route at a module address is caught here rather than at boot).

STILL OWED, and it is a console change, not a cloud one: the console's trace/log
explorers still POST a **v3** composite to `/v1/o11y/query_range`, which the v5
querier refuses with `unknown field "queryType"`. They were already broken before
this change (the forward reached the runtime's `/*` catch-all, not an engine), so
deleting cloud's half regressed nothing — it turned an opaque non-answer into an
honest 400. Migrate `listQueryPayload` + `parseListRows` (hanzoai/console
`src/lib/api/apm.ts`, plus the duplicated bodies in `e2e/insights-o11y.spec.ts` and
`e2e/probe-o11y.spec.ts`) to the v5 composite
(`{schemaVersion, requestType, compositeQuery:{queries:[{type,spec}]}}`).
Restoring the v3 route upstream is the wrong direction: it resurrects a second
spelling of one noun and undoes a deliberate deletion.

The bump was rehearsed end to end before being refused — `plugin/o11y` built at
v1.5.38 with all three forwards updated, run against an upstream carrying
v1.5.38's route table and its REAL v5 request decoder. `/v1/o11y/sessions` → 200
and the llmobs reads → 200, so those two forwards are correct and ready; the
console's composite came back `400 unknown field "queryType" in composite query`
at the wire. That is the whole bump: two sites fix cleanly, one has no target,
and they ship together or not at all.

The rehearsal also answers whether `rewriteExternalPath` should survive. It is
o11y's (`mount.go`), never cloud's, so there is nothing here to delete — but it
was NOT an identity function at v1.5.34: an unknown path arrived upstream as
`/api/zzz-not-a-route`, rewritten. At v1.5.38 the same request arrives verbatim.
Internal and external spellings having converged is exactly what made it an
identity rewrite, which is why the rename deleted it in the same commit. The
only paths that still legitimately carry `/api/` are the Sentry SDK's own wire
form (above) — its protocol, not our route.

Worth knowing for any future client: cloud PROPAGATES the runtime's status. A
forward to a dead internal path surfaces as a real 404/400, not a 200-shaped
envelope wrapping one, so this class of drift fails loudly at the client.

## Typed ops: 12 of the 20 routes, and why the other 8 cannot be

Every route this package OWNS the shape of is a typed op (`zip.Get[In,Out]` and
friends), declared on the `/v1/o11y` group so the prefix is part of each op's
path — one registry entry, and the OpenAPI schema, the MCP tool, the CLI command
and the SDK method all follow from it. The prose in each handler's doc comment IS
the published description: `zipdoc` lifts it into `zipdoc_gen.go` (committed;
regenerated by `make -C apps/o11y openapi` and the Dockerfile), because Go drops
comments at compile time.

- typed: `GET /v1/o11y/{logs,metrics,status}` (scope.go) and the eight
  `/v1/o11y/annotation-queues*` routes.
- **The LLM-obs ingest path is GONE, and the open question it carried closed with
  it.** This section used to describe `POST /v1/o11y/ingestion` — a typed op that
  reached no consumer because `mountEventIngest` returned early without a
  Datastore DSN, so it was published in no document and had no SDK method, MCP
  tool or CLI command. The route became a plane op (`obs_event_claim`) and then
  went away entirely, together with `event_ingest.go` and the sink behind it.
  The reason is worth keeping, because it is the shape of the mistake: that sink
  inserted UNQUALIFIED `traces` / `observations` / `scores`, and the DSN it used
  (`O11Y_DATASTORE_DSN`) names no database, so the names resolved to `default` —
  an EMPTY database on the live datastore. Its schema said so itself
  (`⚠️ ASSUMED SCHEMA`), the datastore's query log holds no such INSERT in its
  whole retained window, and the concept was already served twice: LLM
  observability is READ off `gen_ai` spans in `event.span` by the runtime, and
  the eval product owns the grounded projections (`hanzo.eval_traces` /
  `hanzo.eval_scores`, `apps/eval/telemetry.go`, whose DDL it creates itself).
  Three claimants on one concept; the one that could never have worked is the one
  that went. `planesink.go` keeps the shared `datastoreSink` and states every
  table FULLY QUALIFIED — pinned by `TestPlaneTablesAreQualified`.
- **`cloud.Bridge()` belongs to whoever COMPOSES this app, and `Mount` installs
  none.** It goes on once at the root, ahead of every route it covers — `cloud.App`
  (`app.go`) is where, and `plugin/o11y/main.go` reaches it through the same
  constructor. A second copy at `/v1/o11y` could never run: each `Group` call makes
  a fresh node, and zip judges the node rather than the path. The org still reaches
  a typed op through every entry point, including the ones no route middleware runs
  on — `principal.ValidatedFrom` falls back to zip's own caller. Pinned by
  `TestTypedOpsSeeTheirCallerThroughEveryEndpoint` (`scope_test.go`).
- The tenant is `principal.OrgFrom(ctx)` and the status probe's weaker gate is
  `principal.ValidatedFrom(ctx)` — the two facts `cloud.Bridge` parks — NEVER an
  `In` field. Platform-sudo-ness and the project scope do need the REQUEST (they
  ride in headers neither reader carries), so they go through `typed.go` — the ONE
  `cloud.Request` client in this package, pinned in cloud's `allowedRequestUses`.

The other 8 stay untyped because typing them would MOVE the wire, which typing is
not allowed to do. **The refusals are GATED, not prose** (`typed_wire_test.go`):
`untypedByDesign` is the closed list, keyed the way the DOCUMENT writes each
address, and `TestEveryRouteIsTypedOrNamed` reads the live router of the REAL
`Mount` — so a route added anywhere in that mount is typed by default, and
dropping one out of the registry takes a deliberate edit with a reason. The stale
direction is gated too: a name for an operation o11y no longer serves is red.
`TestEveryTypedOpIsDescribed` holds the prose to the same bar (an op added without
regenerating `zipdoc_gen.go` is a nameless MCP tool), and
`TestUntypedRoutesKeepTheirWire` measures the three wire facts the reasons below
CLAIM — the `text/plain` receipt, the 200 over a body that is not JSON, and the
`text/plain` replay — so the refusals are evidence, not assertion. Prose cannot go
red; that is why this list was a promise until the gate existed.

- `POST /v1/o11y/{query,query_range}` and `GET /v1/o11y/sessions` — reverse
  proxies: request body, query string, upstream status, headers and body all ride
  through untouched. There is no Go type for "whatever the runtime answered".
- `GET /v1/o11y/alerts/last` and `POST /v1/o11y/alerts/:receiver` — `text/plain`,
  not JSON, and the POST deliberately ACCEPTS an unparseable body (it is a
  delivery receipt: a body that will not parse still proves delivery, and a 400
  would make Alertmanager retry forever). A typed op would answer JSON and reject
  the malformed body.
- `ALL /v1/sentinel/*` — a wildcard proxy; a wildcard has no operation to type.

## Annotation queues (native, relational) — `annotation_queues.go` + `annotation_store.go`

The o11y span plane (llmobs) has flat annotations but NO queue entity, so the
human-review queues the console's `AnnotationQueuesModule` consumes are a
cloud-NATIVE relational feature on the o11y surface. Storage is Hanzo Base/SQLite
(`{DataDir}/o11y_annotations.db`, opened through `cek.Open` — encrypted at rest on
a capable build), the eval-metastore discipline: `MaxOpenConns(1)`, org column on
every table + mandatory predicate on every query, `project` narrows within org via
`principal.ProjectScope`. NOT the datastore span plane (queues are durable config,
not append-only telemetry). Registered by `mountAnnotationQueues` inside the one
order-69 mount, so every route precedes the order-70 wildcard.

Surface (lists return the console REST envelope `{data:[…], meta:{page,limit,
totalItems,totalPages}}`; a cross-org id is a 404, never a cross-tenant read):

- `GET/POST /v1/o11y/annotation-queues` — list (org+project) / create.
- `GET/PATCH/DELETE /v1/o11y/annotation-queues/:id` — detail (+ pending/completed
  counts + embedded items) / update name·description·scoreConfigIds / delete (+ items).
- `GET/POST /v1/o11y/annotation-queues/:id/items` — list (status filter, paged) / add
  items. An item references a TRACE·OBSERVATION·SESSION (the console-friendly
  `traceId`/`observationId`/`sessionId` form maps to `objectType`+`objectId`).
- `PATCH /v1/o11y/annotation-queues/:id/items/:itemId` — update status (PENDING↔
  COMPLETED, stamps `completedAt`) / assignee.

`scoreConfigIds` reference eval score-configs (`/v1/evals/score-configs`), stored as
opaque bounded ids (shape-validated only). Shutdown closes the store first in
`ShutdownO11y`.

Three planes, one datastore:

- **Scoped read plane** — `scope.go` + `logs.go` + `metricsread.go` + `status.go`
  + `productmap.go` + `vmquery.go`. The org-scoped, tenant-isolated
  `/v1/o11y/{logs,metrics,status}` (order **69**, before the wildcard at 70 so
  Fiber gives it precedence). The ONE owner of these three routes — folded in from
  the retired `clients/observe` so nothing was lost:
  - **logs**: two views over ONE store. A validated SuperAdmin (`c.IsAdmin()`, ==
    `owner==admin` after SanitizeIdentity) sees the raw infra stdout stream
    (`o11y_logs`, `resources_string['app']=<workload>`); every other org sees its
    OWN request stream derived from org-tagged spans
    (`o11y_traces`, `attributes_string['hanzo.org']=<org>`).
  - **metrics**: REAL per-org RED (rate/errors/p50/p95) from org-tagged request
    spans + the org's LLM usage from `hanzo.cloud_usage`. A SuperAdmin sees the
    whole-product RED (no org predicate); usage is always the caller's own org.
  - **status**: a live in-cluster health probe (allowlisted host — SSRF boundary)
    fused with VM `up{service}` inventory.
  - **Tenant isolation**: org is `principal.Tenant(c)`, bound as a positional
    Datastore parameter (never interpolated); the product is shape-validated
    (DNS-1123) then alias-mapped (console slug → workload) then allowlisted
    (`knownServices`) — a malformed slug is a 400, an unbacked one is honest-empty.
    Reuses the shared `aiobject.DatastoreQuery` (ONE datastore client).

- **Query / control plane** — `embed.go` + `o11y.go`. Constructs the o11y
  query runtime (`community.NewServer`) and serves the rest of `/v1/o11y/*` from
  this binary (dashboards, alerts, querier) via the upstream wildcard (order 70) +
  `o11y.SetHandler` (installed by `mountRuntime` inside the one order-69 mount).
  Falls back to reverse-proxying a standalone o11y Deployment when disabled/failed.
  READS Datastore via the branded `github.com/hanzo-ds/go` **v1.0.1**. `/v1/settings/:product`
  is NOT here — it is console product config, split out to `apps/settings`.

- **Ingest / write plane** — `planesink.go`. A ZAP span receiver (:4317) and log
  receiver (:4318) decode the same wire the retired embedded collector bound, and
  a native `datastoreSink` appends prepared batches to the EVENT PLANE —
  `event.span` / `event.log`, the same 15-column envelope `event.event` /
  `event.error` / `event.metric` share. The `o11y_*` databases are closed.
  - The whole otelcol pipeline (`ingest.go` + `zapingest.go` + `spanconv.go` +
    `tracesink.go`) was DELETED, not ported: it existed to feed the SigNoz-schema
    exporters, and with the plane as the store the translation layer has no job.
  - **Bound by CAPABILITY, not a flag** — ingest runs exactly when a datastore DSN
    is configured, because the DSN is the thing it writes to. Fail-soft at every
    branch; a bad telemetry config can never take cloud down.
  - Row identity is DERIVED (a span's id is its span_id, a log line's a content
    hash salted by batch position), so ReplacingMergeTree idempotency is structural.
  - `service` is the WORKLOAD, resolved in ONE place (`planeService`) for spans and
    logs alike: `service.name`, else the wire app name, else the legacy `app` label,
    else OTel's k8s derivation (`k8s.deployment.name` → replicaset → statefulset →
    daemonset → cronjob → job → container). That last leg is not optional — the
    fleet's biggest log producer is the otel-agent's filelog receiver, which stamps
    `k8s.*` and NO `service.name`, and every reader keys on this column.

## Metrics ingest — LANDED, natively (`metrics.go`)

Metrics were deferred once because the SigNoz metrics exporter needed a **dd-sketch
fork** of ch-go that cannot coexist with the upstream driver the query plane pins.
The fork is not needed: `startNativeMetricsIngest` runs a ZAP metric receiver
(`O11Y_METRICS_ZAP_LISTEN`, `:4319`) that decodes `MsgMetricBatch` and writes via
o11y's `pkg/datastoremetrics` over UPSTREAM ch-go, into `event.series` /
`event.metric`. Classic bucket/quantile decomposition, no DDSketch, one datastore
connection shared with the query plane.

That native writer is the shape `planesink.go` copied for spans and logs — a
receiver decodes the wire, a writer appends a prepared batch. **One write path per
signal, all four the same shape, all four on the event plane.**

## cloud's own telemetry — split host / sink

The **host** owns the tracer + meter providers (`cloud/telemetry.go`,
`cloud.InstallTelemetry`, called by `cloud.Listen` before `MountAll` and by
`cmd/o11y` for its own process). This package owns the SINK.

That split is not cosmetic. The provider used to be BUILT here and handed to the
host through `cloud.RegisterTelemetryInstaller` from this package's `init()`.
That only worked while o11y was LINKED INTO the host. When o11y became a plugin
(`cloud.PluginSpec` → `zip.Load`, its own binary) the `init()` ran in the CHILD,
the host's installer stayed nil, `installTelemetry` became a genuine no-op, and
tracing went dark fleet-wide — including the `gen_ai` spans it adopted into the
ai module. The provider is a host concern (every request the host serves needs a
span) and now lives there; only the collector, the datastore exporter and the
SDK→pdata conversion (`spanconv.go`) stayed, because only those are coupled to
the `o11y_index_v3` schema. Host root graph cost of the move: **+6 packages**
(644 → 650 — the OTel trace SDK + `luxfi/trace`); `go.opentelemetry.io/collector`
and `hanzoai/ai/object` stay at 0 in the host.

The ai adoption (`aiobject.AdoptHostTracerProvider`) moved to
`cloud/apps/install.go`, beside the other `aiobject.Set*` calls, gated on
`cloud.TracerProviderInstalled()` — `apps/` already links ai; the host must not
(`hanzoai/ai/object` is 1270 packages).

## cloud's own spans → the plane (`planesink.go`)

The dogfood: cloud's OWN spans (service + ai GenAI/LLM-obs) reach `event.span`
WITHOUT a socket, via the ZAP locality-adaptive **Router** (`github.com/luxfi/zap`:
`Router`/`InProcessInterface`/`Destination`/`Payload`). One Send API, Cost-table
routing — not a caller branch. **The router lives in the host**
(`cloud/telemetry.go`); this package registers a handler on it.

- Client: `cloud.RegisterTraceSink(cloud.TraceSink)` where
  `TraceSink = func(ctx, []sdktrace.ReadOnlySpan) error`. `mountPlaneIngest`
  registers it; `shutdownPlaneIngest` registers nil. The payload is SDK spans —
  and now it stays SDK spans: `sdkSpanRowsOf` renders them straight to
  `event.span` rows, one converter fewer than the retired pdata detour, sharing
  every helper (`planeOrg`, `planeService`, `planeSpanKind`, `planeStatus`) with
  the wire path.
- **Same code, both deployments — but the wire needs an ADDRESS.** Registration is
  process-local. Linked in, the host's `Send` to `traceDest` ("hanzo.o11y.traces")
  finds this handler and hands over the LIVE batch by value (zero serialize, zero
  socket). As a plugin, the handler is registered in the CHILD, the host's router
  returns `ErrNoRoute`, and the identical `Send` falls through to the ZAP wire.
  - Cloud runs its ~20 subsystems as sibling plugin PROCESSES in one pod, so
    "co-resident" means loopback for all but one of them. `wireEndpointFor`
    (host, `telemetry.go`) resolves that: explicit `OTEL_EXPORTER_ZAP_ENDPOINT`,
    else the fleet collector when a legacy OTLP endpoint declares remote intent,
    else `127.0.0.1:4317` — `planeSpanListen` — whenever
    `O11Y_TRACES_ZAP_INPROCESS` says a sink exists in this DEPLOYMENT. Without
    that last leg the siblings had neither a route nor an endpoint and every span
    they produced was dropped, which is precisely how `event.span` stayed empty
    while five migrated read paths queried it. The process HOLDING the sink is
    routed and never reaches the fallback, so it cannot self-dial.
- **Capability-bound + fail-soft.** `mountPlaneIngest` (in the one order-69 mount)
  starts when a datastore DSN is set; the in-process sink additionally honours
  `cloud.TraceInprocEnabled()` — the ONE gate, read by both the sink (whether to
  register) and the host producer (whether a wire address is even needed). Any
  construction error leaves cloud's spans on the wire; activating it can never
  take cloud down. Shutdown deregisters the handler, stops the listeners, closes
  the connection.
- Boot window: the provider installs before `MountAll` but the sink registers at
  mount (order 69); spans in between take the wire fallback, else surface
  `ErrNoRoute` (visible, never a silent drop).
- Covered by `planesink_test.go` (the pure row builders — wire span, wire log, SDK
  span, the k8s workload derivation and the filelog regression it closes, with a
  column-count guard pinning each row to its column list) and the routing /
  Cost-table / `wireEndpointFor` tests in `cloud/telemetry_test.go`.
