# LLM.md — apps/event

Guidance for apps/event. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The automatic debug-and-resolve loop (`insights.hanzo.ai`)

An error occurs, the session and the person who hit it are already on the plane,
an agent diagnoses it, and where the evidence carries it the loop opens a PR or
answers the customer. This section is the design. It is a WIRING decision: every
rung below already exists as a shipped surface, and the loop is a trigger plus a
join plus a policy about who may pull which rung.

### 1. The trigger

Promote a fingerprint to an investigation when it is BOTH new and has reach:

> never investigated before, AND ≥ 3 distinct `person_id`/`distinct_id` in the
> window, AND at least one occurrence in the last hour.

Novelty and reach are both required because either alone is wrong on measured
data. Novelty alone fires on our own test junk — the rows whose messages are
`"0"`, `"13"`, `"test"`, `"data"`, `"undefined"`, `"{}"`. Reach alone re-fires
forever on `TypeError: Failed to fetch`, which is 151 of 1,058 rows and is
mostly a user's wifi. Together they select the thing worth a run: something
nobody has seen that is happening to more than one person.

**It is cheap because the plane was built for exactly this query.** `event.fact`
is `ORDER BY (org, time, id)` and `PARTITION BY (signal, toYYYYMM(ingested_at))`,
so scoping to one org, one signal and one hour is a partition prune plus a prefix
seek. It also carries eleven skip indexes, and five of them are the ones this
loop needs: **`by_issue`, `by_session`, `by_trace`, `by_person`** (bloom filters)
and **`by_file`** (a bloom filter on the stack frames' file array). The trigger,
the join and the attribution step each ride an index that already exists. 68
distinct fingerprints exist across all of history. At a thousand times today's
volume this is still a rounding error, and it never touches the 146M-row metric
store or the 132M-row log store.

The novelty lookback needs no configuring: `event.fact` TTLs the `error` signal at
**90 days** (act 2y, log/span/clip 30d). Ninety days is the window, because after
that the plane genuinely does not remember and claiming otherwise would be a
fingerprint that looks new every quarter.

**What is explicitly NOT a trigger.** Not every error occurrence — occurrences
are the thing being grouped, not the unit of work. Not `event.log` severity
`error`: 610,900 rows an hour is the fleet talking to itself, and a loop reading
it would investigate our own retry chatter forever. Not a metric threshold —
that is alerting, `apps/o11y/alerts.go` already does it, a second answer to "is
this service unhealthy" is a second answer, and in any case **`event.metric` has
no `org` column at all**: its identity is `(env, temporality, metric_name,
fingerprint)`, so two orgs reporting one metric name hash to one series. A
per-tenant trigger cannot be built on it, which is why the write core refuses
metrics rather than landing them. Not latency. Not a customer
opening a ticket, because the ticket path is a different rung (below) and not
every ticket is an error.

The two fingerprint widths in the data (64-hex and 16-hex) are two generations of
the hash. Key on the value, not the width.

### 2. The join

**The join key is `session_id`, on the pair `(org, session_id)`.** It exists in
schema on `event.fact` and every projection over it, and on the replay summary.
Whether it JOINS is a separate question, and the measurement is blunt:

| join | overlap |
|---|---|
| `event.error` → replay | **0** of 73 |
| `event.event` → replay | **0** of 1,754 |
| `event.session` → replay | **120 of 128 (94%)** |

The key space is live and correct — the native session rollup matches replay
almost perfectly. The error plane is the one thing standing outside it.

**The reason is one missing stamp, and the join is otherwise already built.**
`event.session_roll` is a materialized view over `event.fact`
`WHERE session_id != ''`, and it ALREADY computes
`errors = uniqExactIfState(id, signal = 'error')` per session. The column is
there, the rollup is there, the replay match is there. It reads zero because the
browser SDK stamps `session_id` on pageviews and clips and does not stamp it on
the error signal. For org `hanzo`: 220 errors, 2 with a session.

So the deliverable is not a pipeline. It is one field pair at one emitter:

- **Deliverable A — stamp `session_id` on the error signal in `@hanzo/event`**,
  the same value the pageview and clip paths already emit. This converts a 0%
  join into the ~94% the session plane already demonstrates.
- **Deliverable B — stamp `trace_id` on the error signal.** `event.error.trace_id`
  is 0 of 1,058, all time, while `event.span` carries `trace_id` on 100% of
  1.87M spans a day across 120 services. The server-side causal chain is fully
  recorded and completely unreachable from an error, for want of one stamp.
  Note that spans carry `session_id` on 0% — so browser-session ↔ server-trace
  is a stamp on BOTH ends, and B is the half that pays first.

**Tenant safety is a prerequisite, not plumbing.** The replay summary is keyed
`team_id Int64` (2 teams); the event plane is keyed `org LowCardinality(String)`
(24 orgs). They are different namespaces. **A join on bare `session_id` with no
tenant predicate can return another org's replay.** Session ids are UUIDs so a
collision is unlikely, but "unlikely" is not a tenant boundary, and the loop
would be reading customer data across orgs. The org ↔ team_id resolution must
exist and every replay read must carry it. This is deliverable C, and no rung of
the ladder may touch replay before it lands.

Identity is already there and does not need a deliverable: `distinct_id` on 808
of 1,058 rows, `person_id` on 195. It is pseudonymous, which is what the agent
should see (privacy, below).

### 3. The evidence bar — what separates diagnosed from guessed

A confident wrong PR is worse than nothing, so the bar is a property of the
ARTIFACT, not the model's confidence. An investigation may propose a code change
only when all three hold:

1. **An owned frame.** At least one `frames.own = true` entry resolving to a
   file and line that exists in a repo at the revision in question. Frames are
   columns, so this is a lookup, not an inference. No owned frame → the fault is
   in someone else's code or the stack was stripped → verdict is
   `unattributed`, and the ladder stops at notify.

2. **A bracketing revision.** The fingerprint's first-seen time falls after a
   known deploy of the implicated service. `release` is populated on 17 of 1,058
   rows today (1.6%), so this is mostly unavailable — the fallback is the CD
   App CR revision for that `service`, which is recorded. Where neither resolves,
   the verdict degrades to `unattributed`.

3. **A reproduction.** A test that FAILS before the change and PASSES after.

Rung 3 is the whole bar. Rungs 1 and 2 narrow the search; only 3 distinguishes
diagnosis from a plausible story, because it is the one artifact a confident
model cannot produce by being confident. "It compiles", "the diff looks right",
and "the model explained the bug" are not evidence. A run that cannot write a
failing test has not diagnosed anything, and it says so.

A session replay is CORROBORATION, never attribution. It shows what the user did;
it cannot show which line is wrong. It may not substitute for rung 1.

### 4. The action ladder, and where the human stands

Every rung is an existing surface. Nothing here is new machinery.

| rung | surface | who |
|---|---|---|
| annotate | o11y annotation / `event.fact` | automatic |
| notify | `apps/notify` | automatic |
| open an issue | `apps/todo` | automatic |
| open a **draft** PR | `todo.CreateAgentPR` (`Kind:"pr"`, `Source:"agent"`) | automatic, bar met, our repos only |
| **enqueue for review** | `POST /v1/o11y/reviews/:id/items` | **← the human stands here** |
| reply to the customer | `apps/help` ticket thread | human first time, automatic on recurrence |
| merge | forge | **human, always** |

**The review queue is already the right shape and already accepts the right
object.** `apps/o11y/annotation_queues.go` has `PENDING`/`COMPLETED`, an
assignee, and `validObjectType = {TRACE, OBSERVATION, SESSION}` — a queue item
can reference a SESSION today. That is exactly "here is a session that broke, a
human should look". Do not build a second queue.

**Merge is never automatic.** A merged wrong fix is the one action on this ladder
with no cheap undo, and the loop's whole value is that it is cheap. The PR is a
draft; a human merging it is the loop's output, not its failure.

**"Resolve support issues automatically" — where that is actually safe.** The
honest answer decomplects novelty from recurrence, which is free because the
trigger is already fingerprint-keyed:

- The **first** occurrence of a fingerprint is answered by a human. The loop
  drafts, cites, and enqueues; a person sends it.
- The **Nth** occurrence of that SAME fingerprint is answered automatically,
  because the reply is a REPLAY of an answer a human already approved for that
  exact fingerprint — not a generated one.

That is a real automation of the support load (the volume is in recurrence: one
fingerprint is 151 of 1,058 rows) without ever sending a customer a sentence no
human has read. A generated first reply is not on the ladder at any rung. The
gain compounds honestly: every fingerprint a human answers once is answered free
forever after, and the loop's job is to make that first answer fast and
well-evidenced.

An auto-reply is withdrawn the moment the fingerprint's shape changes — a new
`class`, a new owned frame, or a new `service` re-opens it as novel and it goes
back to a human.

### 5. The privacy boundary

Session replay is customer data, it is unscrubbed, and this is the part where
being wrong is most expensive.

**What the agent may read:** the `event.error` row, and the replay SUMMARY row
(`click_count`, `keypress_count`, timestamps, counts, urls). Both are structured
and bounded.

**What the agent may never read: the raw rrweb blocks.** `replay.go` carries
recordings VERBATIM by design — "cloud re-encodes nothing and drops no field it
does not understand" — so the snapshot contains whatever the user typed, on any
page, including into fields nobody remembered to mask. The one reader of the blocks
is a HUMAN SuperAdmin's playback (`/v1/admin/event/:org/:name/sessions/:session`,
below), never a tool a model can call. Keeping the agent on the summary is what
makes the whole design safe by construction rather than by a filter someone has to
maintain.

**Redaction happens where it already happens, and nowhere new.** Two sites exist
and both stay:

1. Client-side masking in the recorder — the only place that can stop capture.

2. `scrubText` / `scrubException` at the fold in `apps/event/capture.go` —
   the ONE server-side scrubber. It drops the credential and PII key families
   (`password`, `secret`, `authorization`, `api_key`, `ssn`, `cookie`, …), drops
   name/email/phone keys outright, and redacts email- and token-shaped substrings
   from free text including stack frames. Error rows reaching the agent are
   already clean.

**One concrete hole, and it is cheap to close.** The replay summary's `first_url`
and `all_urls` never pass through the fold — they are derived downstream by the
ingester, not written through cloud's write core. A URL with `?token=…` or
`?access_token=…` in it reaches a model unredacted. `secretRe` already matches
exactly that shape. **Route those two fields through the EXISTING `scrubText`
at the read, before they reach a model.** Do not write a second scrubber; extend
the one that exists if it needs extending.

**Identity reaching the model is pseudonymous.** `distinct_id` and `person_id`
are identifiers, and the scrub already drops names and emails. The agent
diagnoses for `person_id`, never for a human being's name. Resolving a pseudonym
to a person is a human action on the reply rung, where a human is already
standing.

### 6. Cost and blast radius

**One investigation per fingerprint per org, ever.** Dedup on the fingerprint IS
the cost bound — it is not a quota bolted on beside one. 68 fingerprints exist
across all of history; the steady-state spend is new fingerprints per day, which
is a number in the single digits, not a function of the 1,058 occurrences or the
146M-row plane. A fingerprint that was investigated and closed re-opens only if
its shape changes.

A per-org ceiling on new investigations per day lives at the same place the
trigger is evaluated — one site decides whether a run happens, not two.

**Blast radius is bounded by what the rungs can do, not by the agent's
restraint.** Draft PRs against our own repos, never a customer's. No merge. No
first-time customer reply. Nothing on the ladder below the human's position is
irreversible.

```
_meta["hanzo.ai/refused"] = {count: 134, rule: "a tool is not projected when its
  name discloses a bearer secret at any verb, or when a mutating verb acts on an
  identity or authority object"}
```

`fleet/surface.go`'s `refuse()` is that rule and it is the only gate. Two
properties make it worth relying on rather than duplicating:

- **It is a rule over NOUNS and VERBS, not a roster.** Clause 1 refuses a name
  that handles a bearer secret at *every* verb, because reading
  `GET …/reset_password_tokens` discloses the secret exactly as the PUT that
  mints it does. Clause 2 refuses a mutating verb acting on an identity or
  authority object, while the matching READ survives — `GetRole` and `GetUser`
  live, because knowing who holds a role is not granting one. So op 1,324 is
  classified the day it is written, which a hand-maintained list of 36 names
  cannot do.
- **It is applied inside `gather()`, where the routing table is written**, so a
  refused name is absent from BOTH `tools/list` and `tools/call` — unroutable
  even for a client that cached it. That is a boundary, not a preference. The
  separate `rank()` is the preference half (it only reorders, because truncating
  clients keep the head of the list), and the two stay separate.

The withholding is reported rather than silent: a shortened list carries its own
count and rule, so an operator who wonders where `CreateServiceAccountKey` went
reads why instead of filing a bug against a subsystem that is serving it
correctly. The same `_meta` carried `unavailable: [{app: x402, …}]` when I
measured it — an outage and a policy are told apart on the wire.

The loop gets no second surface and no exemption. A capability denied to a
customer's agent is denied to this one by the same line of code, and there is no
second policy site to keep in agreement.

One invariant this still rests on, worth a test rather than an assumption:
`SanitizeIdentity` deletes every authority header and re-mints it from verified
IAM claims (org from the signed `orgs` membership set, never the `owner` claim),
and each typed op's `tenantOf` scopes the read. That holds only while **every
child runs the full `cloud.Serve` chain** — a subsystem mounted by address that
skipped it would trust a forwarded `X-User-IsAdmin: true`.

Runs are metered on the existing agents billing path. A run is a billed act and
is attributed to the org whose error triggered it.

### 7. What this design does NOT build, and why

- **No second ingest endpoint.** `POST /v1/event` is the one endpoint; errors already
  arrive through it.
- **No second tool surface.** The fleet MCP endpoint and its `refuse()` are the one
  policy site.
- **No replay read for the loop.** The loop reads the summary from the warehouse
  and nothing else. The only reader of replay object storage is the SuperAdmin
  playback (`apps/event/recording.go`), a person at a player, never a run.
- **No second scrubber.** One scrub site; extend it or route through it.
- **No new queue, no new todo, no new notifier.** Reviews, todo and notify
  exist.
- **No new auth, no new "gate".** IAM principal + `tenantOf` on every read, as
  every typed op already does.
- **No trigger on `event.log` or `event.metric`.** 132M and 146M rows, and the
  interesting signal is not there.
- **No auto-merge and no generated first reply to a customer.** Stated above.
- **No reader that unions `event.error` and `event.fact`.** Two spellings of one
  fact is what the merge commit was for; a compatibility union would make the
  split permanent and would be the second answer this repo keeps deleting. Fix
  the writer.
- **Not fixing `/v1/o11y/sessions` or `/v1/o11y/services` here.** Both are real
  bugs (§ measurements) and neither is on this loop's path — the loop reads the
  warehouse directly. They are separate one-line fixes and should be filed as
  such, not absorbed into this design to make it look larger.

### 8. The smallest first increment

**Stamp `session_id` and `trace_id` on the error signal in `@hanzo/event`.**

That is the increment. One field pair, one SDK, no new infrastructure, no new
route, no agent. It is worth doing before anything else because everything
downstream is a read over data that does not exist yet, and because the
machinery it feeds is already built and already correct: the columns are on
`event.error`, the MV `session_roll` already counts errors per session, and the
session plane already matches replay at 94%. Today that rollup reports zero
errors on every session it can match. After this change it reports the truth, and
"show me the replay of a session that hit this fingerprint" becomes a query
rather than a project.

**It depends on one thing, and it is already owed:** redeploying `analytics` past
`e5a0712d` so errors land on `event.fact` at all. That is a version bump, not
work. No schema change, no migration, no new service.

**It is verifiable the day it ships**, by the query that returns 0 today:

```sql
SELECT uniqExact(session_id) FROM event.fact
WHERE signal = 'error' AND session_id != ''
  AND session_id IN (SELECT session_id FROM insights.sharded_session_replay_events)
```

Run it against `event.error` too until the writer moves; the two answers
converging on one plane is how you know the migration finished.

Ship it, watch that number leave zero, and only then build the trigger — which is
a scheduled `GROUP BY` over a thousand rows, and is the easy part.

**Order after that:** deliverable C (org ↔ team_id, the tenant boundary) before
any rung reads replay; then the trigger and the annotate/notify/issue rungs;
then the draft-PR rung once the reproduction bar can actually be enforced by the
coding backend. The customer-reply rung is last, and its first version is the
human-approved replay of a blessed answer, never generation.

### 9. How this composes with the agentic coding backend

The loop supplies a BRIEF and consumes a VERDICT; it does not run the model.

What it hands over is fully determined by the sections above: the fingerprint,
the owned frames (file, line, function — already columns), the implicated
`service` and `release`, the affected `person_id` count, and a corroborating
session reference where one joins. What it requires back is the reproduction of
§3 — a test that failed before and passes after — because that is the artifact
that decides which rung the ladder stops at. A run that returns a diff without
one is `unattributed`, and `unattributed` stops at notify.

**Where the loop's agent gets its tools — settled against what exists.**
`apps/agents/surface.go` already routes a run's tool calls through `fleet.MCP` over
the host socket, deliberately so that "an agent CANNOT see a surface an external
client cannot; there is no second surface to see." Building a second aggregation
on the tool plane was considered there and rejected for the reason this document
keeps repeating: it would be a second place for the curation rule to be applied,
or forgotten. Two properties come free and both matter here:

- the same `refuse()` denylist, enforced in `gather()`, so the loop's agent is
  bound by clause 1 and clause 2 like anyone else;
- **identity is stated by the RUN, not by the model** — org and actor are passed
  as arguments from `executeRun` and written as zip identity headers, while the
  model contributes only a tool name and an arguments object. There is no path by
  which a model can name a tenant. Inbound caller headers are deliberately not
  forwarded, because a scheduled run has no inbound request and a nested one may
  be running for a different principal.

Every rung of § 4 is already on that endpoint: `analytics` publishes
`get_v1_errors`, `o11y` publishes 318 ops including the review queues,
and `todo` and `help` are both projected. So the ladder needs no
new tool surface — only the policy about which rung may be pulled without a
human, which is § 4's job and not the endpoint's.

**The division of labour is therefore by role, not by capability.** The TRIGGER
queries the warehouse directly, because it must: deciding whether to investigate
IS a warehouse query, and the replay summary lives in `insights.*` — a different
database, reached by no typed op and therefore by no tool. That data is already
in hand by the time an agent starts, so passing it as a brief costs nothing and
re-fetching it through a tool call would be a second retrieval of something
already retrieved. The AGENT gets that brief plus the endpoint, and reaches for the
endpoint when the brief turns out to be insufficient.

What is *not* justified is handing the coding backend a licence to roam the
telemetry plane as its opening move. That is a least-authority preference, not a
containment claim — containment is `refuse()` plus run-stated identity, and both
hold whichever way this goes.

## One person's history, and the operator mark (`apps/event`)

**Reads** (SuperAdmin only, `principal.Super`; `/v1/admin/event` is the event app's
HIP-0139 operator prefix, beside `/v1/admin/team`):

| | |
|---|---|
| `GET /v1/admin/event/:org/:name` | the account's events across every tenant, newest first; `range` (default 30d) / `start`+`end`, `tenant`, `limit` ≤500, keyset `cursor` ← `next` |
| `GET /v1/admin/event/:org/:name/sessions` | visits (session_id), each with `recording` summary or null |
| `GET /v1/admin/event/:org/:name/sessions/:session` | one visit: events oldest first, replay `blocks`, rrweb `frames` (≤32 MiB, `truncated`) |

Admin SPA routes: `/users/:org/:name` (history + sessions), `/users/:org/:name/sessions/:session` (rrweb player over `frames[].event`).

**Who.** `<owner>/<name>` → IAM id (operator's own creds replayed to IAM; IAM down →
`degraded:["iam"]`). The plane files a person under the sub AND `<owner>/<name>`
(both live, measured), plus anonymous ids linked by `person_id` rows. Email is never
on the plane (scrubbed), so email → account goes through `/v1/admin/users?q=`.
Every id but `$subject` is a client CLAIM a publishable key can make for anyone: a
row is `attested` only when the boundary verified its writer as one of the person's
`keys`. Links from verified writes rank first; past 500 `partial` says so. The gate
refuses a request carrying `X-Acted-By`.

**The operator mark.** IAM's impersonation token (`imp:true`, operator in `act.sub`)
→ the boundary mints `X-Acted-By` from the verified payload (stripped on ingress; an
`imp` naming no operator authenticates nobody). `operatorOf` (masquerade.go) also
marks a SuperAdmin org switch. `mark` in `ingestEvents` is the only writer of
`attributes.impersonated`/`impersonated_by`/`$subject` (the verified X-User-Id);
operator events never reach ad sinks.
Product lenses (`overview`, `top`, `insights/events`, outcomes, campaign) exclude
them by default; `?impersonated=include|only`. The LLM ledger (`hanzo.cloud_usage`,
written by hanzoai/ai) carries no mark: usage under an impersonation token bills and
attributes to the TARGET (`principal.ledgerOf`: not Super → effective org). Not yet
marked: metering `Usage`, replay batches; the audit trail is the next section. Open:
IAM mints `imp` only for the apps in `IAM_IMPERSONATION_APPS` (next section); its other
operator flow, `/v1/iam/assume` (operator's own sub + `assumed`), is marked only through
the org switch; replay rows carry `team_id`, not org; a tenant can re-post an operator
row's `(time, messageId)` unmarked and ReplacingMergeTree keeps the newer; `guide`,
`marketing/audiences`, `risk`, `reference` read `event.fact` without the mark filter.

## Who is asking: `event.Audience` (bot · internal · person)

One classifier (`apps/event/audience.go`), read by the tag config
(`GET /v1/project/tags` answers `audience`) and by ingest (`visitors` stamps
`bot: true` or `internal: true` into every non-vouched event, server-side, over
whatever the client sent). No configuration.

- **bot**: the user agent names a program (`robot`: headless and driven
  browsers, crawlers, link previews, uptime checks, HTTP libraries; an empty
  agent is a program), or `geo.IP` is inside a cloud provider's
  published ranges (`clouds.go`: AWS, Google Cloud, Azure's AzureCloud tag,
  Oracle, DigitalOcean, Linode; Hetzner and OVH publish none). The ranges load at
  mount (`WatchClouds`, called by the event and projects apps) and daily, held as
  one sorted set; a provider that fails keeps its last ranges.
- **internal**: a validated person whose signed membership set names the site's
  org — in a brand's own org (`brand.Registered`: every self-serve account lives
  there as a plain member) only its admins and SuperAdmins — or an address in the
  project's `internalNetworks` (CIDRs, `PATCH /v1/project/:slug`, empty by
  default). The site's org is the resolved project's, else the org the key names
  (`event.Admit`). A tag-config Bearer that is not a `pk-` is identity, never the key.
- **person**: everyone else.

The tag set answers for the audience (nothing for a bot, `ga` alone for the
team). `event.FromPerson` is the one reading of the marks: the destination
consumer (the real ad forwarder, on the bus) drops any event that is not a
person's; `test` orders still go to the platforms' test channels. The insights
bridge carries `bot` → `$bot` beside `$internal`. @hanzo/event ≥ 0.3.55 reads
`audience`, sends the site's IAM bearer (`startTags({token})`) and marks a team
browser with the `hz_internal` cookie.

## Consent: the stream is first party; the third parties follow the region (`apps/event`)

Owner's rule: Hanzo's own analytics on /v1/event — page views, events, errors,
identify, the anonymous id and session — is essential, never sold or shared, and is
kept for every visitor whatever they chose, in any region, under GPC/DNT alike (stamp
keeps everything but localhost; the projected lane and the image pixel too).

The third parties follow the visitor's REGION, decided per request by
`ConsentPolicy.Decide(country, region, refused)` from `geo.Country` and
`geo.Region` (Cloudflare's `CF-IPCountry` / `CF-Region-Code`, read only from a
Cloudflare hop; behind the host the stamps `X-Client-Country` / `X-Client-Region`) and
served on `GET /v1/project/tags` as `consent` (`Cache-Control: private`, `Vary:
Authorization, Sec-GPC, DNT, CF-IPCountry, CF-Region-Code`). The lists are data in
`consent_policy.json` (version 3, floor 2: version-2 choices still count):

- **eu** — EU27 + GB, IS, LI, NO, CH, and any visitor no edge placed (no header, XX,
  T1): GA4 loads for everyone under Consent Mode v2 default all-denied +
  `wait_for_update: 500` (cookieless pings, no cookie), upgraded by the choice; Meta stub gets
  `fbq('consent','revoke')` before `init`, and fbevents, `consent grant` and the
  PageView wait for Marketing; other platforms wait for Marketing; opt-in banner.
- **us_privacy** — a `us_privacy` state, or the US with no attested state: everything
  on (Consent Mode default granted), no banner, a "Your Privacy Choices" link with the
  CPPA opt-out icon. Ads off (the visitor's opt-out) is exactly
  `fbq('dataProcessingOptions',['LDU'],0,0)` + `gtag('consent','update',{ad_storage,
  ad_user_data, ad_personalization: 'denied'})`.
- **global** — everywhere else: as us_privacy, plus a notice closed by ×
  (`hz_consent_notice`, grants nothing); any category saved off is an opt-out.

`Decision.Effective`: GPC/DNT forwards nothing, in every region, and the browser loads
no third party under it (GA included). A choice is stored beside its version and rule
(`hz_consent_rule`): eu counts only an eu choice (or a version-2 one); the floor voids
a choice's grants, never its refusals, and a choice that allowed nothing is never asked
again. A withdrawal that needs a reload waits while the sheet is open. Forwarding is
unchanged otherwise: each event is stamped with the grants it may be forwarded under
(`consent`, `consent_mode`, `refused`; none from an @hanzo/event < 0.3.58), and `admit`
sends GA4/Meta nothing without them. Session replay (`replay.go`) waits for
`analytics` and never runs under GPC/DNT (eu's rule).

- **tag.js**: the stream always sends; each event states the grants in force at flush
  (a rule that arrived, or a choice made or withdrawn, since it was queued). The rule
  is cached per tab (`sessionStorage` `hz_consent_policy`, as @hanzo/event keeps it);
  with none, nothing is drawn until cloud answers or 1.5 s, then eu. Categories:
  analytics = GA cookies (`_ga*`, `_gid`, `_gat*`, `hz_cid`), marketing = Meta and the ad
  platforms' ids + `hz_touch`, ads = Google ad cookies (`_gcl_*`, `_gac_*`); withdrawal
  forgets them on every parent domain. Google hears changes by Consent Mode and Meta by
  consent/LDU with no reload; only a library that cannot be told (GA outside eu, the
  other ad platforms) reloads. gtag events carry `send_to` (their platform's ids).
- **`data-banner`**: by region. eu: Hanzo's one consent design (@hanzo/ui Consent), for
  whom `asks()`. global: the notice. us_privacy: the footer link (none when the page has
  an element marked `data-hz-consent-link`); outside eu the sheet is "Your Privacy
  Choices", every switch on by default. eu details: Bar (`role=region`, rounded 16px
  card inside the safe area, no links):
  "Essential cookies keep this site running…", Manage preferences / Accept all, and
  × = save essential-only (refusing as easy as accepting). Sheet (modal `dialog`,
  Tab wraps, Escape/× close unanswered): "Cookie Preferences", the intro linking only
  the policies the page names (`data-cookies`, `data-banner`, `data-terms`), folding
  sections Strictly Necessary (Always Active, the site's own first-party analytics included)
  / Performance (Google Analytics) / Marketing (the Meta pixel) / Sell, Share, or Use
  Your Data for Targeted Advertising (Google and Meta targeting ads), then Accept all and
  Save preferences. Focus returns to what held it before it came in. Writes the
  cookie as @hanzo/event `setOwn` does. Copy names Google Analytics and the Meta pixel
  only.
- The image pixel (`pixel.go`) records every view under a daily visitor id;
  `?consent=analytics` is only what it may be forwarded under.
- **Several Meta pixels** (one per brand): `Project.Tags["meta"]` is a comma list;
  `buildTags` emits one `meta` tag per numeric id (trimmed, once, in order, rules on
  each). tag.js builds the stub once, states Meta's consent and LDU once before the
  first init, inits each, then (under Marketing) loads fbevents once and sends ONE
  `fbq('track','PageView')` (track reaches every init'ed pixel); each event fires once
  per platform with one eventID. Server CAPI stays per-org.
- tag.js never throws into the host: `page()` from `navigated` and at load is
  guarded, `sessionId` survives a full store; the banner needs shadow DOM (else
  skipped) and styles itself by a constructed sheet or a `<style>` with the script's
  nonce; `sync` redraws a bar the page removed or one drawn under a rule that changed.
