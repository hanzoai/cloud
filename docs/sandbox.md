# The sandbox: the executor everything already points at

Status: **the original design. It shipped, and it shipped DIFFERENTLY in three
places.** Read those three before believing anything below, because each one
reads here as a gap somebody still has to close, and closing them again is
rebuilding what was deliberately torn out.

- **There is no `boxd` and there must not be one.** §3 and the proxy tables
  describe an HTTP daemon inside the box. It was deleted along with the ~2000
  lines that served it; work reaches a sandbox through the Kubernetes exec
  subresource, which is one channel instead of two and needs no port, no
  listener and no auth of its own. `cmd/boxd` does not exist.
- **`@hanzo/dev` is not what the image installs** (§47 says no image installs
  it). The npm package put a Node shim at `/usr/local/bin/dev` and claimed the
  `hanzo` name for the agent, so the cloud CLI became a symlink to it. The image
  fetches the NATIVE binaries — `hanzo`, `dev`, `hanzo-mcp` — from release
  assets, checksummed.
- **The classes are four, not three**, and `admin` is a tag no caller can ask
  for: it is substituted for a SuperAdmin's `dev`.

What is true and still worth reading: the hard boundary in §"The hard boundary"
(`apps/sandbox` executes nothing itself), the lease/GC model, and the routing
prefix. The image itself is specified by `hanzoai/bot`'s `Dockerfile.box` and
its `LLM.md`, which are the source of truth for what is in a box.

Every control plane for agentic execution exists. The thing they control does
not. This is the spec for the thing.

---

## 0. What is actually broken right now

Measured, not remembered. All paths absolute; all line numbers as of this commit.

**hanzo.chat's `execute_code` tool is pointed at a service with no pods.**

    chat.yaml:126   LIBRECHAT_CODE_BASEURL = http://cloud.hanzo.svc:8000/v1
      -> cloud apps/exec (reverse proxy, apps/exec/exec.go)
      -> CODE_EXEC_UPSTREAM  — set NOWHERE in universe (grep: zero hits)
      -> falls back to defaultUpstream = http://code-exec.hanzo.svc.cluster.local:8000
      -> charts/app/values/hanzo/code-exec.yaml, its own words:
         "the code-exec Service exists ... nothing matches app: code-exec today"

So the chain resolves end to end and terminates on zero endpoints. `apps/exec`
is a correctly-built proxy in front of nothing.

**`/v1/functions` invoke is dead the same way, one env var later.**
`apps/functions/invoke.go` reads the same `CODE_EXEC_UPSTREAM`; unset means
`configured()` is false and every non-fleet invoke returns 503. Fail-closed and
honest, but the product does not run.

**`apps/coding` — a fourth consumer nobody counted — is dead a third way.**
It dispatches through `bots.Stream` to `POST /v1/coding-tasks` on
`bot-gateway.hanzo.svc` (`apps/coding/task.go`). That handler is real
(`~/work/hanzo/bot/src/gateway/coding-tasks-http.ts`) and it fail-closes on
`createDockerRuntime`, which needs two things the cluster does not have:

- `HANZO_CODING_SANDBOX_IMAGE` — grep across `charts/`: **not set anywhere**
- a Docker daemon — grep for `docker.sock` across `charts/`: only
  `git-runner.yaml` (the ARC pool). bot-gateway is an ordinary pod.

**`~/work/hanzo/app/lib/agent/sandbox-fs.ts` calls a daemon that no longer
exists** — `/process/execute`, `/files/upload`, `/files/download`,
`/files/info`. Endpoints of the deleted Daytona fork. Known defect; rewritten
against §3 below.

**The image gap.** No image in the org installs `@hanzo/dev`. But the *recipe*
is further along than anyone said: `~/work/hanzo/bot` already has
`Dockerfile.sandbox`, `Dockerfile.sandbox-common`, `Dockerfile.sandbox-browser`
— digest-pinned `debian:bookworm-slim`, non-root `sandbox` user, layered by
`ARG BASE_IMAGE`, `CMD ["sleep","infinity"]`. They are good. They are also
laptop-only: `src/agents/sandbox/constants.ts` defaults to bare tags with no
registry (`bot-sandbox-common:bookworm-slim`), and `hanzoai/bot` has **no
`.github/workflows` and no `hanzo.yml` at all**, so nothing has ever built them.

The gap is exact: **the box recipe exists, the agent is not in it, and CI has
never built it.**

---

## 1. Where the control plane lives — `apps/sandbox` in `hanzoai/cloud`

Three of the four consumers are already in this binary. `apps/exec` needs an
upstream, `apps/functions/invoke.go` needs an upstream, `apps/coding` holds a
`Runner` seam. Put the scheduler anywhere else and three in-process calls become
three network hops.

The duplicate-product argument is the same one that already got settled: the
standalone Fission install was deleted from the cluster on 2026-08-04 (universe
`798fac65`) for 0 invocations in 36 days, while `/v1/functions` — the same
product, in cloud — stayed. The API never went away; the second copy did.

cloud also already owns both things the scheduler needs: the per-org store
(HIP-0302, one SQLite per org) for the box registry, and `cloud.EmbeddedTasks()`
for lease/GC — the same durable engine `apps/functions/fleet.go` runs `fn.run`
jobs on. A bespoke reaper goroutine would be a second scheduler.

Ships as `plugin/sandbox`, per cloud's per-app plugin binary model.
Manifest row — name and prefix are both free (verified):

```go
{Name: "sandbox", Prefixes: []string{"/v1/sandbox"}},
```

Place it beside `functions` (line 123). The only ordering constraint is that it
land **before the `ai` row at line 382**, which owns the bare `/v1` glob.

### The hard boundary

**`apps/sandbox` never executes anything.** It holds the same line `apps/exec`
holds — *"there is no os/exec anywhere in this package"*. It is a scheduler and
a proxy: it talks to the Kubernetes API to create, suspend and resume box Pods,
and it forwards file/process calls to them. The isolation boundary is the box.
If a reviewer ever finds `os/exec` in `apps/sandbox`, the design has failed.

---

## 2. Where the image lives, and what is in it

**Repo: `hanzoai/bot`.** The three recipes are already there, already correct,
and bot's own `createDockerRuntime` already wants a published one. What bot
lacks is CI, which is the actual reason the images are laptop-only. Fix that,
don't move them.

**Not `hanzoai/cloud`.** cloud's `hanzo.yml` refuses a second images lane in its
own words: *"a lane here would be a SECOND builder on the same commit — the
double-build that re-opens the phantom-tag hole."* One image, one owner.

### Why we do NOT layer on `ghcr.io/hanzoai/xvfb`

The brief assumed layering on it. Read the recipe
(`~/work/hanzo/operative/docker/Dockerfile.xvfb`) and it disqualifies itself:

| in the image | why it disqualifies |
|---|---|
| `mongodb-org` | violates the drop-Mongo directive outright |
| `postgresql`, `mariadb-server`, `redis-server` | violates the Base/SQLite-only storage directive |
| `docker.io`, `docker-compose`, `colima` | a container runtime **inside** the thing that is supposed to *be* the containment boundary |
| VS Code, Emacs, Spacemacs, SpaceVim, LibreOffice, .NET, PowerShell, Scala/sbt, Julia, Dart, R, Rails, Octave, PHP, Java | an interactive workstation, not an executor |
| `setup_20.x`, Go 1.21 | both behind |
| `ENTRYPOINT ["bash"]`, no `USER` | runs as root |

We keep its *intent* — the X/VNC recipe — and drop its layers. `hanzoai/bot`'s
`Dockerfile.sandbox-browser` is already the slim, digest-pinned, non-root
version of exactly that stack (`xvfb`, `x11vnc`, `novnc`, `websockify`,
`chromium`, `EXPOSE 9222 5900 6080`). That is the one to keep.
`ghcr.io/hanzoai/xvfb` stays where it is for operative's own computer-use work.

### The three tags

Registry is `registry.hanzo.ai` (self-hosted, S3-backed). Base is
`debian:bookworm-slim`, digest-pinned, non-root `sandbox` uid 1000 — lifted
verbatim from the existing `Dockerfile.sandbox`.

```
registry.hanzo.ai/hanzoai/box:base-<ver>      # = Dockerfile.sandbox, unchanged
  bash ca-certificates curl git jq python3 ripgrep
  useradd sandbox; USER sandbox; CMD ["sleep","infinity"]

registry.hanzo.ai/hanzoai/box:dev-<ver>       # FROM base
  build-essential pkg-config file unzip
  node 22 + pnpm + bun          (22, not bot's default nodejs — dev's toolchain)
  python3-venv + uv
  go, rustc/cargo
  npm i -g @hanzo/dev@<pinned>  <-- THE MISSING PIECE
  COPY boxd                     <-- §3

registry.hanzo.ai/hanzoai/box:desktop-<ver>   # FROM dev  (computer-use variant)
  xvfb x11vnc novnc websockify chromium xdotool scrot fonts-liberation
  EXPOSE 9222 5900 6080
```

Layering is `desktop` **on** `dev`, not beside it. bot's current tree builds
`sandbox-browser` as a sibling of `sandbox` — that duplicates the base and
gives a computer-use box no toolchain. One chain, three tags.

`<ver>` is `bin/imgver` from `hanzoai/ci`, same as the rest of the fleet.
`:dev-latest` is not a thing; deployments pin a digest.

Drop `INSTALL_BREW` from `sandbox-common`. Homebrew in a sandbox image is
~400 MB and a second package manager for a box that already has apt, npm, uv,
cargo and go. One way to install things.

### CI

`hanzoai/bot` gets the canonical pipeline it has never had:

`.github/workflows/cicd.yml` (~7 lines, the shape every repo uses):

```yaml
name: CI/CD
on:
  push: { branches: [main], tags: ['v*'] }
  pull_request:
  workflow_dispatch:
jobs:
  cicd:
    uses: hanzoai/ci/.github/workflows/build.yml@main
    secrets: inherit
```

Root `hanzo.yml` declares the three images (`images:` is a list) with
`context: .` and the three dockerfiles. CI builds all architectures and pushes
to GHCR + mirrors to `registry.hanzo.ai`. **Nobody builds these on a laptop.**
Delete `scripts/sandbox-*-setup.sh` once the tags publish, and repoint
`src/agents/sandbox/constants.ts` at the registry tags so `docker pull` works
for the local path too — one image, both paths.

### boxd, and why it is in `hanzoai/cloud`

`boxd` is the tiny HTTP server inside the box. Its request/response types are
the *same types* `apps/sandbox` marshals. If they live in two repos they drift;
that is the one failure this spec exists to prevent.

So: **`boxd` is Go, in `hanzoai/cloud`, at `cmd/boxd`** — same module as
`apps/sandbox`, so the wire structs are one declaration in one file and drift is
not expressible. It is static (`CGO_ENABLED=0`), a few MB, and it is *not* a
second cloud: it has no store, no IAM, no openapi. It reads a config from env
and serves §3.

The box image consumes it as a published artifact:

```dockerfile
COPY --from=registry.hanzo.ai/hanzoai/cloud:<pinned> /usr/local/bin/boxd /usr/local/bin/boxd
```

This is the direction cloud's own Dockerfile already runs — it pulls prebuilt
inputs (`console-embed`, `agent-skills`) *"published by THEIR OWN repos"*. The
pin is a reviewed line in bot's Dockerfile.

---

## 3. The HTTP contract

### One box primitive, two path families

A stateless `/exec` call and a long-lived coding box are **the same object at
two lifetimes**: `/exec` is a box with `ttl=0` that is created, used and reaped.
There is one primitive.

There are two *path families*, and the reason is not two products — it is that
we do not own one of the shapes. `@librechat/agents`' `CodeExecutor` fixes
`/exec`, `/exec/programmatic`, `/upload`, `/download/{id}`, `/files/{sid}` and
the `{session_id, stdout, stderr, files:[{name}]}` response. We cannot rename
them, so `boxd` serves them verbatim beside its native surface. Every other
consumer uses the native one.

### A. cloud — `/v1/sandbox` (org-scoped, IAM at the gateway, `principal.Org`)

| method | path | body / query | returns |
|---|---|---|---|
| POST | `/v1/sandbox/boxes` | `{project, class, ref?, image?, ttlSec?}` | `Box` (201) |
| GET | `/v1/sandbox/boxes` | `?project=&status=` | `{boxes:[Box]}` |
| GET | `/v1/sandbox/boxes/:id` | | `Box` |
| DELETE | `/v1/sandbox/boxes/:id` | `?purge=1` drops the volume too | 204 |
| POST | `/v1/sandbox/boxes/:id/suspend` | | `Box` |
| POST | `/v1/sandbox/boxes/:id/resume` | `{ref?}` | `Box` |
| GET | `/v1/sandbox/boxes/:id/events` | SSE | lifecycle + exec frames |
| ANY | `/v1/sandbox/boxes/:id/fs/*` | | proxied to boxd `/v1/box/fs/*` |
| ANY | `/v1/sandbox/boxes/:id/proc/*` | | proxied to boxd `/v1/box/proc/*` |

```
Box = {id, org, project, class, status, image, target, pvc, ref,
       createdAt, lastUsedAt, expiresAt, url}
class  = exec | dev | desktop
status = pending | running | suspended | error
target = the /v1/agents target id this box registered as (§6)
```

Yes, cloud proxies every `fs` call. That is deliberate: boxes have no public
address, and terminating auth anywhere but the IAM edge means inventing a second
auth path — which is banned. Same shape as `apps/exec`, and the per-call cost is
one in-cluster hop.

### B. boxd — native, `:8000`, never public, never internet-reachable

| method | path | body / query | returns |
|---|---|---|---|
| GET | `/v1/box/health` | | `{ok, boot, image, project, ref}` |
| POST | `/v1/box/proc/exec` | `{argv[] \| command, cwd, env{}, timeoutSec, stream}` | `{exitCode, stdout, stderr, durationMs}` |
| GET | `/v1/box/proc/exec/stream` | `?id=` | NDJSON `{stream:stdout\|stderr, data}` |
| GET | `/v1/box/fs/list` | `?path=&depth=` | `{entries:[{path,size,mode,isDir}]}` |
| GET | `/v1/box/fs/read` | `?path=` | bytes (executor's own Content-Type) |
| POST | `/v1/box/fs/write` | `{path, content \| contentB64, mode}` | 204 |
| POST | `/v1/box/fs/patch` | `{path, ops:[{type:"update",oldStr,newStr} \| {type:"rewrite",content}]}` | `{applied, summary, warnings[]}` |
| DELETE | `/v1/box/fs` | `?path=` | 204 |
| GET | `/v1/box/fs/search` | `?q=&limit=&regex=` | `{matches:[{path,line,text}]}` |
| GET | `/v1/box/fs/changed` | | `{paths:[]}` |
| POST | `/v1/box/git/clone` | `{url, ref, dir, credential{username,token}}` | `{head}` |
| POST | `/v1/box/git/push` | `{branch, credential{...}}` | `{commitSha, diffstat, changed}` |
| POST | `/v1/box/agent/run` | `{prompt, sessionId, timeoutSec}` | NDJSON `{type:step\|log\|result\|error, ...}` |

`fs/patch` ops and `fs/search`/`fs/changed` are shaped to satisfy
`app/lib/agent/fs.ts`'s `ProjectFs` exactly — `list/exists/read/write/search/
applyPatch/changedPaths/changedFiles` — so `sandbox-fs.ts` becomes a thin
rewrite of that interface and nothing in the agent loop changes.

`agent/run` frames are `{type: step|log|result|error, step, message, status,
branch, commitSha, diffstat, changed, ok, logTail}` — **byte-identical to
`apps/coding/task.go`'s `message` struct**, so `apps/coding` rebinds its
`Runner` seam to `apps/sandbox` and its orchestration, its session mirroring and
its tests do not move.

Credentials ride in the **body**, never argv, never a URL, never a log — the
rule `apps/coding/task.go` already states and `coding-task.ts` already enforces.

### C. boxd — the frozen LibreChat family

```
POST /v1/exec                {lang, code, files?, args?}
                             -> {session_id, stdout, stderr, files:[{name}]}
POST /v1/exec/programmatic
POST /v1/exec/upload         multipart
GET  /v1/exec/download/{id}  bytes
GET  /v1/exec/files/{sid}    listing
```

The three file addresses answered at the bare roots `/v1/upload`, `/v1/download`
and `/v1/files` until they folded under `/v1/exec`. The client composes all three
off a configured base, so what the fold costs a caller is the base — point
`LIBRECHAT_CODE_BASEURL` at `.../v1/exec` and every path below it is unchanged.

**Served under `/v1/`, and this uncovers a live defect that must be fixed with
the same commit.** The two existing consumers currently disagree about the path:

- `apps/exec` is `httputil.NewSingleHostReverseProxy` with a pathless target, so
  it preserves the incoming path — the upstream is asked for **`/v1/exec`**.
- `apps/functions/invoke.go` builds `e.upstream + "/exec"` — it asks for
  **`/exec`**.

No single value of `CODE_EXEC_UPSTREAM` satisfies both. Measured, both
consumers reconstructed against a recording upstream:

```
== CODE_EXEC_UPSTREAM=http://sandbox-exec.hanzo.svc:8000
   apps/exec      (chat)      -> upstream sees "/v1/exec"
   apps/functions (invoke.go) -> upstream sees "/exec"
== CODE_EXEC_UPSTREAM=http://sandbox-exec.hanzo.svc:8000/v1
   apps/exec      (chat)      -> upstream sees "/v1/v1/exec"
   apps/functions (invoke.go) -> upstream sees "/v1/exec"
```

Neither value works for both, and the `/v1` suffix is worse — it makes the
proxy ask for `/v1/v1/exec`. Fix, one line, in `apps/functions/invoke.go`:

```go
req, err := http.NewRequestWithContext(rctx, http.MethodPost, e.upstream+"/v1/exec", ...)
```

Then `CODE_EXEC_UPSTREAM = http://sandbox-exec.hanzo.svc:8000` (no path) serves
both, `apps/exec` is untouched, and no `/api/` appears anywhere.

### D. Who calls what, after

```
hanzo.chat  --LIBRECHAT_CODE_BASEURL-->  cloud /v1/exec (apps/exec, proxy)
                                           --CODE_EXEC_UPSTREAM--> exec pool boxd /v1/exec
/v1/functions invoke  ------------------->  same upstream, /v1/exec
apps/coding (Runner seam, rebound)  ----->  apps/sandbox (in-process) --> box /v1/box/agent/run
hanzo.app ProjectFs  -------------------->  cloud /v1/sandbox/boxes/:id/fs/* --> box /v1/box/fs/*
```

---

## 4. Isolation — what the boundary is, and what it is not

### What it is

- **One Pod per box**, in a dedicated namespace `hanzo-boxes`. Not `hanzo`:
  `code-exec.yaml`'s policy admits ingress from namespace `hanzo`, and boxes
  must not sit beside the datastores.
- `runAsNonRoot: true`, uid 1000, `allowPrivilegeEscalation: false`,
  `readOnlyRootFilesystem: true` with tmpfs `/tmp`, `capabilities: {drop:[ALL]}`,
  `seccompProfile: RuntimeDefault`, `automountServiceAccountToken: false`.
- No `hostPath`, no `hostNetwork`, no `privileged`. CPU/memory limits always set.
- **Two network postures on one image**, because the two classes have genuinely
  different needs — this is why the pod carries a class label:
  - `hanzo.ai/box-class: exec` — DNS only, no egress. This is exactly the
    policy `charts/app/values/hanzo/code-exec.yaml` already declares. Give the
    new exec-pool workload the label `app: code-exec` so that policy — written
    for a workload that never arrived, and deliberately kept ("the isolation has
    to already be in place the moment the workload comes back") — finally
    selects a pod.
  - `hanzo.ai/box-class: dev` — DNS + `git.hanzo.ai` + `api.hanzo.ai` +
    `pkg.hanzo.ai` and nothing else. A coding box must clone and install; an
    `/exec` box must not.

### What it is NOT

- **It is not a kernel boundary today.** grep for `gvisor`/`runsc`/
  `runtimeClass` across `charts/`: one comment in `code-exec.yaml` and an
  unrelated kserve CRD. There is no RuntimeClass, no installer DaemonSet, no
  runsc node. A container escape via a kernel LPE reaches the node.
- Therefore **the boundary that actually holds is the node pool.** Boxes run on
  a dedicated tainted pool (`hanzo.ai/box=true:NoSchedule`) with nothing else on
  it, so an escape lands somewhere holding no other tenant's workload.
- The spec reserves `runtimeClassName` on the box Pod. Nothing depends on it
  being set. When runsc is installed, setting it is a values change.
- Not claimed: side-channel resistance, protection against a malicious operator,
  or that a `dev`-class box cannot reach the package registries it is allowed to
  reach.

---

## 5. Fast launch, suspend and resume

The cost is not starting a container. It is populating one. Everything here
attacks populate, not boot.

**a. Warm pool.** A Deployment of N idle pods per class, label
`hanzo.ai/box-state: warm`, no volume bound, already running `sleep infinity`
(the existing `Dockerfile.sandbox` `CMD` — the recipe anticipated this). Claiming
a box is a **label patch** (`box-state: bound`, `box-org`, `box-id`) plus a
volume attach — not a pod create, not an image pull, not a scheduler cycle.
`apps/sandbox` refills asynchronously. The `exec` pool binds no volume at all and
resets between calls, so a chat code-run never waits on storage.

**b. One PVC per (org, project).** `box-<org>-<project>`, `do-block-storage`,
mounted at `/work`, holding the checkout **and** the caches — `node_modules`,
the pnpm store, pip, cargo, go mod. Resume rebinds the same PVC, so the clone
and the install are already on disk. That is the entire trick: resume is
`git -C /work/repo fetch && checkout <ref>` and nothing else.

Caches live on the PVC, not in the image, so an image bump does not cost a cold
install.

**Honest constraint:** `do-block-storage` is RWO — one node at a time. So **one
live box per (org, project)**, and a resume that lands on a different node pays
a DO detach/reattach (tens of seconds, not instant). A second concurrent box for
the same project is **refused**, not silently given a cold ephemeral volume —
fail closed, and it matches the one-box-per-project mental model.

**c. Suspend** deletes the Pod and keeps the PVC, recording
`{boxId, org, project, pvc, ref, ttl}` in the org store. **Resume** claims a warm
pod and attaches the PVC.

**d. Lease and GC are a `hanzoai/tasks` workflow** on `cloud.EmbeddedTasks()` —
the same engine `apps/functions/fleet.go` already uses. Heartbeat expires →
suspend. Idle `N` days → snapshot the PVC to `s3.hanzo.svc`, delete it. No
bespoke reaper loop; that would be a second scheduler.

---

## 6. How a box registers on the existing `/v1/agents` registry

No new control plane. The server side is **already built** — `apps/agents/
routing.go` + `routing_http.go` implement claim keys (256-bit, stored SHA-256,
never plaintext), serving liveness, and a 25s long-poll. What is missing is the
client: `apps/agents/routing.go` describes a `hanzo code --serve` daemon, and
grep across `~/work/hanzo/dev` finds no such thing. **boxd is that client.**
Build it once, in boxd, not in the CLI.

On boot:

1. `POST /v1/agents/targets` `{label:"box-<id>", kind:"cloud", host, spec:{os,arch,cpus,memory}}`
   → target id. (`kind` must be one of `laptop|cloud|gpu|cluster|machine`; a box
   is `cloud`.)
2. `apps/sandbox` mints the key server-side — `POST /v1/agents/targets/:id/key`
   → `{claimKey}` — and injects it into the Pod as `HANZO_TARGET_KEY`. **boxd
   never mints its own.** The key is a capability handed down by the scheduler,
   and it is stored hashed.
3. boxd long-polls `POST /v1/agents/targets/:id/claim` with `X-Target-Key`.
   204 → re-poll. **The poll is the heartbeat** (`servingTTL`); no separate
   liveness call.
4. On a claimed run: `POST /v1/agents/sessions` (or use the `sessionId`
   `apps/coding` already opened), then run `dev proto` and mirror every frame as
   `POST /v1/agents/sessions/:id/events` with kind `tool-call|log|status`.
5. Steering: poll `GET /v1/agents/sessions/:id/control?after=<seq>` and map the
   closed vocabulary onto `dev proto`'s Op set — `message` → `queue_user_input`,
   `stop` → interrupt + exit, `pause`/`resume` → gate the turn. `dev proto`
   exists for exactly this: *"a controller can steer a turn in flight
   (`queue_user_input`) rather than only interrupt it."*
6. Terminal result: `POST /v1/agents/targets/:id/runs/:runId/report`.
7. Heartbeat `Metrics{load1, memUsed, ...}` onto the target, so `/v1/visor/fleet`
   shows boxes beside laptops and GPUs and **tabs.hanzo.ai gets them for free**.

The session's `Target` field carries the box's target id; `Terminal` carries the
box's watch URL (the `desktop` class publishes noVNC on 6080).

---

## 7. Deploy — universe, on the forge

CD reads `git.hanzo.ai/hanzo/universe`, not GitHub. Push to the forge-tracking
clone at `~/work/hanzo/universe-certmgr`. The two lineages are unrelated
histories; do not try to reconcile them.

| file | change |
|---|---|
| `charts/app/values/hanzo/cloud.yaml` | add `CODE_EXEC_UPSTREAM = http://sandbox-exec.hanzo.svc:8000` |
| `charts/app/values/hanzo/sandbox-exec.yaml` | **new** — exec-class warm pool + Service :8000, image `box:exec-<digest>`, pod label `app: code-exec` so the existing NetworkPolicy selects it |
| `charts/app/values/hanzo/code-exec.yaml` | unchanged — it finally has a pod |
| `charts/app/values/hanzo/box-pool.yaml` | **new** — dev-class warm pool in ns `hanzo-boxes`, its NetworkPolicy, the tainted node pool |
| `charts/app/values/hanzo/bot-gateway.yaml` | unchanged — `apps/coding` rebinds to `apps/sandbox`; bot's `createDockerRuntime` stays for the laptop path only |

`CODE_EXEC_API_KEY` already exists in `cloud.yaml` from `code-exec-secrets`
(KMS-sourced). boxd reads the same key and compares it in constant time. No new
credential, and no custom auth: user identity is terminated by IAM at the
gateway for `/v1/sandbox`, and the `/v1/exec` family stays a service-key surface
because the chat server calls it server-side, exactly as `apps/exec` documents.

---

## 8. Build order

Each step is independently shippable and independently useful.

1. **cloud** — `cmd/boxd` + the `/v1/box` types. Fix
   `apps/functions/invoke.go` to `/v1/exec` in the same commit.
2. **bot** — root `hanzo.yml` + 7-line `cicd.yml`; add `@hanzo/dev` and `boxd`
   to `sandbox-common`; re-parent `sandbox-browser` onto it. CI publishes three
   tags. *This alone un-breaks hanzo.chat once §7's env var lands.*
3. **cloud** — `apps/sandbox`: manifest row, box store, Kubernetes client,
   warm-pool claim, the proxy routes.
4. **universe (forge)** — the values files in §7.
5. **cloud** — rebind `apps/coding`'s `Runner` from `bots.Stream` to
   `apps/sandbox`.
6. **app** — rewrite `lib/agent/sandbox-fs.ts` against `/v1/sandbox/boxes/:id/fs/*`.
7. **cloud** — boxd's `/v1/agents` claim loop (§6); boxes appear in `/v1/visor/fleet`.

## 9. Open, and deliberately not decided here

- **Snapshot format** for step 5(d)'s S3 archive of an idle PVC. A tarball is
  the obvious answer; whether it is worth deduplicating is a question for when
  there are enough idle projects to measure.
- **Cross-project cache sharing.** A shared read-only pnpm/cargo store volume
  would cut first-populate materially, but it is a cross-tenant read surface and
  needs its own argument before anyone builds it.
- **gVisor.** Reserved in §4, not scheduled. It needs a node pool with runsc
  before it is anything but a field name.
