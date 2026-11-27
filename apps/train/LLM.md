# LLM.md — apps/train

Guidance for apps/train. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Training (`apps/train`, `/v1/train`, HIP-1333)

The one training endpoint; ga for every org, no flag. Two nouns: **clients** (the
engine's `create → forward_backward → optim_step → sample → save_weights` wire, relayed
to `ENGINE_UPSTREAM`, SuperAdmin only — a client loads any model into the memory every
org's inference shares, and takes a local path — `TRAIN_CLIENTS` 2 per org and
`TRAIN_ENGINE_CLIENTS` 2 on the engine, metered `train/client-second` on success) and
**jobs** (the loop an executor runs). Every job read, write and claim is per org in
`train.db` (`cloud.OrgStore`); bytes are in S3 `TRAIN_BUCKET` (`hanzo-train`) at
`<org>/<sha256>`.

**Where weights live.** A job's outputs (checkpoint, capability: a deterministic tar)
and the org's datasets are objects in `hanzo-train` at `<org>/<sha256>`, sealed under
the org's KMS key `train-<org>`, recorded in the org's `train.db` `objects`. Base
weights are the executor's: `train serve` reads them on its own machine, and a result
names the base's `sha256`, which publish records as `base_sha256`. Publishing a head
first copies its tar to the serving store, the platform's `models` bucket
(`BucketName("hanzo", "models")`) at `capabilities/<org>/<sha256>` (`objects.serve`),
where the decision service fetches it over the S3 API under its own key, which reads
the bucket's `kai/` and `capabilities/` and nothing else (cloud writes with its own). ai attaches the
org's published capabilities to each decision it forwards (`X-Org-Capabilities`),
read over the plane op `train_capabilities` (`client/train`); it and
`GET /v1/train/published` read one function, `capabilitiesOf`.

**Defaults the owner may change** (each is the fallback when commerce's meter authority
has no row; an operator sets the row at admin.hanzo.ai): compute `train/kai-gpu-hour`
$3.48 a device-hour (`gpuHourFloor`, the catalog's GPU Basic), `train/kai-cpu-hour`
$0.08 (`cloud.RuntimeHourMicros`), storage `train/storage-gb-month` $0.02
(`storageFloorMicros`, the catalog's S3 row). `budget` is in US cents.

### Files

| file | what it owns |
|---|---|
| `train.go` | `Use`, `state`, the route table, `tenant` (org store + lease expiry on every read), `ship` (durable before ack, else 503), the bell and `poll` long-polls, `GET /models` |
| `spec.go` | `Spec`, the `families` table `GET /models` answers (kai: `full`, `readout`, `auto`; its `runs` — where, noun, executor, devices, and the meter that prices a device-second; datasets `stage`, `artifact`), validation order shape → base → `agree` → `supported` |
| `jobs.go` | create (reads an `artifact:` dataset back; the 402 gate for a job Hanzo runs), list, get (`usage`; a stop's reason is `error`), cancel, events, metrics, job artifacts with links |
| `executor.go` | the claim (org machine and `platform`), the report, artifact registration, `admit` (grant) and `stored` (read-back) |
| `money.go` | rates, `fit` (device kind and count), `extend` (next window vs caps and the payer's `holding`), `use` (meter by the clock, charge into the outbox), `window` (hold, or stop and cancel), `settle` (ship, then send the outbox), `locks` (one money gate per payer), stop reasons |
| `store.go` | schema + `columns` + `reshape`; tasks (`take`/`untake`/`held`/`use`/`hold` CAS), the `charges` outbox (`owed`, `paid`), `committed`, `outstanding`, `expire`, `end`, `finish`, `publish` (route check inside the tx), objects (`seal`), `pending`/`lapsed`/`lapse`, storage watermarks, `stale`, `holdings` |
| `objects.go` | the S3 bucket: `grant` (single PUT or 64 MiB parts, SSE-KMS headers signed), `verify` (completes parts; size, checksum and the org's key, else deleted), `link`, `remove`, `abort`; `grantTTL` 2 h, `pendingCeiling` 128 GiB |
| `keys.go` | the org's SSE key: `train-<org>` minted once into KMS at `s3/train-<org>@prod` (32 random bytes, hex), single-flight, read back; the cloud never holds it |
| `publish.go` | `Route`/`Options` (kai `routing.json` shape), `check`, `clash`, publish (`form` head/full), `GET /published` with `revision` |
| `uploads.go` | `POST /artifacts` (datasets), list/get/delete objects, `forget`, `deleting` (the audit record a platform deletion needs) |
| `sweep.go` | the hourly per-process sweep over owned orgs: expire, `settle`, storage billing, `reap` (lapsed uploads), retention, `Settle` |
| `clients.go` | engine clients |

### Endpoints

| route | who | what |
|---|---|---|
| `GET /v1/train/models` | org | every base the caller can train, by name: `adaptations`, `runs` (each `where` hanzo/machines, `devices`, `price_nano_usd` a device-second from the meter authority; by place, then price), `nouns`, `executor`, `devices`, `protect`, `terms`, `outputs`, `datasets`. `clients` is listed to a SuperAdmin only |
| `POST /v1/train/jobs` | org | create; a job naming no machines is Hanzo's and needs `resources.budget` (400 `budget_required`), 402/503 when its payer cannot hold one device's first window on its own; 409 `job_limit` past 64 jobs not ended (`maxLive`) |
| `GET /v1/train/jobs`, `GET /jobs/{id}`, `POST /jobs/{id}/cancel` | org | read, cancel |
| `GET /jobs/{id}/events` (`after`, `wait` ≤ 25 s), `/metrics`, `/artifacts` | org | follow a run; artifact links live 10 min |
| `POST /jobs/{id}/publish` | org | name + `route: {keys, options?: {<key>: {type: choice\|score\|noul, labels}}}`; 409 `route_conflict` |
| `POST /v1/train/artifacts` | org | upload JSON Lines rows (`kind: dataset`); same grant as an executor's, 409 `pending_limit` past 128 GiB granted and unconfirmed, 402/503 when the payer cannot pay a month of what it keeps in the org + the org's pending + these bytes (`admit`, both paths) |
| `GET /v1/train/artifacts`, `GET/DELETE /artifacts/{sha256}` | org | objects (one per sha256, `expires`), one with a URL, delete (409 `published` / `in_use`) |
| `POST /jobs/claim` | executor | long poll, 25 s; `platform: true` is Hanzo's executor, SuperAdmin only, audited `train.claim` |
| `POST /jobs/{id}/events`, `POST /jobs/{id}/artifacts` | executor | report under the lease; register an output. A Hanzo task answers only the platform credential acting in the job's org (`X-Org-Id`) |
| `GET /v1/train/published` (`?revision=`) | SuperAdmin | every org's published capabilities for the decision service; 503 for an org it cannot read or does not own; in `openapi/operator.go`'s operated set |
| plane `POST /train/capabilities` (`train_capabilities`) | the plane's org | the org's published capabilities as one JSON array (`Capabilities.Set`, `[]` for none), the `X-Org-Capabilities` ai attaches to each of its decisions |

### The executor wire (what `train serve` speaks today)

- **Claim** `{machine, platform, bases, devices: [one entry per device], supports: {adaptations, protect, terms, outputs}}` → 204, or `{org, task: {id, job, role, lease, coordinator, device, devices, held, report: 20}, job}`. An org's executor gets tasks placed on its machine; the platform claim gets the oldest open lead of any org's job naming no machines, and is refused 503 when the deployment has no audit recorder. `device` is the first kind in the claim's order the job allows, `devices` how many the claim listed (a platform claim halves it down to 1 until the payer holds a window). The lease is answered once, stored as sha256.
- **Report** `{task, lease, events}`, at least every `report` (20) seconds — `train serve`'s heartbeat. The cloud meters the task by the clock: `used = min(devices × (now − claimed), held)`, charged the increase; a resend at the same instant adds nothing. The executor reports no seconds. Events: `status` (lead running/evaluating/failed/stopped; join done/failed/stopped), `coordinator`, `metric`, `log`, `artifact`, `result`; ≤ 500. Answer `{cancel, held, status, seq}`. Once the job succeeded, was rejected, cancelled or stopped, a report records nothing and answers the status; a failed job or another lease is 409 `lease_lost`.
- **Stop is cancel.** When the next window cannot be held the task runs what is held, asking again at every report; once less than `stopBelow` (40 s) × devices is left the job ends `stopped` with the reason as its `error` (`budget`, `max_seconds`, `insufficient_balance`, `spend_cap`, `balance_unavailable`, `no_payer`) and the answer says `cancel: true` — `train serve` kills its trainer on cancel or on `lease_lost` (kai `train/src/serve/api.rs` `Report::send`, `watch.rs` `Watch::wait`). Nothing is checkpointed on a stop.
- A lease silent for `TRAIN_LEASE_TTL` (600 s; any report or registration keeps it) is lost: a lost lead fails the job `executor_lost`.
- `train serve` does not yet send `platform: true` (kai `train/src/serve/mod.rs` `run`), so Hanzo's executor needs that one field before it claims a platform job; everything else above it already speaks.

### Money flow

1. **Create** (`jobs.go`): a job naming no machines needs a `resources.budget` (`supported`, 400 `budget_required`), a billable `cloud.PayerOf` wallet (else 403) and `afford(first(spec))` — one device's window alone, 300 s clipped to `max_seconds`, at most `budget`, at the opening class — → `cloud.Denied` → 402 `insufficient_balance` / `spend_cap_exceeded`, 503 `balance_unavailable` (the group carries `cloud.DenyEnvelope`). The payer and whether its project is claim-bound (`jobs.validated`) are stored on the job, so every window weighs a project's cap as hard as the create did.

2. **Claim** (`takePlatform`): rate = `cloud.RateMicros("train", "<base>-<gpu|cpu>-hour", floor)`; floors $3.48/GPU-hour (`gpuHourFloor`, the catalog's GPU Basic, pinned by `TestTheComputeFloorIsTheCatalogs`) and `cloud.RuntimeHourMicros` for cpu. `extend` holds `windowSeconds` (300) × devices, clipped by `max_seconds` (device-seconds) and `budget` (cents, via `affordable`), then `afford(holding(payer) + window cost, validated)`. `holding` sums, over every org store this process holds, the payer's live holds (held − used) and its outbox — one wallet pays in several orgs (a SuperAdmin's own books). The payer's lock (`locks`) is held from the first weighing to `take`, and around `extend`→`hold` in a report, so two claims or reports cannot weigh one balance. Refused while the payer's other work holds money (`busy`) or unreadable → the job waits for a later claim; refused outright → it ends `stopped` before it runs. `take` stores class/devices/rate/held/claimed.

3. **Report** (`use`, then `apply` → `window`, then `settle`): `use` meters `used' = min(devices × (now − claimed), held)`; the charge is `cost(rate, used') − cost(rate, used)` (`cloud.RuntimeCost`), a `metering.Usage{AmountMicros, Model: kai-gpu-hour, Ref: trc_<task>_<from>_<to>}` written to the `charges` outbox in the same transaction as the used CAS. `settle` ships the file, then sends every outbox row to `b.Bill.Record` and deletes them; the ledger dedups on `Ref`, so a row sent twice (a process that died before the delete shipped) moves money once. A report whose file does not ship answers 503 and keeps its charge for the next ship — any later report, the terminal answer to the retry, or the hourly sweep. When held − used < `extendBelow` (180 s) × devices the next window is asked, again at every report until one is held. The seconds are charged even when an event in the same report is refused. A claim whose take does not ship gives the task back (`giveBack`). Refused or unreadable → the task runs what is held; below `stopBelow` (40 s) × devices `end(stopped, reason)` and the answer says cancel. A report late past `held` is charged `held`; a later window then covers the gap, since `used` is always the clock's.

4. **End**: `end`/`finish`/`expire` close the tasks, so `outstanding` (held − used of claimed priced tasks) drops: failed/cancelled/rejected/succeeded/stopped release the remainder; a crash is charged through its last report and released at lease lapse; a cancelled task's seconds after its last report are never charged.

5. **Why not `finance.Holder`**: a ledger hold is paid only by a payment (`Draw`, `KindPayment`), which is not metered usage — it fires no usage hook and counts toward no spend cap. Training compute is usage, so a window is held the way `cloud.Meter.Reserve` holds an in-flight call (commit, then `Authorize` the sum), kept in `train.db` because a job outlives a process. The cost: another surface's spend does not see a train hold, so a wallet can be overrun by at most what its running tasks hold (`outstanding`) at the moment another surface spends it.

6. **The org's own machines**: rate 0, never priced; `max_seconds` still stops them by the clock.

Compute gating is `b.Bill.Authorize` (balance + spend caps). Plans include AI as limits only (`apps/ai/limits`); nothing includes training compute, so there is no allowance leg here.

### Store, keys, retention

Grants (`objects.go`) bind checksum, length, content type (`application/x-tar` outputs, `application/jsonl` datasets) and SSE-KMS under `train-<org>`; over 64 MiB one presigned PUT per part of a multipart upload opened sealed. `verify` refuses an object not sealed under the org's key (`errPlain` → 409 `artifact_mismatch`, object deleted); a stored object records the key it is sealed under (`objects.seal`). Objects `reshape` carried over from the release that stored them unencrypted have `seal` '', so `holds` does not count them and `admit` answers a fresh sealed grant rather than `stored: true`; its read-back sets the seal. `grantTTL` is 2 h because every part is signed at grant time (64 GiB at ~80 Mbit/s); HIP-1333 §6/§9 say so. The S3 deployment must run its `lux` KMS provider against the KMS this cloud writes (`kms.lux.path` `/s3`, `kms.lux.env` `prod`); without it every sealed PUT fails and nothing is stored unsealed. Retention: `TRAIN_RETENTION_DAYS` (30; 0 keeps all) deletes objects that are unpublished and read by no job that has not ended, each on the audit trail first (`train.artifact.retain`). "Read" is the `read` predicate: `dataset.uri` = `artifact:<sha>`, `adaptation.basis`, `from`, or an element of `protect.capabilities` / `adaptation.sources`, each compared whole — not a substring of the spec; DELETE's 409 `in_use` is the same predicate. A deployment with no audit recorder deletes nothing on its own and refuses platform claims.

### Publishing and the published list

Publish needs a succeeded job (rejected + `force`), a stored output of the spec's kind, and for a capability a route and a result naming `base.sha256`; `form` is `head` for `readout`, else `full`. The clash check runs inside the publish transaction. `GET /published` lists `{org, name, sha256, base_sha256, form, route, published, url, expires}` sorted by org then name with `revision` (sha256 of the list without URLs, 32 hex); `?revision=<current>` answers `changed: false` and signs nothing. An org store it cannot read, or holds and does not own, is 503, never an omission.

### Limits as built

- `stores.Each` sees the orgs whose files this process holds: on a sharded deployment (`CLOUD_PEERS` > 1) the platform claim, a payer's `holding` and `GET /published` see the serving writer's orgs only, and the payer lock is per process. One writer sees all.
- Each platform claim poll runs `expire` + `unplaced` over every pending org once a second.
- Red review (3748beb5e) still open: an org's executor is trusted with its org's verdicts. Events are kept as their type's own fields (`report.body`; a result whole, ≤ 1 MiB), and a job keeps at most `maxNotes` (100,000) logs and metrics, the first ones; statuses, coordinators, artifacts and results always land.
- Hanzo's executor (`train serve --platform`) is not deployed in universe, so a job naming no machines queues until one claims it; nothing is held or charged before a claim.
- The clock meters only at reports. An executor that ignores `cancel`, or reports late, runs past `held` on Hanzo's devices uncharged; `train serve` reports every 20 s and kills its trainer on cancel or `lease_lost`.

### Tests

`money_test.go` (budget and max_seconds cancel the job inside what is held, charged exactly the metered seconds, and a report after the stop is told cancel and charged nothing; each report metered by the clock on every device, a resend adds nothing, a late report is charged what is held; a balance spent mid-job stops it inside the hold; an unreadable balance runs the hold down; a crash charged through its last report and released at lapse; failed/rejected/cancelled release; create 402/503; the first window sized by the job's own bounds; a refused event still charges its seconds; one device when the wallet holds one; a job whose payer's money is held waits and then runs; the platform claim is SuperAdmin's and audited; device matching; every stored object by the GB-month; catalog pins), `objects_test.go` (dataset upload sealed and read back, SSE refusal, get/delete, retention + audit, lapsed uploads and the pending ceiling, no audit no deletion, unsealed bytes uploaded again sealed, key minted once), `publish_test.go` (route validation, conflicts, the list's revision), `red_test.go` (the adversarial review's findings: running and rejected outputs billed, exact "in use", lapsed uploads, one payer's claims serialized, a project's hard cap kept, the outbox, legacy unsealed bytes, claims refused without an audit trail, one wallet across two orgs), `train_test.go`, `spec_test.go`.

## Capabilities (`apps/train`, `/v1/capabilities`): teach Kai from mistakes without retraining Kai

    POST   /v1/decisions/:id/corrections        {question, answer, note?, cohort?} -> 201 Correction
    GET    /v1/decisions/:id/corrections        {data: Correction[]}
    DELETE /v1/decisions/:id/corrections/:cid   204
    GET    /v1/capabilities/corrections?question=   {data: Correction[], questions: [{key, type, labels, corrections}]}
    GET|PUT /v1/capabilities/capture            {on, retention_days}; the first correction turns it on
    GET    /v1/capabilities                     {data: Capability[]}
    POST   /v1/capabilities                     {name, corrections: [id]} -> 201 Capability (draft)
    GET    /v1/capabilities/:name               Capability with versions and live
    DELETE /v1/capabilities/:name               204; 409 while installed
    POST   /v1/capabilities/:name/train         {corrections?} -> Capability, a new immutable version
    POST   /v1/capabilities/:name/publish       {version?} -> installed (in the org's published set)
    POST   /v1/capabilities/:name/disable       -> disabled (out of the set)
    POST   /v1/capabilities/:name/rollback      -> the previous installed version back in the set
- **Evaluation**, in the shared `/evals` vocabulary: `{base: {sha256}, candidate: {form, rank,
  sha256, bytes}, route: {key, type, labels}, held, rows: {target, preserve, other}, repairs,
  damage, fire_rate, specificity, ood: {rows, moved, preserved}, latency_us, memory_bytes,
  cost_micro_usd, arms: [{arm: base|everywhere|routed, repairs, damage, fire_rate, utility}], gate:
  [{check: repairs|damage|ood|routing|cost, passed, detail}], promotion: publishable|rejected,
  tried}`. The UI's four columns: fixed = repairs, broken = damage, fires = fire_rate, cost =
  latency and memory. `publishable` means the gate passed: repairs on held-out rows, damage bounded,
  every other answer bit-exact base (`ood.moved` 0), routed at least as good as applied everywhere,
  cost bounded.
- **Live** `{version, fires, repairs, damage, neutral, repair_rate, damage_rate, last_fired}`.

How it is built (train owns it: one store, one bucket, one org key, one published set; a store has
one owner, so `/v1/capabilities` is train's and `openapi/misfiled.txt` carries
`/v1/capabilities train`):

| file | what it owns |
|---|---|
| `capabilities.go` | the routes, `Capability`/`Version`/`Live`, the state machine, `install` (the passing version over the base Kai serves now, route and name checked against published jobs, its tar copied to the serving store by `objects.serve`, as a published head's is), disable, rollback (the installed pointer back one), the capture switch, the org's corrections list, `trail` (every action on the audit trail as its person) |
| `captures.go` | the plane ops ai calls (`client/train.go`: `train_capturing`, `train_capture`, `train_correct`, `train_corrections`, `train_uncorrect`); a capture's bytes at `<org>/captures/<decision>` sealed under `train-<org>`, then its rows; `watch` (an installed version over another base than a capture's is dropped from the set, `needs_validation`; one that fired is `active`); corrections (the answer one of the question's options and not what the person was answered: a capability's label where one answered, so naming Kai's own over it is a damage report) |
| `lesson.go` | a lesson: the captures over the newest capture's base, every target of the selected corrections, the newest `keptRows` (2,048) of the question Kai answered uncorrected (a row a capability changed and no one corrected teaches nothing), `otherRows` (4,096) of other questions; read 16 at a time; `POST {KAI_URL}/v1/teach`; the tar kept as an object (`objects`, so retention and storage see it; every version's artifact counts as published); `cost_micro_usd` the lesson's seconds at `train/kai-cpu-hour` |
| `capstore.go` | tables `settings`, `captures` (one row a decision and question: base label, sha256, the capability and version that answered, fired, changed, answered label), `corrections` (one a decision and question), `capabilities` (`installed`, `since`, `history` of installed versions), `versions` (immutable); `installs`, which `capabilitiesOf` (publish.go) lists beside published jobs: one read for `GET /v1/train/published` and the plane op `train_capabilities`, the set ai attaches to each of the org's decisions (`X-Org-Capabilities`) |

## The Enso policy registry (`apps/train`, `/v1/train/policies`): what each Enso tier maps to

An Enso policy is an `enso.policy/1` file the engine's tuner writes (hanzo-inc/engine `retrain`
bin `tune`, `policy::cascade::PolicyFile`): per benchmark the pool, the sealed lockbox, the best
single arm held out, and per tier a cascade with its budget ($ per 1k questions) and its
in-sample, held-out (new questions) and lockbox (sealed set) scores. The registry keeps each file
as an immutable version, pinned by the sha256 of its canonical bytes (keys sorted, numbers as
written, `canonical` in `policy.go`), and serves one of them. SuperAdmin only, in the reserved
`platform` namespace's train.db (`polSchema`: `policy`, `policy_version`, `policy_event`).

    GET  /v1/train/policies/:name                    serving, versions (newest first), history
    POST /v1/train/policies/:name                    {document, note} -> 201 draft; 409 exists
    GET  /v1/train/policies/:name/versions/:version  file, servable+reasons, chains, catalog diff
    GET  /v1/train/policies/:name/diff?from=&to=     sources, models, per bench pool/best single/tiers
    POST /v1/train/policies/:name/promote            {version, base, note}; 409 moved|promoted|not_servable|conflict
    POST /v1/train/policies/:name/rollback           {base, note}; 409 moved|nothing_promoted|not_servable
- **Live Enso reads the promoted version by hot reload.** Promotion compiles the version onto the
  Enso catalog as it serves and applies it through the ai process's one write path (plane
  `ai_routing_apply` → hanzoai/ai `RoutingApply` → engine `PUT /v1/admin/catalog`), which records
  a routing version; ai's minute convergence keeps serving it across the engine's restarts on the
  same deployment (`boot`), and a new release of `charts/app/files/enso/catalog.yaml` is left
  standing (`redeployed`) until it is promoted again. No pod rolls.
- **What a promotion serves** (`realize`): each of the version's tiers leads with its arm and the
  rest of the route follows in the order it had before any policy; every other model a policy set
  goes back to that route. Serving is a function of the routes before any policy (`origin`,
  recorded the first time a policy sets a model) and the version on top. Rollback pops the stack;
  past the first promotion every tier goes back to `origin` and `origin` is cleared.
- **Audited first, no trail no write**: `train.policy.create|promote|rollback` with before and
  after, and an `error` record beside a promotion the catalog refused. A catalog the ai process
  cannot read is said in `serving.error`; every version then reads unservable, nothing promotes.
- The ai plane ops (`apps/ai/routing_rpc.go`): `ai_routing` (a family's catalog, newest version,
  `drift`, `redeployed`) and `ai_routing_apply` (`dry` proposes: diff and the guard's refusal),
  both `principal.SuperFrom` alone; the version's actor is `owner/name`.
- admin.hanzo.ai `/enso` (hanzoai/admin `pages/Enso.tsx`) is the surface; nothing public names a
  pool member, a route or the mechanism.
