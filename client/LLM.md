# LLM.md — client

Guidance for client. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The mechanism is `sdk/plane`; this package is the contract

`Call`, `For`, `Peer`, `Reach`, `ErrNoPeer`, `Addr`, the plane transport and its read
ceiling, and the router's wake handshake (`HostApp`, `HostStart`, `StartIn`, `Started`)
live in `sdk/plane` (public module `github.com/hanzoai/cloud/plane`): zip and the
standard library only, no DTO, no op name. This package keeps the DTOs, the op-name
constants, the generated per-app clients (`client/<app>`, which call `plane.Call`) and
the host-side fan-out (`ask.go`, the MCP surface, GraphQL), which read `manifest` and
`openapi`. `go generate ./client` emits `zap_gen.go` beside each package that declares a
wire (here none; `sdk/plane` for the wake pair).

## Cross-subsystem clients that are values, not places

      bal, err := cloud.Ask[plane.BalanceIn, plane.Balance](
          cloud.As(c, org), "commerce", plane.FinanceBalance, &in)

  `As(c, org)` delegates THIS request's principal, optionally re-pointed at another
  tenant — the operator acting on someone else's books. `For(ctx, org)` states the
  tenant when there is no request to delegate from, the background-loop case. A
  missing socket is an ERROR naming the app — "that app is not running here" — never
  a zero value, which is exactly the lie the imports told. Serving side:
  `zip.Post[In,Out](cloud.Plane(), path, fn, zip.WithOperationID(plane.X))` from the
  app's Mount, `cloud.Who(ctx)` for the principal, and an ordinary `zip` error for a
  refusal the caller sees with its status intact (402 vs 403 vs 404 vs 503).
- **One socket path, named once, and it is zip's.** `zip.SocketPath(app)` →
  `{ZIP_RUNTIME_DIR}/<app>.sock`, the SAME function `ServePlane` binds and `Ask`
  resolves, so a server and its callers cannot disagree about where an app lives.
  Cloud points `ZIP_RUNTIME_DIR` at `{CLOUD_RUN_DIR | CLOUD_DATA_DIR/run |
  /run/hanzo}` once, at `bindRuntimeDir`. Directory 0700, socket 0600: the
  filesystem is the reachable surface and the kernel enforces who may connect,
  which is why a forwarded principal is sound here and would not be over a network.
- **The org rides the CALLER, never the argument.** Every org-scoped op takes its
  tenant from `cloud.Who(ctx).Org` and refuses an empty one. Do not add an org field
  to an input — a caller that can name its own org can name another tenant's books.
  Prefer making it UNREPRESENTABLE over validating it away: `plane.BalanceIn` carries
  `(subject, currency)` and has no org field at all, so there is no check to forget.
  `plane_test.go` proves this adversarially against the real transport AND
  structurally (`TestNoPlaneInputCanNameAnOrg` walks every input type by reflection),
  and the property is live — an org-less call answers `403: … no org on the call`.
- **Wire contracts live in `plane/`, imported by BOTH ends.** Typed In/Out plus the
  op names; the encoding is zip's, not ours. Putting the request/reply structs in
  the OWNING app instead is what made `apps/billing` import `apps/commerce` to name
  two structs — 1246 packages for a DTO. One definition, neither end importing the
  other. Measured: `plugin/admin` 2261 → 1115 packages, `apps/billing` 1246 → 945,
  and two boards that had been reporting zeros started reporting the truth.
- **THE FLEET HAS ONE MCP ENDPOINT: `POST /v1/mcp`, on the HOST, AND IT IS A QUERY.**
  The host serves it itself (`fleet.Mount` in `cmd/cloud`; zip's own endpoint is
  `Disabled` there so exactly one handler holds the address). A `tools/list`
  forwards the CALLER's own message to every composed subsystem in parallel and
  unions the replies — so what the endpoint carries is what the subsystems serve at
  that instant, and a subsystem whose tools depend on the tenant answers for THIS
  caller out of its own rows.

  Identity differs per hop and neither re-derives it. The MCP hop has no request
  behind its context, so the caller is read once with `CallerOf` and stated with
  `WithCaller` — a STATED caller is what `CallerOf` answers where no request
  stands. The graph's hop (`execute.go send`) carries an arbitrary REST address
  rather than a typed op, so there is no `zip.Here` to reach for: it replays the
  caller's own request against the serving app's router (`a.Fiber().Handler()`),
  and the headers ride along untouched. `zip.Serving` is what makes that the SERVED
  router rather than `App.Test`'s, which prepares an app of its own and can answer
  for routes the served one lacks.
- **The per-tenant tool plane is ONE typed op, and callable in-process.** An org's
  connectors, functions, agents, authored skills and external MCP servers are ROWS,
  not code, so no build-time catalogue can hold them: they are reached through
  `POST /v1/tools/call` (a typed op, therefore itself a tool on the endpoint) over the
  registry's one policy — precedence → activation → price → dispatch. Discovery is
  `GET /v1/tools`. `apps/auto` contributes to it through
  `connectorToolProvider`, whose `dispatchTool` is the ONE core (resolve
  `<connector>_<action>` → run with a Token bound to the VALIDATED org); the
  exported `automations.InvokeTool(ctx, org, tool, args)` is the same core for a
  sibling subsystem that must ACT AS a caller (the Business AI guide's "do it for
  me") — same 403 gate, per-org concurrency bound, one metered unit, one audit
  record. Use these clients; never re-implement tool dispatch.

  (1) A bot RUN — a task the executor performs on a surface — is `apps/bot` at
  `/v1/bots` (3 ops: a 501 launch, list, stop). (2) A bot MACHINE — a
  visor-provisioned kind=bot machine plus its agent binding — is `apps/visor` at
  `/v1/visor/compute/bots` (apps/visor/bots.go); what it rents you is compute, so it
  nests in visor's domain. (3) The executor's own OPS FACE — the TS bot
  (channels/skills), never reimplemented in Go — is `apps/bot` at `/v1/bot/*`
  (apps/bot/relay.go), a verbatim relay carrying base address, identity, framing
  and cleartext policy. (4) A bot NODE — one of the org's OWN machines that
  dialled in and holds a socket — is `apps/bot` at `/v1/bot/connect`,
  `/v1/bot/nodes`, `/v1/bot/peer/invoke` (apps/bot/subsystem.go).

  **The name `runtime` is free at the app level and belongs to `apps/sandbox`** —
  `apps/sandbox/runtime.go` already uses it for what a sandbox is MADE OF
  (`runtimes` maps runc / gvisor / kata-clh / kata-fc to `{kernel, shares}`;
  `runtimeFor` derives the isolation). A sandbox is an INSTANCE of a runtime.
  What survives of the old squat is package-local to `apps/bot`: the `Runtime`
  interface (imported by nothing outside it), the `state.runtime` field and ~10
  `"runtime: …"` error prefixes in transport.go. Renaming it moves no wire and no
  operationId, but its PROSE reaches `zipdoc_gen.go` and therefore the published
  document, every SDK docstring and every MCP tool description — so regenerate
  (`make -C apps/bot describe`), never hand-edit.

  **TWO FOLDS THAT LOOK AVAILABLE AND ARE NOT.** A bot MACHINE is not a sandbox:
  `apps/sandbox` is a gVisor pod addressed by pod name through the apiserver, with
  no `os/exec`, no pool and a lease; a bot machine is a rented provider droplet
  with a public IP, cloud-init-bootstrapped, carrying `hanzo-kind:bot` and a
  size/region. Folding (2) into the sandbox primitive deletes a PRODUCT, not a
  duplicate meaning. Nor are (3) and (4) one value — a relay to somebody else's
  service is not a registry of the org's own connected machines.
- **vm speaks TWO wires, and a call site says which — `cl.call` or `cl.op`.**
  Visor (`hanzoai/visor`) is converting its routes to typed zip ops noun by noun,
  and a typed op has no envelope: the answer IS the value, the status IS the
  outcome. Everything not yet converted still answers the legacy
  `{status,msg,data}` inside an HTTP 200, where a logical failure is a 200.
  `apps/visor/client.go` reads both, and the choice is per call site because the
  two CANNOT be told apart by looking — feeding a typed answer to `cl.call` reads
  an `AgentBinding`'s own `status:"Pending"` as an envelope status, decides the
  upstream failed, and answers 502. Converted so far: a machine's AGENT
  (`GET /v1/machines/agents`, `PUT|GET|DELETE /v1/machines/:id/agent` — one
  address, the method carrying the verb, the SAME shape cloud publishes one
  segment down at `/v1/visor/machines/…`, so there is no translation left to
  keep in step). `call` shrinks to zero as the
  migration finishes and goes with the last noun. Converting a visor route is a
  WIRE BREAK and lands with its cloud caller in the same change; visor's `LLM.md`
  ("Typed ops") is the other half of this note.
- **Cloud owns policy; the runtime owns the run. Do not copy state you do not
  own.** `apps/bot` holds NO store. The sandbox lives in the bot runtime,
  keyed in the runtime's own tenant store, which is the only thing that knows
  whether a run is alive — so list and stop PROXY it, gated by cloud's
  principal/org. A cloud-side registry was tried and was wrong: it minted an id
  the runtime had never heard of, so it listed runs that did not exist and
  "stopped" runs that were never started. Isolation holds because the org is the
  validated one cloud sends, never a client's, and the runtime keys every run
  under `tenants/{org}/`.
- **An app declares storage** (`storageGb` on the platform Application): the
  deploy ensures an RWO claim `<slug>-data`, mounts it at `/data`, and forces
  `strategy: Recreate` (an RWO volume + rolling update deadlocks on
  Multi-Attach). The claim is never patched or deleted with the app. Stateful
  binaries should keep their store in a SUBDIRECTORY (`/data/<name>`) — the
  volume root carries ext4 `lost+found`, which version-sniffing stores reject.
- **A KMS write's `path` never names an org** (`namesAnOrg`, apps/kms use.go).
  The root is already `/orgs/<org>` from the credential, so `--path /orgs/hanzo`
  acting in org admin was stored at `/orgs/admin/orgs/hanzo`; it is refused 400
  with that address. Reads, deletes and listings still reach such a record.
- **A typed op on a greedy tail is public** (openapi/public.go `audience`): only
  an UNTYPED `{wildcardN}` is a relay. That put `GET`/`DELETE
  /v1/kms/secrets/{wildcard1}` (and s3 objects, space files, sbom) into
  openapi.yaml, so the CLI generates `kms secrets` read and remove. The MCP still
  withholds every KMS secret op by name (client/surface.go TheRule).
- **No KMS URL names an org.** The tenant surface is `/v1/kms/secrets`; the org
  comes from the validated principal (for a switched-in SuperAdmin, X-Org-Id —
  the same one-predicate switch every subsystem honors). Cross-org over HTTP
  does not exist; in-process callers (which hold the master key) are the only
  cross-org readers. The org remains the STORE partition (`/orgs/<org>/…`).
- **Per-tenant KMS identity is minted, not runbooked.** On a missing
  `orgs/<org>/kms-auth/*` credential and with `IAM_SERVICE_TOKEN` set,
  `apps/platform` calls IAM's idempotent bootstrap upsert to create
  `<org>-platform-kms` (clientId==name==audience; a surprise clientId is
  refused, never sealed; the upsert must carry `cert-<brand>` or the minted app
  cannot SIGN and its tokens 500), seals both fields, and the sync proceeds.
  In-cluster IAM base resolution is `cloud.IAMBaseURL` — the split-horizon
  policy stated once (Cloudflare 403s server-side POSTs to the public issuer).
- **A customer IS an IAM user; marketing keeps no contact list.** Who to email is
  read IN-PROCESS from the embedded IAM (`apps/marketing/roster.go` →
  `iam/pkg/store.GetMailableUsers` over `apps/iam.DB()`), the same client
  `apps/platform` uses for the IAM-owned Project — no HTTP hop to `/v1/iam`
  from inside the binary, IAM's `model.User` verbatim, read-only, masked. The org
  is `principal.Org`, which IS IAM's `Owner`, so an audience can only ever resolve
  its own tenant; `GetMailableUsers` REFUSES an empty org rather than falling back
  to the all-orgs view that `GetProjects` deliberately allows. IAM not co-mounted
  is a 503, never an empty audience — a send reported successful to nobody is the
  worse failure. An audience with no event filter is every mailable customer;
  with one, `matchCohort` joins the warehouse `distinct_id`s to that roster and
  COUNTS what matched nobody instead of inventing an address.
- **A product announcement is not its own engine.** It is a one-step sequence with
  an audience enrolled into it — `POST /v1/marketing/sequences/:id/enroll` takes
  `audienceId` where it takes `address` — so it inherits the drip engine's
  claimed-once delivery, the ONE send gate (`state.deliver` → suppression →
  `notify.Send`), and the signed unsubscribe footer. Never add a blast path beside
  `deliver`: the gate is only absolute because it is the only endpoint.
- **Absence is only meaningful from a callee that could have said otherwise.**
  `runtime.ErrNotFound` (the operation ANSWERED "no such target") is separate from
  `runtime.ErrNotServed` (the operation does not exist). Conflating them makes a
  stop that cannot fail: a runtime without the route reports absent for EVERY run,
  so "already gone" becomes permanently true. A bare 404 is 502, never success.
- **The Business AI Guide (`apps/guide`, `/v1/guide/*`)** is the on-site launch
  checklist: a pure engine (`curriculum.go` — parse/validate/next-step/dependency
  gating over plain data) + per-org progress (`cloud.OrgStore[*Store]`) + an
  injectable auto-detect registry (`detect.go` — `acted` reads the agent action
  ledger, `analytics` probes the shared warehouse) + the agent (`agent.go` — drafts
  with `deps.AI`, executes the step's bound tool via `automations.InvokeTool`). The
  curriculum is a machine-readable contract (embedded `default.yaml`; org-custom via
  PUT replaces it) so `hanzoai/marketing` can author the full `checklist.yaml`
  against the same `Step`/`Curriculum` shape.
- **The EXPERIMENT is a composition, not a fourth engine (`apps/experiments`,
  `/v1/experiments`).** A/B testing is ONE value whatever the variant KIND (feature
  flag, ad creative, email subject, model id): the primitive owns only the
  experiment registry (definition + decision); it COMPOSES three planes it never
  duplicates. ASSIGNMENT = `flags.Assign(org,project,key,subject,props)` —
  subject→variant is a deterministic `engineEvaluate` (sha1 rollout hash), no second
  bucketing, no assignment store; create writes a multivariate flag def
  (`flags.PutDef`) and decide rewrites its weights to 100% for the winner
  (`flags.GetDef`+`PutDef`). MEASUREMENT = `analytics.Outcomes(...)` — one
  org-scoped `hanzo.events` query (the `eventsWhere` isolation invariant), no second
  event store; the analyze fold joins each subject's analytics outcome to its flags
  variant by `distinct_id`. EVIDENCE = `research.Record`/`research.List` — per-variant
  samples land as immutable `kind:"ab"` rows; significance (two-proportion z-test,
  `math.Erfc`, no dep) is a PURE function over them. `apps/campaign` runs a
  creative A/B by composing `experiments.Assign`/`experiments.Analyze` — it never
  reinvents assignment or evidence. Add a new variant KIND by putting a payload on
  the variant; the primitive does not care what it is.
- **The OSS-template compute cost is DERIVED from the compose, not a fourth ledger
  (`apps/blueprint`, `/v1/blueprint`).** A blueprint's `docker-compose.yml` is
  parsed to its SBOM (the bill of container images) and its services' CPU/memory
  footprint priced through ONE documented rate card (microdollars per vCPU-/GB-hour,
  DigitalOcean-droplet-derived + platform margin; tunable via
  `CLOUD_BLUEPRINT_UCPU_HR`/`_UGB_HR`). Sizing is the declared
  `deploy.resources.reservations`/`limits` (or legacy `cpus`/`mem_*`) else a default
  footprint per inferred class (db/cache/web/worker/other). `blueprint.EstimateTemplate(id)`
  returns `{sbom, vcpuHr, gbHr, microUsdPerHour, estCentsPerMonth}`: `estCentsPerMonth`
  is the "~$X/mo to run" the console shows; `microUsdPerHour` is the exact rate the
  deploy path meters the deploying org on via the SAME commerce spine `resource_billing`
  uses. The author royalty (`apps/authors`, `defaultShareBps=2000`) already accrues
  20% of a deploying org's metered spend — this plane only DEFINES the compute component
  of that spend from a real rate card; it never touches the ledger or the accrual sweep.
  Distinct from `apps/sbom` (CycloneDX packages INSIDE one image, keyed by digest);
  this is the bill of IMAGES a stack runs, keyed by template.

## After the split: a store has one owner, and everyone else asks

`cmd/cloud` is a light plugin HOST. Each app is its own binary (`plugin/<app>/main.go`
→ `cloud.Listen`), started lazily on the first request to its prefix, and a child's
`--enable` contains ONLY its own name. So `cfg.Enabled("commerce")` is false in every
process but commerce, `finance.Current()` is nil in every process but commerce, and
`iamclient.DB()` is nil in every process but iam.

`make build` builds the HOST ALONE. Use `make apps` (112 parallel targets; 53s cold,
4.3s warm) or `make ship`. With only the host present every app route answers 503,
which looks exactly like a broken product and is not one.

**THE LAW.** A store has one owner. Any other process ASKS it — it never opens it, and
it never reports "not configured" for something that is one socket away.

See "Inter-app calls" above for the mechanism. The rule is the same whatever the
transport: one owner, everyone else asks.

**Six subsystems broke this way**, each silently, each fixed by publishing a method
from the owner: the prepaid gate (which ALLOWED — every priced act became free), the
balance read (501 on funded accounts), the identity roster (campaigns mailed nobody),
secrets (a stored provider read back as "not configured"), the welcome grant (an
org opened broke, and the paywall then refused it correctly for a reason nobody chose),
and the ADMIN CREDIT GRANT — the last one, and the only WRITE among them.
credits/usage/ledger are three projections of ONE entry list and went together.

Four things are worth carrying off it:

- **A read and its write are ONE conversion.** Converting the read alone leaves a
  surface that can be looked at and not used, and it reads as working — nobody notices a
  write nobody is making until an operator makes one. When you convert a plane read,
  convert its write in the same change or write down why not.
- **The test fixture was the reason nobody saw it.** `newCockpitFakes` handed admin a
  reachable `httptest` commerce URL, so `commerce.Client.Ready()` was TRUE in every test
  and FALSE in every production process. The suite was green on an arrangement production
  has never had. It serves the two finance ops on the REAL plane now
  (`cockpitFakes.servePlaneBooks`), and `apps/admin/core/grant_plane_test.go` drives
  `grantDeposit` with no co-resident ledger — which is production's arrangement, not a
  degraded one.

Three rules fell out of doing it, and they are worth reusing:

- **Peer ABSENT ⇒ fall back, or stay inert. Peer ANSWERED badly ⇒ error.** A missing
  peer is the legitimate split-deploy (or no-money-plane) shape, and erroring there
  502s a deployment that is working as designed. A corrupt reply rendered as zero is a
  funded account shown as broke.
  **Unless there is nowhere to fall back TO, and a WRITE usually has nowhere.** The rule
  is about a caller with a second way to get the answer; the admin grant has none — a
  deployment that runs no commerce cannot credit anybody — so `ErrNoPeer` FAILS the grant
  rather than being read as absence. Read literally, the rule would have turned the one
  honest error on that path into a silent success, which is worse than the defect it
  replaced. Ask what the fall-back IS before taking it.
- **The billed or read ORG rides the CALLER, never the argument.** A caller that could
  name the org in a body could bill or read another tenant. `TestNoPlaneInputCanNameAnOrg`
  pins it structurally: no input type on the plane may carry an Org or Owner field.
- **Send DATA, not a rendered view.** usage and txns carry ROWS; the HTTP surface
  renders its own envelope. Sending the envelope would require the renderer to live
  with the ledger, which is an import cycle (billing already imports commerce) — the
  compiler tells you.

**Wire contracts live in `plane/`** — one leaf package imported by both ends, holding
every op's In/Out and the op names, so the halves cannot drift and neither drags the
other's dependency graph. It imports nothing of cloud's. Money crosses as `plane.Money`
(exact decimal text + currency code), never as an int.

**Money is not an int.** `hanzoai/money` is the general exact value; `apps/money` is
USD at 18 decimals so an off-chain amount and an on-chain uint256 are the same integer.
The ledger speaks the latter, so the wire carries `AttoString()` ↔ `ParseInt()` — exact
by construction. Flatten to cents only at a boundary that is already cents-shaped.
Alignment is NOT finished: ~46 `money.Amount` against ~308 `int64` cents.

**One deployment, one key.** The data-plane master arrives in the environment and
every child inherits it (master.go), so the fleet cannot end up running on two
keys with nothing saying so. There is no broker to start first and no token to
wait for — that was credz, and it is dead (see the credentials section).

**THE PLANE PROVES NOTHING ABOUT WHO IS CALLING, and that is the fact to carry.**
The caller is nine HEADERS — `zip.CallerOf` reads X-Org-Id, X-User-Id,
X-User-IsAdmin and the rest (zip caller.go). Nothing signs them and nothing
verifies them: the callee believes the sender, so any co-located app can state
`For("another-tenant")` and be believed.

The socket is the boundary for "one of our own processes" — 0600, and
`SO_PEERCRED` is kernel-authenticated for uid — and it is NOT a boundary between
our own processes. That is fine for a bug-free fleet and is worth knowing before
treating a forwarded identity as an authorization decision.

Tenancy is enforced where a request principal is RESOLVED: at the edge, from a
validated token. A method that re-checks the stated org against a ref (as kms
does) catches an app asking for one tenant while acting for another — a BUG worth
failing on, rather than an attack being repelled.

**ZAP capabilities exist, and none of this uses them.** `luxfi/zap@v1.2.7/dexsession`
is a Cap'n-Proto-shaped capability system — `QuoteCap`/`IntentCap`/`SettlementCap`/
`AdminCap` derived by ASKING the session, backed by an unforgeable-token grant table,
over a surface that is value-free by construction so `adminWithdraw` cannot be named
at all. It is written, it is next endpoint, and **cloud imports `dexsession` nowhere**.
Do not read "we run ZAP" as "we hold capabilities": the plane's transport is ZAP and
its authority model is a forwarded string.

## A pointer to zero crosses the plane as present

zapenc carries `*T` for a scalar, string or `[]byte` as `list<T>` of at most
one element (zip v1.37.31+; v1.37.31 and .32 are retracted): nil is absent, `&false`/`&0`/`&""` arrive as
themselves. Before, they arrived nil: `AlertSpec.Enforce=&false` made a hard
cap, `PATCH enforce:false` did nothing, `periodSpentCents:0` read as unknown.
Plane fields this moves: `AlertSpec.Enforce`, every `AlertPatch` field,
`Alert.{PeriodSpentCents,Over,Warn}`, `GitHubIssueIn.Assignee` (`""` now
unassigns, as documented), `Payout.FailureMessage` (`""` now reaches the JSON
when a failure code is set, as documented). Every sender passes these through
from a decoded JSON body, so none relied on the drop. A pointer to an all-zero
struct now arrives non-nil too. `apps/commerce/cap_wire_test.go` drives the cap
handlers over a real socket; `plane.Call` would short-circuit in-process
(`zip.Serving`) and never encode.
