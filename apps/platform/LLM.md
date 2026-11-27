# LLM.md — apps/platform

Guidance for apps/platform. Moved from the root LLM.md; cross-cutting rules stay in the root.

### `/v1/platform/runner` builds ANY project, not only a Dockerfile

`/v1/platform/runner` has two lanes, and a request is in exactly one of them:

- **image** (`image:`) → `launchDirectBuild` → rootless BuildKit → a pushed OCI ref.
- **artifact** (`binaries:`) → `launchArtifactBuild` (`apps/platform/artifact.go`) → a
  Job whose initContainers are ONE PER RECIPE ENTRY, each in that entry's toolchain image
  (`image:`, default `golang:1.26-bookworm`), sharing `/w`; then a publisher that hashes
  everything the recipe left in `/w/dist`, PUTs it to hanzoai/s3 and writes `binaries.json`
  last. Output: `https://s3.hanzo.ai/<bucket>/<owner>/<repo>/<tag>/binaries.json`, the same
  layout hanzoai/ci publishes to — one index, two front endpoints.

The recipe is the EXISTING `hanzo.yml` contract, extended by exactly two optional fields:
`run:` (a build command for any toolchain that is not Go) and `out:` (the glob of what it
produced). `main:` stays the zero-config Go lane. Nothing here is a second recipe format.

Why the split in the Job matters: `run:` is arbitrary shell by design (same trust as a
Dockerfile `RUN`), so it must never see a credential — the initContainers carry no
object-store env and no service-account token; only the publisher, which runs a constant
script, mounts `artifact-s3` (a KMSSecret; cloud holds no secrets grant in `hanzo-build`).
The publisher writes over the INTERNAL endpoint (`s3.hanzo.svc:9000`) and records the
PUBLIC URL, because `s3.hanzo.ai` is this cluster's own LoadBalancer and does not hairpin —
a pod dialling it times out. Its egress hole is `artifact-publish-egress` in universe,
selecting the pod label `hanzo.ai/publish=artifact`.

### The delivery board is typed, and the gate had to move for it to be safe

`GET /v1/platform/{apps, apps/:app, apps/:app/cd, cd}` are typed ops
(`apps/platform/delivery.go`), so the fleet's delivery board is an MCP tool, a CLI
command and a typed SDK method where it published an operationId and nothing else
— an agent asked "what is deployed" had nothing to call. **platform 30 → 34 of 37;
the fleet catalogue 1502 → 1506.**

**THE DISCRIMINATOR FOR WHICH ROUTES CONVERTED IS "READS NO BODY", NOT THE VERB.**
All four are GETs, so zip's decode is skipped and there is no 400-before-403 to
trade; `POST /v1/platform/apps` beside them stays raw for exactly that trade, and
says so in `apps/platform/delivery_wire_test.go`. Reading the class as "GETs
convert, POSTs do not" gets the next package wrong in both directions.

**`admit` became a FUNCTION**, because it read nothing off its `board` receiver
and a second surface needed it — and a second copy of an authorization rule is two
rules.

Two things this cost that are worth knowing before the next conversion:

- **zipdoc needs a router whose TYPE it can resolve, and widening a parameter to
  an interface is what breaks it.** `appsRoutes(app cloud.Router, …)` was already
  being CALLED with the concrete `zapp`; the declaration alone made zipdoc refuse
  every op in the file ("cannot resolve the path prefix of the router"). One word —
  `*zip.App` — fixed it. Note this REFINES failure mode #13, which reads as though
  the absolute-on-the-app form cannot carry prose at all: it can, and apps/world,
  pricing, plan, prefs, share and now these four all do. What it cannot resolve is
  an INTERFACE-typed router, whatever the path.
- **A harness that never installed `cloud.Bridge` measures a gate that can only
  refuse.** `mountDelivery` built a bare app, which was fine while the handlers
  read identity straight off the `*zip.Ctx` and broke the moment they read it off a
  context — 403 to a genuine admin, in tests only.

## A queued GitHub job is counted against the runners that will take it

A job's runners are named try 0 (the delivery's) and `-r1`…`-r9` (the
reconcile's), and the id is never truncated — the repository is; a run of
separators holding a dot collapses to one dot, so every name is a valid object
name and label value (`FuzzRunnerName` checks with apimachinery's validators). A name held by a
runner busy with another job, counted for an older one, or finished (returned in
`Reclaim`, which platform deletes) is passed over to the next try. A runner stuck
on the cluster leaves the job alone, so stuck pods do not pile up. A registration
with no Job leaves it alone too — it may be a delivery's whose Job is not listed
yet — and is withdrawn only when the previous reconcile saw the same id so.

`cloud.Podless` is the one rule for a label naming macOS or Windows (parts
between `-`/`_`, any case: macos*, windows*, osx, darwin, mac, m1–m4, win,
win32, win64; a label naming linux never is). Provider's `ours` refuses on it
before reading `self-hosted`, which alone does not say Linux, and platform's
`runnerNode` refuses a launch on it.

Budget per installation per pass: 1 runner listing, 1 per hundred granted
repositories, 60 run listings, 1 per active run — ~750/h of the 5,000/h token
limit. A delivery's `queuedFor` reads queued runs only: 1 per hundred
repositories, 30, and 1 per queued run.

## A build waits its turn and is never refused for the others

Every build Job (image, index, artifact) is created suspended by `createBuildJob`, which
then runs `dispatchBuilds`; a suspended Job is the queue entry, so the line is the
apiserver's and survives every replica. `dispatchBuilds` resumes builds, not Jobs, under two
numbers an operator sets on admin.hanzo.ai's board, `platform_builds` (whole cluster, seed 48)
and `platform_builds_per_org` (seed 20): the org running the fewest goes next, within an org
an admin's build before a member's and then the oldest, and a build with one Job started takes
the rest at once (the index of a fan-out never waits). The reconcile tick dispatches too, which
is when a finished build frees its place. The only refusal is `errBuildQueueFull` (429): an org's
line holds ten times its concurrency.

A build row is `queued` until its first Job starts, then `building`; `GET /v1/build/{id}` says
`position` while it is queued. The 45-minute deadline runs from the first Job's `startTime`
(`buildOverdue`), so waiting is not running.

A door build keeps what it was asked (`Build.Request`, `Attempt`; retry.go). When its source fetch
fails for the forge's sake (`failed to fetch remote … error: 5xx`, a timeout, a dropped connection)
a single-platform build is launched again under its own Job name, up to three times after 30 s,
2 min and 5 min; any other failure, a fan-out and a build with no kept request fail at once. The
failed Job is deleted to free its name, and the next reconcile tick (`askAgain`) launches it.

## Four xl runners share a node

An xl runner (`xl-linux-amd64`) asks 7 of a node's 32 cores, so four fit, and the class's spread
(`topologySpreadConstraints`, `DoNotSchedule`, hostname) used to allow a skew of one. A node out of room
for another job counts as the least-loaded one whatever else is on it: the cloud builder
(`builder-hanzo-inc-cloud`, nodeSelector `hanzo.ai/class=xl`, 30 cores, no class label) held one xl node
with a count of zero, so every other node stopped at a single job. Measured 2026-11-21 09:32 UTC: 127
runner pods Pending, 50 Running, the eleven other xl nodes at 7 of 32 cores requested and 0-27% busy.
The skew is `xlPerNode` (4); heavy jobs keep one.
