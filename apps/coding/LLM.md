# LLM.md — apps/coding

Guidance for apps/coding. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Module hygiene & commit shape

- **1,000-line ceiling (`filesize_test.go`).** Every handwritten `.go` file in `apps/coding/` and `plugin/agent/` must stay at or under 1,000 lines (`TestModuleFilesUnder1000Lines`). Split growing files by responsibility (`sandbox_prompts.go`, `sandbox_workspace.go`, `sandbox_turn.go`, `sandbox_settle.go`, `sandbox_git.go`, `coding_run.go`, `coding_lifecycle.go`, `coding_proposal.go`, `park_wake.go`, `start_context.go`) instead of growing monoliths.
- **Atomic commits & resumable sandboxes (`deliverLine`, `pushCommitted`).** Coding agents are instructed to make small, atomic local `git commit`s (`<type>(<scope>): <summary>`) continuously as each verified unit of work completes, while leaving `git push` and PR creation to the platform. When a turn crashes mid-flight after working, the runner keeps the warm sandbox alive for `Resume` / follow-ups and pushes only clean commits the agent already explicitly made — never dumping half-edited uncommitted files into a dirty crash commit.

## Coding runs: which repository, a loop, and a done with nothing in it (`apps/coding`)

| value | runs on | fresh? |
|---|---|---|
| `cloud`, `hanzo/cloud`, a URL ending `hanzo/cloud` | GitHub `hanzo-inc/cloud` (repo.go `homes`, `home`) | live, what production builds from |
| `universe` | GitHub `hanzo-inc/universe` | live |
| `github.com/<owner>/<name>` | that GitHub repository, admitted by GitHub | live |
| `hanzoai/cloud`, `git.hanzo.ai/hanzoai/cloud` | the forge namespace `hanzoai` — never mapped: hanzoai/cloud is the open-source edition | see below |
| any other bare name (`docs`, `mcp`) | forge `hanzoai/<name>`; a pull mirror clones GitHub's origin | live when mirrored |

`homes` maps only a bare name or one under the org's exact name. A run never reads `/v1/git` (Hanzo
Git, S3-backed), a separate store. The forge's `hanzoai/cloud` held one README ("created for a Hanzo
agent run", an early `Ensure`); `vacant` refuses an estate repository that is empty or carries that
description, and the run then asks GitHub for `hanzoai/<name>`.

**A follow-up** (`after`) resumes the earlier run's sandbox only when its repository tag equals this
run's; otherwise it clones fresh, so one repository's tree is never pushed to another.

**The clone** (`cloneArgv`) is `--depth 1 --single-branch` with NO blob filter and three settings
written into the checkout's own configuration (`tuned`: `core.untrackedCache`, `gc.auto=0`,
`maintenance.auto=false`). A run checks out every file, so a filter only splits the download into a
second fetch and a second discovery (5 HTTP exchanges for 3; 14.4 s against 10.2 s on a 2.5 GB
mirror). `core.fsync` stays at git's default: syncing loose objects costs one fsync per file staged.
`checkout.workers` is not set; it helps only where each directory call costs ~100 us, which is the
volume mount's to decide. Measurements and method: hanzoai/vfs `docs/git.md`, `tools/gitbench`.

**Loops.** dev 0.6.94 (sandbox 1.3.4) ends a turn when its model makes the same call twice with the
same result (`repeated_tool_cycle.rs`). The runner resumes the conversation of the turn's FIRST
`thread.started` (`dev exec … resume -- <thread> <nudge>`) and requires the resumed turn to name the
same thread (0.6.94 silently starts a new one for an unknown id). The nudge carries the call and its
shape hints, never what it printed. Second loop: the next tier (`tiers`), only when the request named
no model; third, or no tier: fail naming the call. A loop is checked before a 402, and a 402 fallback
thinks with `codingFallback` from then on. Root causes fixed in argv: `--include-plan-tool` (dev's
prompt promises `update_plan`), `shellLine` (`shell` takes argv), `deliverLine` (no `git push`/`gh`).
`/.code/agents/` and `/.code/users/` (dev's scratch) are excluded from the commit and the artifacts.

**Hollow done.** A build that changed and kept nothing fails (`hollow`) when its clone held no file but
a README, licence, notice or git setting by whole name (`Empty`; not in a made workspace, where a new
project begins as one README) or its harness ended with no last message (`Unanswered`). A dev build
whose commit found nothing changed is first asked once more in its own conversation (`settle`, #451):
make the change, or answer "No change needed: …". One that does neither is `Unacted` and fails with
what it said last; a turn that ended on "Let me create the spec document" had a last message and was
read as done. deliver's no-change step says "the repository is unchanged", a measurement, not a verdict.

**Work is kept (#450).** `.git/info/exclude` gets dev's scratch and the package caches an install leaves
in the tree (`unkept`: .pnpm-store/, node_modules/, .npm/, .cache/, __pycache__/, .venv/), each line
once; a committed pnpm store was a push GitHub refused (a 127 MB file) and an HTTP 408 before that. A
staged file over 100 MiB is unstaged, excluded and named (`oversize`). The harness gets the run's ttl,
or less so keepWait and a minute are left before the run's deadline (`working`, recomputed per call);
a call that ran out of time (`outOfTime`) has its task stopped and its work kept on the branch
(`keep`), and the run fails saying where. A failure with pushed work carries changed/branch/commit
in its last status (`fail`), so the app says it was kept and a follow-up starts from it.

**At capacity.** org hanzo is a held org: 2 runs per person, 4 shared by every held org. A full pool
queues the run (202); 429 means the queue is full too, names the bound, and carries `retry_after`.

**A request with an id happens once, and a word to a run is never lost (`apps/coding` `ledger.go`,
`inbox.go`, `idempotency.go`).** `requestId` on start, steer, extend, pull and merge (and the
`Idempotency-Key` header, bound to the same field by zip's `header:` tag, winning when both are sent)
runs the op once across every pod and every restart. The record is the `requests` store (system
namespace, app `agent`, `locus.Use`, both engines; in `manifest.SQLStores` and, as `ledger.go` and
`inbox.go`, in `portedDirs`). It replaced two process-local maps (24 h, 4096 entries), which ran a start
twice, real money, whenever a retry landed on another pod or after a deploy.

- **A request is a row** keyed (org, subject, scope, id); scope names the op and the run (`start`,
  `steer/<session>`, `extend/…`, `pull/…`, `merge/…`). The claim is `INSERT … ON CONFLICT DO UPDATE …
  WHERE expires <= now`: one insert wins, and an expired row the sweep has not reached is an absent one.
  State is `claimed` then `done`, with the outcome as JSON (`Accepted` for a start), the session, a hash
  of the request's body (`print`), and times in unix ms. A failure is not remembered: the claim is
  released and the next attempt runs.
- **An id is the same request only with the same body.** The claim stores `printOf` the body, hashed; a
  duplicate whose hash differs is refused 422 naming the id, whether the first is done or still running.
  A start is hashed as `normalized` reads it (`task` folded into `prompt`, `instructions` into
  `developerInstructions`, text trimmed and cut where start cuts it, the branch written `agent/<name>`,
  mode/model/effort as the run carries them; not the person's settings, which can change between a request
  and its retry), so a retry spelled another way is a retry. A base given and a base left to default from
  `after` are two requests (start reads them differently), so `after` is not folded into the base. The fingerprint says the version of its recipe (`v1:<hash>`,
  `printVersion`): raise it when the recipe changes, and a request settled under an older one (no version
  is the oldest) matches its retry on the request id alone (`samePrint`), so a deploy that changes the
  recipe cannot 422 a retry of a settled request. The same version still refuses another body. A steer is hashed
  as session and trimmed text. A reused `Idempotency-Key` therefore cannot answer
  another start's session. An id longer than 256 bytes is a 400, never cut (a cut merges ids that differ
  late and can split a rune), and one that cannot be kept (a credential that names no person, an op that
  names no run) is a 400, never dropped.
- **A duplicate of a done request answers the stored outcome. A duplicate of a claimed one** subscribes to
  the request's channel on the coord bus (a hash of the key), looks again, and sleeps until it is told.
  The bus does not deliver a pod its own message, so `tell` wakes this pod's waiters directly and
  publishes for the others. It looks at the claimer twice at most, because a claimer can die without
  telling anyone: once when the claim has stood the bound (`claimBound`, 30 s), by asking the claimer's own
  process, and once at `claimCeiling` (15 min), when it is taken over whatever the claimer says. There is
  no recheck loop; the only timer is the sweep.
- **Takeover.** The question is `CallPod` on the claimer's coord `Owner()` (a nonce, so a restarted process
  answers no), `coding.alive`: a restarted process says no, a gone pod says nothing, one that cannot be
  asked is not judged before the ceiling. Of the duplicates that judge it abandoned exactly one wins the
  `UPDATE … WHERE owner = old AND claimed = old`.
- **The fence, per op.** The claim is only the right to run; what makes an effect happen once is recorded
  where the effect is made.
  - *start*: the outcome is settled before the run is spawned (`claim.Settle` in `startSession`, after the
    session is tagged). A claimer that was retaken (slow, not dead) learns it there, withdraws its
    credential, closes its session with the reason and answers 409. The window left is a claimer that dies
    between settling and spawning: its retry answers a session the orphan sweep resumes or ends.
  - *steer*: the instruction is a row in the inbox, unique by request id, before it is applied; the flight
    hands rows to the run (below). A replay after the claimer died finds the row and hands nothing.
  - *extend*: the pod that flies the run adds the time under a claim of its own (`extended/<session>`,
    `flight.extended`), so a replay asked of it answers what it answered and adds nothing.
  - *pull* and *merge*: the claimer checks its claim is still its own just before the host call
    (`claim.Fence`, `fenced`), so a retaken claimer makes nothing. A claimer that dies after the host call
    is replayed into the host's own idempotence: the branch's pull request is open, and a merged one
    answers as it is (`TestARetakenClaimerOpensAndMergesNothing`). The pull request's work item in the task
    app is filed under the request (`AgentPRIn.Request`, `issues.request`, a partial unique index per
    project; `Store.CreateIssueOnce`), so a replay after the claimer died between opening the proposal and
    recording it is answered the row already filed and files no second.
- **Prune.** `coding:requests`, a `Lead` loop (`Keep`), deletes expired requests and steers. A request id
  with no ledger kept (`coding.Keep` not called) is 503, never run on a guess; no id runs as asked.
- **The inbox** (`steers`, same store: seq, org, session, subject, request, body, state `queued` /
  `delivered`, times). Where a ledger is kept EVERY instruction for a run is a row first, flown or not, and
  the flight hands rows to the run by seq, so order is the inbox's: a direct steer cannot overtake a
  backlog (while a read is in progress, a new one is queued behind it and answers `queued`). A row is
  delivered only when the run has ALL of its text: the turn took it; or the run it rides in the task of
  has been spawned with it; or the session recorded it for a resume. A task is cut at `maxPromptLen`
  (32 KiB) by Run, so what is joined to one goes in whole and in order, each row that still fits (`fit`,
  one pass with a running total), and one that does not is left queued without holding up the small rows
  behind it: none is cut mid-text and then marked. **A steer is accepted only if it can reach the run.** A
  run whose harness has live turns (only `dev`, `flight.live`) takes an instruction of any size, so one
  that fits no task beside the run's own waits for a live turn instead of stopping the run for a follow-up
  that could not carry it either (`carryable`); a turn beginning (`setSteerer`) is a signal that hands it
  what waited. Any other harness is told by being stopped, and its follow-up carries the queue in one task,
  so a steer that would not fit that task after everything already queued is refused when it is said, 400
  naming `maxPromptLen` (`ledger.room`), as an oversized one is: never acked queued and left to rot. The
  message says what works: with nothing waiting, stop the run or wait for it to end and start a run with
  `after` (a start after a live run is a 409); with rows waiting, wait for the run to take them and say it
  again. A steer for a run no pod flies cannot know its harness, so it is held to the same limit against an
  empty task. A retry of a kept instruction is not counted twice. A mark
  that fails leaves the row queued and the flight remembers by seq what it already handed its run
  (`flight.handed`), so it is not handed twice. A crash between handing and marking repeats an instruction to
  the next owner, never loses one.
- **A steer for a run no pod flies** (a rolling restart, a run being resumed) is appended when the session
  says `running`, then told to the owner by push (the flight's `coding/inbox/<hash>` subscription, and
  directly on the pod that wrote it). Only a run a pod of this cloud can resume is kept for: a run on a
  machine of the person's own (`Target`) or with no sandbox (`Box`) is never flown or resumed, so it still
  answers 404 as it did and keeps nothing. A run that has ended answers **409** with its status, and a
  paused run no pod holds 409; it was 404 "nothing of yours is running there" before. No client tells the two
  apart: apps/transport treats every steer error alike. Another person's run, or a session that is not a
  coding run, is still 404. The answer's `queued` (on `CodingAck`) is true for an instruction kept for a run
  that has not taken it, absent when the run has it.
- **A resumed run** reads its inbox before it begins (`backlog`): each instruction is first recorded on the
  run's session as a user message flagged `queued`, then marked delivered, and joins the run's task. A
  second resume rebuilds the task from the session (`resumeAsk` in plugin/agent, which now reads every
  event, a page at a time, and folds the `queued` messages after the first one, in order), so it has them;
  a message a turn took in place is not folded, the dev thread holds it. One that does not fit the task
  whole stays queued and the ones behind it that fit go in; one the session could not record stays queued
  with every one after it (the order a resume rebuilds from), for the flight to hand over.
- **Size.** An instruction is at most `maxPromptLen` (32 KiB, the limit of a start's prompt). The inbox has
  no row cap: no existing constant fits, and a cap is the owner's to set; rows expire with requests (24 h).
- **A rolling update runs two versions.** An old pod knows neither table. It writes no row and reads none;
  its flights hand nothing from the inbox and its resume ignores it, and nothing it does deletes or marks a
  row. A row for a run an old pod resumes stays queued, untouched, and shows as queued on `/changes`; the
  first pod of this version to fly or follow up that run hands it over (`TestAPodThatKeepsNoLedgerLeavesTheInboxAlone`).
  An old pod's flight still answers `fleetFollow` as it did, so a steer a new pod asks of it is delivered
  directly. An instruction can therefore be delayed by an old pod, never lost or doubled by it.
- `/changes` lists the inbox (`steers`: queued or delivered, a claimed one still as queued) beside the
  commits.
- **resumeAt is the authority's** (`clearsAt`): the allowance app's reset for the person (`allowance_read`:
  a session's end, the UTC hour or day) when it says spent, and the free pool's refill (`ai_pool`); the
  later holds. Where neither names a time (a payer's refusal, a vendor 429, the per-minute rate, an
  authority that cannot be asked or names a past instant) there is none, and none is invented. A time
  beyond the age below is not named either (`clearsAt`).
- **The wake is a timer, never a ticker.** `fleet.alarm` is one `time.AfterFunc` armed for the earliest
  `resume_at`: from the rows when the process starts (`watch`, in `Keep`: it LISTENS FIRST and reads the
  rows after, so a park told in between is not missed), when a run parks (the pod sets its own and tells
  `coding/park` on the coord bus), and after each pass. `fire` reads the rows due and the pod that leads
  `coding:wake` tries them; a pod that is not the lead while one is due asks once more after
  `coord.Handover()` (a dead lead's lease) and then no more (`alarm.again`): a lead that is alive keeps
  what it could not try for a later time, and the pod that finds the lead gone is the lead. The process
  that holds the timer and the subscription is pinned: the `agent` row is `Eager` (as `auto` is, for
  the same reason: it owns a clock), because `Turn` pins it only where the deployment pins the turn
  path and a lazy child is stopped after `IdleAfter` with the timer in it
  (`TestAParkedRunsWakeLivesInAProcessThatStaysUp`).
- **Liveness.** A start that cannot be made (`remain`) ends the attempt and leaves the run parked, the
  reason as it was, `tries + 1`, and a time `retryAfter(tries)` away: the ledger's own `claimBound`
  (30 s) doubling to `claimCeiling` (15 m), no figure of its own. A try that could not be made at all
  (IAM, the audit trail, the ledger or the sessions store could not answer: `later`) keeps its attempt
  and counts the failure the same way, armed on the lead and told to the others. A run waiting out such
  a delay is not hurried by a change of funds (it waits on a start, not on funds); a person's Continue
  goes on at once. Nothing retries on a fixed clock; the delay is the only timer. A panic in a try
  (`survive`) is a try that could not be made, kept the same way, so a park that makes a try panic
  cannot crash-loop the process: the row survives a restart, the panic would not. A lease that fails
  for a reason that is nobody's refusal (a 503, a gateway error, a timeout: `refusedReason`) is a
  failed start and not a refusal: the run is parked AS IT WAS PARKED (`Req.Reason`), for the next
  attempt's time (`Parking.Retry`, `Parked.Tries`) with the failure in the event's `note`; only a 402
  and the prepaid gate's spent cap (`unpayable`) are admission refusals that name money. A lease's 429 is
  a usage limit only where the authority that makes them says one stands (`standsFor`): the sandbox
  app answers 429 for the org's running-sandbox quota, which no credits and no reopening window clear,
  and that takes the retry path. Every goroutine that makes tries recovers (`reconsider` backs off the
  parks it had read, as `survive` does for one; `fire` arms itself again after a delay; the pass
  goroutine of `askPass` frees its key), so no panic is the process's. A process with no audit trail does
  not park at all (`parkRun` answers `errNoTrail`, `Dispatcher.parked` fails the run, its work on its
  branch): a park is a promise that the run goes on, and every try there is refused.
- **Funds** (`internal/funds`, `billing/funds` on the coord bus; a listener on the telling coord is
  called directly). Told ONCE where the change is made: `finance.Deposit` (credit, the one place a
  balance rises, via `SetDepositHook`), `planeSubscribe`/`planeSubscriptionRecord`/
  `planeSubscriptionReactivate` (plan), `writeLimit`/`dropLimit` (cap; an org limit names its org, a plan
  limit its plan). An event names whom it is about (`Event.Names`): one that names nobody is not told
  (`Tell`) and not heard (`funded`). `reconsider` tries the parked runs of the org, or of each org
  whose payer is on the plan (`onPlan`: `billing_tier`), one org at a time on a pod (`serial`); the
  gap callback of the bus (events may have been missed) is the one place every org is looked at.
  Not wired: the usage-policy flags (`ai_*`), which a pod reads through a TTL cache the event would race.
- **A try** is a request in the ledger (`proceed`: scope `park/<session>`, id the park's attempt), so of
  any number of pods firing one wake, funds on two pods, and a person's Continue, one makes it. ONE
  ATTEMPT IS ONE CLAIM, and the attempt number advances only when an attempt FINISHES: a run launched
  drops its park; a start that failed, or a refusal that still stands, settles the claim with that
  outcome (`attempted{Failed|Held}`, answered to every duplicate as `resolved`, with no start of its
  own) and writes the next attempt on the park (`retime` with `next`); a try that was never made
  (an error before the start) releases the claim and the attempt stands. `relaunch` first asks, in the
  claim: the session is still parked; IAM (`mayAct`: `client.IAMRoles` `Member`, as automations ask
  before each run) says the person exists, is not disabled and is in the org, else the run is ended,
  `access removed` (`agents.StopSession`), its park dropped and a denial audited (`revoked`); an
  unreadable IAM is not a no; for a try the clock or funds make, the authority that refused the run
  (`standsFor`: the allowance app and pool for a limit, the prepaid gate `finance_authorize` for a
  payer) is asked whether it still does, and a refusal that stands ends the attempt there, with no
  lease and no turn (Continue is not asked; an unreadable authority is not "cleared"); then the try is
  AUDITED (`coding.park.resume`, as the person, with the trigger `wake|funds|continue` and the
  attempt) before it is made, and a process with no trail makes none (`Keep` is given the request
  path's recorder). The payer is whatever `principal.PayerFrom` resolves from the person with no
  billing claim, as a pre-claim credential's is (org pool outside the signup org, the person's wallet in
  it); the claim of the request that parked the run is not kept. `relaunch` goes on in the SAME session through
  `startSession(existing, parked)`: `Unpark` (one UPDATE, won by one caller, restarts the session's
  clock and meter from now) right before the spawn, admission exactly as any start, `req.From` set to
  the branch so the clone carries it, a fresh sandbox, the task as it was asked plus `continuingLine`,
  the inbox read by `backlog` (a steer said to a parked run is queued by `parkedWord`, found by session
  or thread, never refused, and rides the relaunch once), the thread message edited in place
  (`spec.TS`), a chat turn that followed the run ended when it parked so the run narrates itself.
  A run that adds nothing more is done with the work it kept (`Req.Kept`). Refused again before its
  harness began (`admission`: the pre-flight or the lease) or on its first turn, it parks again with the
  new reason and time; a start that cannot be made leaves it parked and tried again (`remain`).
- **A client that waits** (`agents_turn_events`, `waitTurn`) is told a parked run has NOT ended: no
  `outcome`, and `park {reason, resumeAt}` (`client.TurnPark`) beside the events; a run a person paused
  still answers `paused`. A waiter should keep waiting as long as it will and say why the run stands
  still; it cannot know when credits are added, and `resumeAt` 0 means "when credits are added or a
  person presses Continue". An automation's agent step (`apps/auto`) waits until its own ceiling and
  then stops the run, which drops the park.
- **A try is told what it is, and answered as one.** The audit trail holds an INTENT before the start
  and its RESULT after (`success`, or `error` with the reason): a start that fails is not on the trail
  as a success. A person's Continue is answered 403 (the person may not act, "access removed"), 409 (the
  start failed; the reason is in the text and the run stays parked) or 503 (IAM, the trail or the ledger
  could not answer), never a bare 500 (`answered`). The claim of a relaunch settles in `ledger.run`,
  after `relaunch` returns: `launched` is given a context with no claim, so `startSession` does not
  settle early (it does for a start with a request id), and a pod killed anywhere in a try leaves a claim
  unsettled, which is retaken, finds the session running and drops the row. `proceed` that is answered
  "launched" from the ledger while the run is still parked still gives the park the next attempt and
  tries once more (`stranded`), as a guard for a settled outcome whose park survived by another road; it
  is not a window this code opens. The
  session's stored payer, which the runtime meter bills, is re-stamped from the caller on unpark
  (`Store.Unpark`), so the sandbox time of a run that goes on is billed as the person is paid today.
- **Events coalesce.** `askPass` keeps at most one pass running and one waiting per org or plan
  (`fleet.passes`), so a burst of deposits is two passes and not a goroutine per deposit.
- **Rolling update.** An old pod fails a limit-refused run as before and knows neither the `parks` table
  nor the session columns (added by `addColumns`); it never relaunches, and ignores a parked session
  (`lost` is false for paused). A parked session keeps no `box`.
- Finance tells after the settle that SUCCEEDS, whichever call it is: a deposit whose first call posted and
  failed to settle is retried by the processor, and the retry is the call that gets there
  (`TestADepositRetriedAfterAFailedSettleTells`). A listener that hears it twice finds nothing new, because
  the authority is asked before anything is leased. The same hook (`finance.told`, a goroutine that
  recovers, so it can neither fail nor delay a movement of money) is told by every call that puts money in
  a wallet: `Refund`, `Restore` (a clawback the processor reversed) and `Return` (a hold given back, when it
  returned something), each also on a replay that settles, as `Deposit` is; and by `SetLimit` when it RAISES
  an org's credit line (a line lowered or written again clears nothing). `Reverse` is not one of them: it
  posts the mirror of a deposit, which takes money out. It announces only; no cap, price or plan term is
  touched.
- Pinned by `park_test.go`, `park_wake_test.go`, `park_authority_test.go` (both engines, both buses, two
  pods), `apps/agents/park_test.go`, `finance/deposit_hook_test.go`, `internal/funds`,
  `apps/commerce/funds_test.go`, `manifest/eager_test.go`, `park_review_test.go` (Red's second review),
  `finance/refund_hook_test.go`, `finance/returned_hook_test.go`, `park_pass_test.go` (Red's third),
  `plugin/agent/coding_test.go`.

## Forge native IAM credential cutover (staged)

`forge.Source` supports `CLOUD_FORGE_AUTH_MODE=auto|iam|legacy` (legacy is the
pre-cutover default). `auto` accepts the existing KMS credential only while
`GET /v1/identity-authority` explicitly answers 404; after observing native IAM
it never downgrades. `iam` never reads the legacy credential. The native source
uses `secret.WorkloadToken` to exchange the pod's projected ServiceAccount
assertion, bounds reuse by the token expiry, and verifies both the active admin
account and the explicit machine role on Forge. No client secret is introduced.

Image and artifact builds opt in separately with `CLOUD_FORGE_BUILD_AUTH=iam`
and `CLOUD_FORGE_TOKEN_IMAGE` pinned by digest to the multi-arch index of
`ghcr.io/hanzoai/forge-token` (`cmd/mint`, `Dockerfile.mint`): `cloud secret token`
and nothing else, so an init container runs on a node of either architecture. A
`forge-reader` ServiceAccount in `hanzo-build` must be
registered in native IAM. Only the credential init container mounts its projected
assertion. The solve receives a read-only JWT file on tmpfs; admission verifies
Forge's distinct `reader` role before writing it. Customer builds receive neither
identity. No Secrets API write permission is added. Existing legacy Secrets are
not referenced on the native build path.

Do not activate these flags or roll the IAM-only Forge image before GitOps
registers the exact workload identities, their linked active Forge accounts,
issuer/audience projection and read-only account admission, and verifies a fresh
native build. IAM registrations and deployment policy remain reviewed in universe.

## A run's pull request: draft, assignees, reviewers, and its title (2026-11-16)

`POST /v1/agent/coding` (with `pull: true`) and `POST /v1/agent/coding/{session}/pull` take
`draft`, `assignees` and `reviewers`. Logins are checked against GitHub's login shape
(`people`, max 10 each) and refused with a 400 before anything is opened. On GitHub, the PR
opens first, then `POST issues/{n}/assignees` and `POST pulls/{n}/requested_reviewers`. If
GitHub refuses a login, the PR stays open: pull answers with `warning`, and a run's
completion records it under `prError`. A forge repository ignores assignees and reviewers,
because a forge login is not a GitHub login. Routed runs (`targetId`) don't carry the
Proposal yet.

`codingTitle` no longer prefixes `<repo>: ` onto a headline that already has a
Conventional Commits prefix (`fix:`, `feat(api):`, `docs!:`).

## A run on your own machine with no repository (2026-11-19, #779)

A run with `targetId` and no `repo` is dispatched to the machine with an empty `repo`,
`cloneUrl` and `branch`. hanzo-gen's runner (`src/runner.rs` `execute_workspace_run`) then
works in an empty directory, clones and pushes nothing, and reports `changed: false`, so
`finalizeRouted` closes the session done. Before this, `startSession` refused every such
run with "a run on your own machine needs the repo it works in", which failed every
automation (`/v1/auto`) aimed at a machine, since an automation's Dev run never names a
repo. The refusal now applies only to a machine run that asks for something only a
repository gives (`needsRepo`): `pull`, `branch`, `base`, or `issue`. The error names
the field. `Dispatcher.routed` resolves no actor or clone address for such a run.
An automation with `permissions: ask` runs in plan mode, and `checkAsk` still refuses
plan mode on a machine (its read-only guarantee is the sandbox's credential).
Test: `TestARunOnYourOwnMachineNeedsNoRepo` (machine_test.go).

## Calls are tried again, through one policy (#817)

`sdk/retry` is the one retry policy of the cloud's own calls: `Do` makes a call up to `Tries` times, waits what
the other side named (within `Hold`) or `Backoff` (doubling from `First` to `Longest`, lower half fixed, upper
half random), and stops when the context ends. `hub.doCall` (github.go) uses it for a 429, a 403 carrying
`Retry-After`, a 500, 502, 503, 504 or a failure of the connection that passes; apps/auto's start uses it for
the same on the Dev app, under the request id `auto-<run id>`. Every GitHub call here is safe to repeat; one
added that is not must not go through `doCall`. A run's model-API calls are not these: the harness retries them.

## The agent can fetch and merge: a read-only token and a treeless clone (#553)

A run on GitHub holds two tokens for its repository from the provider's installation path (`Read` set on the
second, which GitHub mints for `contents: read` only): its own, fed to the commands that reach the remote
(`gitAs`), and a read-only one (`hub.readable`, `RunRequest.ReadKey`; a hub admitted to read is its own reader).
The read token is written on stdin to `/tmp/hanzo-run/read` at the start of each turn (`sandbox.lend`) and by
`arm`; the clone names a credential helper for github.com that reads it (`readHelper`). `gitAs` resets that
helper, so a push is never offered it. It is blinded in every stream and revoked with the run's own.
With one, the clone is `--filter=tree:0` with no depth (`treeless`), so `git fetch origin main` and a merge
have a merge base, and the agent is told so (`partialLine`). Any other run (forge, ssh, a public repository) is
the depth-1 clone (`shallowLine`). Tests: `reader_test.go`.

## One issue, one branch (#820)

A run's branch is, in order: the one a resumed or parked run wrote; the one its caller named;
`BranchForIssue(issue)` (`agent/issue-464`, with a digest of another repository's name); the session's. A
follow-up's `after` branch wins over the last two. A pull request is reused only where its head is that branch
(`propose` reads back the open one when a second open is refused). Tests: `start_issue_test.go`.

## A model-API server error parks the run, and the same one five times ends it (#817)

A turn the harness ends "exceeded retry limit, last status: 500|502|503|504" (`faultOf`, named `503 service
unavailable`, the request id left off) parks like a refusal (`agents.ParkFault`): work kept on the branch, box
given back, `ParkSaid` says when it is tried again. It names no authority, so `parked` gives it its time,
`retryAfter(tries)` (the ledger's own doubling), and counts the failure on the row (`Retry`).
The count is `Fault{Print, Count}`, carried in the park's spec: it goes on while the name is the same and no turn
of the run answered since (`sandbox.answered`), and begins again otherwise. The `faultCap`th (5) in a row ends the
run instead (`parked`): failed, work kept, and its last status carries `reason: "fault_exhausted"`, `fingerprint`
and `faults` for whatever reads it. Tests: `park_fault_test.go`.
