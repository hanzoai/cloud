# LLM.md — apps/gateway

Guidance for apps/gateway. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Lifecycle defense: ONE scorer client, ONE fail policy, a sensor at the edge

`risk.go` · `agency.go` · `middleware_abuse.go` · `apps/gateway/edge/traffic.go`

**`cloud.Decide` is the only endpoint to `/v1/risk`.** The app that owns `/v1/risk`
hands its scoring function to the core with `cloud.SetRiskScorer` — the same
inversion `SetObsEventIngest` uses, because package `cloud` cannot import an app.
Nothing in cloud scores; a second scorer would be a second answer to one question
and the two would disagree silently.

**The fail policy is `riskUnavailable`, and it is the only copy.** A scorer that
is absent, erroring, silent, out-of-vocabulary, panicking or past `RiskBudget`
(150ms, enforced by the caller) ALLOWS an ordinary request and BLOCKS a
`Privileged` one. Every answer carries a `Refusal`, so an allow-because-nobody-
was-listening is never recorded as clean. `Privileged(method, path)` is the
data list of grants: credential minting/revocation, identity provisioning,
sign-up, onboarding, the key store (all methods), and admin/org MUTATIONS.

**`AbuseGate` sits `AuditTrail → ScopeRateLimit → AbuseGate → StarterGrant →
BillingGate`.** It keys on the CREDENTIAL, which neither existing limiter can see
— EdgeRateLimit keys on IP pre-auth, ScopeRateLimit on (org, project, service) —
so a stolen key inside its org's normal ceiling is invisible to both. It counts,
classifies, asks and enforces; it never scores. A non-allow verdict is HELD for a
minute so an attack costs one screen, not one per request. A refusal is a 401/403,
which AuditTrail already puts in the tamper-evident trail — there is no second
audit write, because one event must not have two records.

**SHADOW PER ORG BY DEFAULT** (`edge.Policy.Mode`, the one new knob). Shadow
senses and records and enforces nothing. `PUT /v1/gateway/config {"mode":"live"}`
is REFUSED while no scorer is installed: fail-closed on a component that was never
wired is an outage, not a defense, and this is what makes the distinction real.
Only live mode reaches the scorer, meters a screen, or fails closed.

Mode is the one field on that route whose SCOPE and whose AUTHORITY are different
questions, and both were wrong once:

- **It does not inherit.** Every other per-org field layers a platform default
  under the org's own value. Mode is not a default, it is an arming decision, so
  seeding it from the platform row meant the single PUT that arms the anonymous
  lane (the reserved admin org — the only way to arm the lane that has no tenant)
  silently armed every tenant in the estate. `Store.Mode` reads the org's OWN row
  and nothing else; the platform row governs exactly one scope, the empty org.
- **It is not self-service.** It lives on a tenant's row but writing it requires
  SuperAdmin, whichever row it lands on. The subject of an abuse control does not
  get to switch the control off — and an org-admin credential is what a stolen key
  buys, so leaving it in the self-service branch made the gate disarmable by the
  account it was watching.

**Agency — the differentiator.** `agency(class, pattern)` is pure and total, and
reads OUR issuance, never the client's self-description (there is no user-agent
heuristic and there must not be one) and never a REQUEST HEADER. "We minted this
credential" is a fact only the identity boundary can state, so the boundary states
it: `SanitizeIdentity` parks the principal it resolved with `principal.Mint`, a
request-local slot no client can write, and the classifier reads `principal.Minted`.
Reading `c.Org()`/`c.User()` instead was forgeable twice over — X-Org-Id survives
the boundary on the anonymous path by design, and a hand-written plugin process may
have no boundary in front of it at all — so two headers plus an `sk-`-shaped string
that never validated bought the agent lane. Absent attestation resolves to
anonymous, which is the fail-closed direction: only a credential we resolved buys
the lane. An attributable machine credential is the
`agent` lane whatever it claims to be; a browser session is `human`; an
unattributable caller is `unknown` until it shows an abuse SHAPE — many
credentials from one address, a wall of refusals, a path sweep — and only then
`bot`. Anonymous is not malicious.

**The sensor** (`edge.Traffic`, a LEAF package so the middleware and the app share
ONE object) counts requests, 401/403 failures, path spread and peer spread per
(org, credential) over a rolling minute. Bounded by construction: a fixed ring
plus two 64-bit population-count words per key. Credentials appear only as a keyed
per-process fingerprint — never a bare digest, so a published fingerprint cannot be
tested against a candidate key off-box.

**TENANCY IS THE DATA STRUCTURE AND THE BOUND IS PER TENANT.** One org's callers,
hosts and lane counters live in that org's OWN tables, reached only by indexing
`tenants[org]`; there is no shared map with org-prefixed keys, so a cross-tenant
read or eviction is unwritable rather than merely refused. Each tenant has its own
ceiling and reclaims only its own keys — a process-wide cap over a shared table is
a cross-tenant denial of service, because the org that fills it evicts whoever was
quietest and that victim's controls then go silent with no error. A tenant at its
ceiling degrades exactly one tenant, itself, and says so: `TrafficView.Saturated`
is its own count of its own reclaims. The one reclaim policy lives in `table[V]`
(unexported map, cap as a constructor argument, a pinned live verdict is never
dropped) so the wrong shape is unrepresentable rather than discouraged.

- **A preflight is anonymous** whatever it carries (`preflight`): CORS answers it
  before identity, so a `?api_key=` on it buys nothing.
- **Strikes** (`strike`): a strike means the address presented a credential that
  was CHECKED and found to be nobody — a key IAM refused by name (a `key_*` reason,
  `keyRefused`) or a token that does not verify — on each bucket of its address. It
  is never an expired session (it verified and ran out), a publishable key, a key
  on `Api-Key`/`?api_key=` (the edge counts those as anonymous: identity never makes
  a principal of them), or an IAM answer with no `key_*` reason (a throttle, a
  refusal of cloud's own credential), which is also never cached as the key's
  answer. A bucket that spends its limit of strikes (the per-IP limit, 16× for a
  /48) is struck until its window resets, and while it is, the edge refuses only a
  credential the boundary cannot tell is somebody without asking: it asks
  `identityValidator.glance` (the same boundary instance, built once in `App` and
  handed to both), which verifies a JWT locally (memoized for the boundary) and
  reads a key's cached answer. A signed-in JWT user and a key whose answer is
  cached are served from a struck address; an uncached key and a forged token are
  refused before CORS and identity. Junk costs IAM at most the per-IP limit per
  address per window, and 16 limits per /48 instead of 65,536.
- **An API key its OWN rate refused** is barred at the edge under its fingerprint
  alone until that window resets (`rateBarredLocal`) — never for an org-level
  ceiling, which does not bind the billing surface, and never for a transcript push
  the key opened (`continues`), which no ceiling counts, the key's own included.
  The bar lifts early once the boundary's cached answer for the key carries a
  higher rate; a member's key (never cached) waits out the window. This is what
  bounds a member's key, which IAM is asked about on every request by design
  (membership removal must end it on the next one). The limiter's own memo
  (`rl.over`) likewise holds a refusal only while the limit it was counted at
  still stands.
The key cache (`iamKeys`) files entries under `keyID` (SHA-256, never the key),
keeps IAM's refusals (`refused`, kept 60s like `cache`; `newIAMKeys` sets both, and
`TestTheProductionResolverRemembersRefusals` builds it that way) apart from its
principals (`cache`) so a flood of
junk keys evicts only junk, bounds each at `cacheCap` with lapsed entries swept on
insert (the shape `maxProvenCache` keeps), collapses concurrent lookups of one key
into one IAM question (`asking`, a stdlib in-flight map, detached from the first caller's
context), and never remembers a lookup IAM did not answer (transport error, 5xx,
unreadable body) as a minute of "nobody".

**Rate counts are the deployment's, not the process's.** Every ceiling a request
binds — its key's `rate`, its org's rule — is a one-minute window in the named
`cloud.Window` "rate", charged in ONE call (`Window.Ceiling` → `coord.Ceiling`):
Quota's all-or-nothing script, so a request one ceiling refuses spends nothing of
the others, and each window's reset comes back as the KV's own PTTL (Retry-After
never compares two clocks). Windows are named `<class>/<escaped parts>` —
`key/org/id`, `rule/org/project/service`, `org/org`, `floor/org/service` — so no
scope spells another's. The old per-process map made an org's ceiling N× its
replicas and apps. A refusal is remembered in the process until its window resets
(`rl.over`), so a refused flood never asks the KV again. A KV that cannot be asked
fails OPEN: each pod admits its share of a ceiling rounded UP (`coord.Ceiling`;
`coord.Quota`, money, rounds down), and `coord.Spend`'s fallback now takes the same
rounded-up share instead of the whole limit per pod. The edge's per-IP windows stay
unnamed on purpose — a pod's flood guard, no round trip on public traffic.
**An org's ceiling has a cap, and only the platform raises it** (`edge.Store.Cap`,
`rules`). Every request an org makes is charged to the org's WHOLE ceiling: its cap
— the `org_cap` a SuperAdmin set on its row, else the platform row's `org_rpm` (a
SuperAdmin's write), else `edge.DefaultOrgRPM` (60,000/min, about 1,000 rps per org
across the deployment) — lowered by the org's own `org_rpm` and any org-wide
commerce rule. The cap is NOT derived from the anonymous per-IP knob: cutting
`per_ip_rpm` during a flood must not move every paying org's ceiling. The platform's
`org_rpm` is the default cap and does not inherit into any org's own row (or it
would clip every raised `org_cap`). A tighter project/service commerce rule or
service floor is charged besides the org's ceiling, so no set of scoped rules adds
up past it. A tenant only lowers: every free signup administers its own org, so
`org_rpm` above the cap is refused at the write (403 naming `org_cap`) and clipped
by the limiter whatever is stored, and a commerce rule above it is moot. `org_cap`
is SuperAdmin-only and does not inherit, like `mode`, and every change to it is on
the audit trail (`gateway.org_cap`, the cap before and after) before it is made —
no trail, no change (503). The platform `org_rpm` is recorded the same way
(`gateway.default_cap`), whether written on the admin row or beside a platform
field. A validated caller with no org meets `Cap("")` by its
subject. `GET /v1/gateway/config` reads back what is ENFORCED: `org_rpm` is the
org's own clipped to its cap (`Store.Ceiling`), `org_cap` the cap in force.
**Before deploying this, convert the rows it would clip.** On main an org's own
`org_rpm` won over everything, so a row above the platform cap was honoured and is
now clipped to it. Read the gateway policy table (system database `gateway`, table
`policy`, one JSON `doc` per org) for them —
`SELECT org, json_extract(doc,'$.org_rpm') FROM policy WHERE org <> 'admin' AND
json_extract(doc,'$.org_rpm') > <cap>`, where `<cap>` is the admin row's `org_rpm`,
else 60000 — and for each one a SuperAdmin sets
`PUT /v1/gateway/config?org=<org> {"org_cap": <its org_rpm>}` (audited).
**The signup org is counted per person** (`counted`): `account.SignupOrg` (`hanzo`,
IAM's default org) holds every self-serve stranger, each paying from
`person:hanzo/<name>`, so EVERY window it is charged — its whole ceiling, the
service floors (`defaultServiceRPM`: audio, sbom) and scoped commerce rules — is
counted per person, split exactly as the wallets are (`principal.PayerIn`), never
one `org/hanzo` budget a single free account could spend for everyone. `org_cap` on `hanzo` is not the remedy for its
size: it is one number for every person window and every hanzo app's.
**First-party traffic carries no default** (`fleet`): anyone acting in the reserved
`admin` org, and an application (`principal.App`) of a brand org
(`brand.Registered`), is held only by an `org_cap` a SuperAdmin set on that org's
own row (`Store.OwnCap`) — and a fleet application in a window of its own
(`app/<org>/<app>`), so a brand's bot and the brand's people never spend one
budget. A brand org's people carry the default; a tenant's applications spend
their org's window with its people, so minting apps cannot multiply a cap. The
default was set without production numbers: before relying on it, read each org's
peak requests a minute from the span plane (`event.span`, one server span per /v1
request, `org` column, `attributes['http.request.method'] != ''`) over a few weeks
and pin an `org_cap` for any org near 60,000.
Tests that need pods sharing a count run on `coordtest.Memory` (in process, always) as
well as `coordtest.KV` (valkey-server, skipped where absent); the Lua the KV runs is
exercised in CI under miniredis (`sdk/coord/miniredis_test.go`). A count the
quota/ceiling script finds without an expiry gets one on its next charge.

**`X-RateLimit-*` describe the ceiling that applies** (`pace`): limit, remaining,
and `X-RateLimit-Reset` in unix seconds (the spelling zen and ai's allowance use).
When several bind one request, the one with the least room left speaks and a looser
one charged later does not overwrite it; a refusal names the ceiling that refused.
An edge backstop that admits writes nothing.
`c.Fiber().IP()` is never a key: inside a subsystem it is the host's socket, `0.0.0.0`
for every caller (`geo.Peer` names that hop on purpose).
The host stamps `address.Header` and `address.Country` (`StampClientIP`) after
deleting every caller line of both, and over a unix socket — a subsystem behind the
host — `ClientIP`/`ClientCountry`/`ClientIPOf` read those stamps and never re-read the
chain, which answers for whatever the host's own peer wrote.

`GET /v1/gateway/traffic` (`gatewayTraffic`) reports the caller's own org: lane
split, denials, screens, and its busiest credentials by fingerprint. Screens are
counted there from the first request AND metered on the org's usage ledger —
`ResourceMeter.Meter` is a no-op while `CLOUD_RISK_SCREEN_CENTS` is unset (0),
because the price belongs to the pricing catalog and inventing one here would be
a fabricated number.

**A path is what the ROUTER says it is.** `cloud.RoutePath` normalizes to fiber's
own detection path — lower-cased, trailing slashes stripped — and every security
comparison (`Privileged`, `Probe`, the gate's exemptions) runs against it, on
segment boundaries. `strings.HasPrefix` over the raw `c.Path()` meant one capital
letter routed to the key store while matching no grant prefix, so the scorer's
silence ALLOWED what the fail-closed branch exists to refuse.

**Asking is bounded.** Each ask costs a goroutine that lives until the scorer
returns, so `Decide` holds `MaxScorerCalls` in flight and answers `RefusalBusy`
past the ceiling — the same fail policy a timeout gets, reached without allocating.
A scorer stuck on a lock cannot become an out-of-memory in the process it was
installed to protect.

The other enforcement point is in `hanzoai/iam` (`internal/risk` +
`internal/oidc/signup_gate.go`), which calls `POST /v1/risk/decide` over the wire
at sign-up. Its arming signal is `RISK_URL` rather than a per-org mode; the
semantic is identical — fail closed once armed, allow before.

## "edge" is a position, not a product, so it owns no prefix

Five unrelated things wore the word, which is why it read like a missing product:

| called "edge" | what it is | prefix |
|---|---|---|
| `hanzoai/edge` | on-device inference runtime (Rust; candle + GGUF + WASM). A binary the customer runs on their OWN machine. | **none, forever** |
| `hanzoai/catalog` | the public catalogue cache (Cloudflare Worker, `catalog.hanzo.ai`) | none — a cache is not a product |
| `apps/gateway/edge` | the CORS allowlist + per-IP flood cap + per-org rate ceiling store | it IS the gateway role; `/v1/gateway/config` |
| `apps/projects/edge.go` | the CDN in front of published sites — provider, reach, cache policy | it is a fact about projects; `/v1/projects/edge` |
| `/v1/edge/nodes` (`apps/network`) | ZT fabric **edge-routers** — the nodes of an overlay network | now `/v1/network/routers` |

**A prefix belongs to a product a customer calls.** Edge names a *position* in
every one of those senses, so it gets none. `hanzoai/edge` keeps the repo name —
it is the one honest use of the word, because it genuinely runs at the edge of
the network, the user's device — and it gets no cloud prefix for the same reason
the CLI has none. **`/v1/edge` 404s at every depth and that is CORRECT, not a
gap.**

## The public catalog is one copy at the edge (`publicCatalog`, `EdgeCORS` `shared`)

`publicCatalog` (build.go) names the paths whose anonymous answer is the URL's
alone: `/v1/models`, `/v1/pricing`, `/v1/research/runs`, `/v1/template` (the
host rewrites `/v1/templates` to it before any app sees the path, so the map
holds the canonical spelling) and `/.well-known/agent-skills`. For an anonymous
GET or HEAD of one, with or without an Origin:

- `EdgeCORS` answers `Access-Control-Allow-Origin: *`, no credentials and no
  `Vary: Origin`, and takes the Origin off the request so no child answers it
  again (`shared` in middleware_edge.go). With no Origin the answer is the same,
  because Cloudflare keys on the URL and the request that fills its copy may be
  a crawler's.

A credentialed read of the same URL keeps the reflected origin, credentials,
`Vary: Origin` and `private, no-store`. universe `infra/cf-zones/cache.py` lists
the four app paths in CATALOG and SHARED; `/.well-known/agent-skills` is
answered by the host, outside the apps' edge, so it carries no
Access-Control-Allow-Origin and is in CATALOG alone. Apply it only after cloud answers `*`, and
at least ten minutes later, so that no older copy without the header is left
at the edge.

**A write clears the copies.** `client.PurgeEdge(platform, host, paths...)` asks the
project app's plane op `sites_purge` (platform only; 1 to 30 `host/path` prefixes),
which calls `Edge.PurgePrefixes` on the one Cloudflare client (apps/sites/cloudflare:
same coalescing and ceiling as `PurgeTags`, body `{"prefixes": [...]}`). A plan write
(`apps/admin/plans.go` `forget`) clears `/v1/billing/plans`, `/v1/plan`,
`/v1/commerce/catalog` and `/v1/pricing` on the brand's API host. With `CF_ZONE_ID`
pinned the purge reaches that one zone, so a prefix on another zone's host is refused
there and logged; unset it and the client finds hanzo.app and hanzo.ai itself. Not
hooked yet: pricing overlay writes (apps/pricing), the model listing sync
(apps/ai/listing.go), commerce's own catalog writes, and the forge (a push should
clear `git.hanzo.ai/<owner>/<repo>` after the forge has pulled it, not at the webhook).

`/v1/billing/plans` is NOT in the map, though its answer qualifies (the brand
comes from the Host). hanzo-inc/pay and @hanzo/ui's onboarding read it signed
out with `credentials: 'include'`, and a browser refuses `*` in that mode. It
joins the map once both read it without credentials.

`/v1/research/runs` pages for a slow reader: `?benchmark=` and `?id=` take comma
lists, `?category=` and `?metric=` narrow each run's measures,
`?view=summary` drops what only a harness reads, and `?limit=` with `?cursor=`
walk the listing in its order, 100 to a page when the query names no limit
(`total` counts every match, and the last page carries no `next`). apps/research/listing.go is the whole of it.
