# What git.hanzo.ai serves, and what apps/git serves of it

Measured 2026-11-13 from the forge's own database (`git` on hanzo-sql, read-only,
row counts) and its access log (`deploy/hanzo-git`, 3 hours, 46,693 lines). The
forge is `oci.hanzo.ai/hanzoai/git:1.26.62`, one pod, 3 containers. Counts are
exact where marked `=`, planner estimates where marked `~`.

## What the forge holds

| thing | rows |
|---|---|
| repositories | = 7,630: 4,148 pull mirrors (all 24 h interval, 1,661 private), 3,431 own (3,153 private), 1 fork; ~300 GB |
| owners | = 192 orgs; the big ones: hanzoai 3,579 repos, hanzoskill 749, luxfi 451, hanzo-inc 394, hanzo-community 312, hanzo-apps 309 |
| users | = 20: 7 local (4 admin: `z`, `zeekay`, `hanzo-cloud`, `hanzo-sync`, readers), 13 linked to Hanzo IAM |
| access tokens | = 72 |
| SSH public keys | = 4 |
| deploy keys | = 0 now (cloud mints one per coding run and deletes it) |
| OAuth apps | = 3 (git-credential-oauth, Git Credential Manager, tea) |
| webhooks | = 125, all active, all POST push+repository events to `cloud.hanzo.svc:8000/v1/provider/forge/webhook` (3 distinct URLs); 5,188 deliveries, 5,076 ok |
| Actions runs | = 16,239 over 156 repos (push 12,527; schedule 2,523; create 946; workflow_dispatch 244); 80 in the last 24 h |
| Actions tasks / runner rows | ~11,770 / 668 (ephemeral `runner-forge-cloud-*`) |
| Actions artifacts / secrets | = 1,620 / 34 |
| commit statuses | ~2,100,190 over 141 repos; 134,992 written in the last 7 days |
| protected branches | = 4,102 |
| tags published as releases | = 149,056 (no hand-made release notes) |
| issues / PRs | = 8 / 4 |
| packages | = 5 container images (cloud, ingress, app, git-runner ×2), 9.2 GB |
| LFS objects, wikis | = 0 |

## What is called, 3 hours of access log

| requests | route | caller |
|---|---|---|
| 36,066 | `POST /api/actions/ping.v1.PingService/Ping` | runners; **404 on the forge too**, tolerated |
| ~8,000 | `GET /{owner}/{repo}/{src,blame,tree,raw,commit,rss,issues,…}` | web UI, crawlers and people |
| 667 | `GET /v1/repos/{o}/{r}/actions/runs` | cloud (platform, CI board) |
| 65 / 63 / 40 / 38 / 36 / 20 | healthz / `orgs/hanzo-community/repos` / `admin/actions/jobs` / run jobs / `admin/actions/runners` / runners registration-token | cloud |
| 45 | git smart HTTP (23 upload-pack, 6 receive-pack, 16 info/refs) at `/{owner}/{repo}.git/…` | developers, mirrors, CI checkouts |
| ~20 | `GET /v1/users/z`, `/v1/repos/…`, `/v1/repos/search`, `git/commits/main`, `/v1/identity-authority` | cloud, `forge/` client |
| few | `POST /v1/runner/{register,task}`, `/v1/internal/hook/{pre,post}-receive` | runners, the forge's own git hooks |
| 3 lines | SSH | SSH has its own listener (22 → 2222) and is not in this log |

Cloud's `forge/` client calls these forge routes: repos (get, create via org,
migrate, mirror-sync, search, delete, branches, compare, contents-ext, archive,
git refs/tags/commits), orgs (create, delete, repos, teams), users (read, admin
create/delete), team membership, branch protections, deploy keys (create, list,
delete), issues (create, edit, comment, label, search), pulls (create, get, list,
reviews, merge), actions (runs, run, jobs, job logs, admin jobs, admin runners,
registration token).

## Parity table

`served` = answered by apps/git on main today. `new` = added in the PR that adds
this file. `missing` = nothing answers it.

| capability | forge evidence | apps/git | note |
|---|---|---|---|
| git fetch / clone over HTTP | 45 req / 3 h, every repo | **served** at `/v1/git/{org}/[{project}/]{repo}.git` (objects store: S3 packs, hanzo-sql index) | **missing**: the forge's address `/{owner}/{repo}.git` on host git.hanzo.ai (the root-host routes were deleted on purpose) and the owner → org/project map (`forge.Owner`) |
| git push over HTTP | 6 req / 3 h | **served**, same address; ref policy, journal, build trigger, usage metering | same address gap |
| git over SSH (`ssh.hanzo.ai` 22 → 2222) | 4 user keys; every coding run pushes by deploy key | **new**: listener (`GIT_SSH_ADDR`, host key `GIT_SSH_HOST_KEY` from KMS), keys in hanzo-sql (`POST|GET|DELETE /v1/git/keys`, user and per-repository deploy keys, read-only option), fetch and push over the objects store, same ref policy and push effects as HTTP | tested with the real git CLI on both engines; objects-store repositories only (a linked one is refused with the host's address); `sshUrl` on repo views still empty until the listener is deployed; scp-style `git@host:org/repo` works, protocol v2 is answered as v1 |
| Actions runner protocol | all 668 runners; `register`, `declare`, `task`, `state`, `log` | **served**: `/v1/runner/*`, same wire module as the forge (`modules/actions/runner`), pools declared, join secret per pool | runners only change `GIT_RUNNER_URL`; the Ping probe is 404 on both |
| workflow runs, jobs, logs, runners (read) | 667 + 38 + 40 + 36 req / 3 h by cloud | **served** natively: `/v1/git/{runs,runs/:id,runs/:id/logs,runners,pools,workflows}`, `POST /v1/git/runs` for dispatch | not the forge's `/v1/repos/{o}/{r}/actions/*` shape; cloud's own readers (`actions.go`) call the forge today and must call the store instead |
| run triggers | push 12.5k, schedule 2.5k, create 946, dispatch 244 | **partial**: push (journal, `.hanzo/workflows`), dispatch | schedule and tag-create triggers: missing |
| commit statuses | 2.1M rows, 135k / week | **new**: `POST|GET /v1/git/repos/:name/commits/:sha/statuses`, written by the run engine in the task's own transaction | one row per (commit, context): re-stating replaces, history is not kept |
| Actions artifacts | 1,620 | **missing** | |
| Actions secrets / variables | 34 secrets | **missing** (cloud has KMS) | decision: read from KMS at lease |
| pull mirrors of GitHub | 4,148 repos, 24 h cadence | **partial**: linked repositories (`link.go`) read through a cache, refresh on read (30 s window) | no scheduled background sync; the 4,148 become links, not copies |
| webhooks to cloud | 125 hooks → one cloud URL | **not needed**: a push is `cloud.OnGitPush` + `EmitLifecycle` in-process | arbitrary user webhooks: no consumer exists (3 URLs, all cloud) |
| pull requests | 4 | **served**: open, list, get, merge (fast-forward only) | no reviews, no comments |
| issues | 8 | **missing** | the board is `/v1/task`; not worth a forge issue tracker |
| branch protection | 4,102 rules | **partial**: one fixed ref policy (`refpolicy.go`: default and release branches refuse direct and force pushes) | per-branch rules: missing |
| tags / releases | 149k tags | **served** as refs (browse refs, tree); no release objects | none are hand-made |
| deploy keys | per coding run | **new**: `repo` on `POST /v1/git/keys` | org admin registers; bound to one repository |
| users, orgs, teams, access | 20 users, 192 orgs, ~201 teams, 12k access rows | **partial**: org = IAM org, project scope, repo `public` bit; no teams | auth is IAM only; teams map to IAM roles, undecided |
| access tokens | 72 | **by design not served**: Hanzo IAM bearer (git accepts it as the password) | |
| container registry (`/v2`) | 5 images, 9 GB | **missing** | `oci.hanzo.ai` already exists beside it; decision |
| LFS, wikis | 0 | not needed | |
| web UI | ~8,000 req / 3 h, 80 % of all traffic | **partial**: minimal pages under `/v1/git` (`ui.go`) | decision: a UI at git.hanzo.ai or redirect to the console |
| forge REST API (`/v1/repos/…`, `/v1/orgs/…`, `/v1/admin/…`) | the `forge/` client, ~1,000 req / 3 h | **missing** by design | the client is replaced by calling the store in-process; each call site is a change |
| internal pre/post-receive hooks | forge's own | **not needed**: receive runs in-process (`receive`, `fireBranchBuilds`) | |

## Order of work

1. Commit statuses — merged (#701).
2. SSH: key registry, deploy keys, listener over the objects store — this PR. Left: the run engine mints a deploy key per run (replacing `forge/grant.go`), and the SSH Service/LoadBalancer in universe.
3. Root-host smart HTTP address and the owner map, after the importer fixes the owner namespace.
4. Scheduled and tag-create triggers; artifacts; secrets from KMS.
5. Replace `forge/` calls with in-process store calls, route by route.
6. Scheduled link refresh for the 4,148 mirrors.
