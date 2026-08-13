# sandbox — what a pod is, and what it is handed

## The identity question, and the three answers that were wrong

A sandboxed agent has to authenticate. Three designs shipped and three were
reverted, all failing the same way: **a credential that authenticates more than
its design describes.**

1. **`hrun_`, the run grant.** Taught to `credentialUser` — ai's *general*
   identity resolver — and ai owns the bare `/v1` catch-all, so one grant
   widened ~200 routes. A sandbox could trade it for a session through
   `GET /v1/ai/account`'s self-heal, and in the reserved `admin` org
   `IsSuperAdmin` (a bare `Owner == "admin"` compare) made `GetOrg` honour
   `X-Org-Id` cross-tenant.
2. **Cloud's own machine identity.** Minted from `IAM_CLIENT_ID` with no
   `Scopes` — the same token class KMS's own login returns.
3. **The run-scoped credential** (`8da80cc93`): custom auth wearing a scope.

`cred.go` states the bar the fourth answer has to clear, and it is a high one:
nothing is minted here, no new authority, no second answer to "may this pod do
X". What it does is *deliver* a credential IAM and DigitalOcean already issued.

**The test trap that let #1 through:** its isolation test passed, because it only
ever built `Run{Org: "acme"}`. Containment held everywhere except the reserved
org, which is the only place that mattered. A test that never constructs the
privileged case proves nothing about it.

## What a tenant agent should be handed

Today a SuperAdmin's sandbox gets the SuperAdmin's own DO token and kubeconfig,
and **a tenant's sandbox gets nothing** — so a tenant agent cannot reach
inference, or anything else, on its own behalf.

The identity is IAM's, and it belongs to the ORG:

```
org  →  IAM application  <org>-agent      provisioned, never promoted
        a run gets a short-lived token from THAT application
        subject = the person who asked
        scope   = the ops an agent run may call
        life    = the run
```

Five properties, each killing one of the failures above:

- **IAM mints and validates it.** Cloud delivers. One auth, one gate — the same
  rule that makes this a delivery rather than a fourth rejected design.
- **Org-scoped by construction.** The application belongs to the org, so there is
  no cross-tenant scope to reach. That is what makes the `admin`-org escalation
  impossible rather than merely unlikely.
- **Carries the asking person as subject.** `planeRunOnBehalf` already refuses an
  empty subject rather than defaulting to the org — "a turn that lost its caller
  must not run AS THE ORG: that would bill the tenant for an unattributable act."
  That invariant does this work for free.
- **Scoped to the ops an agent may call**, and handed to the ONE door that needs
  it — never to a general resolver. Every failure above is the inverse.
- **Dies with the run.**

**Provision, never promote** — the same rule as SuperAdmin (AC-6(5), AC-5).
Giving an org an agent identity means CREATING `<org>-agent` in IAM, not
elevating something that exists.

**The machinery already exists — VERIFIED, not assumed.** IAM serves
`POST /v1/iam/applications` (create) and `POST /v1/iam/oauth/token`
(client-credentials), and `internal/provision/provision.go:504` already upserts
applications programmatically against `/v1/iam/admin/applications/upsert`. So
this needs NO new auth machinery, which is the whole reason the previous three
attempts were wrong: each invented a credential rather than asking the service
whose job it is.

What is missing is only the two ends:
1. nothing CREATES `<org>-agent` — the natural place is wherever an org is
   created, beside whatever else an org gets by default;
2. nothing DELIVERS its token to a sandbox — `cred.go` is where that belongs,
   next to the SuperAdmin delivery it already does, and by the same rule: read a
   credential the issuing service already made, mint nothing.

Sequence matters. (1) is inert on its own — an application nobody uses — so it
can land and be reviewed by itself. (2) is the part that hands a capability to a
running pod and is the part to review hardest.

**Where it is created, and there is only one answer.** An earlier version of
this file listed three "options" — boot seeding, IAM's internal reconcile, the
public API — which was a mistake: those are three MECHANISMS, and the question is
who OWNS the act. The owner already exists.

`apps/account/iam.go` holds an `iamClient` that creates an org's IAM objects
with cloud's own machine identity (`c.basicAuth()`), and already calls
`POST /v1/iam/add-organization`. `<org>-agent` is created there, beside
`createOrganization`, by that client. One place, one authority, nothing new.

There is no bootstrap problem: the credential doing the provisioning is cloud's
own, it already exists, and creating IAM objects for an org is precisely what it
is already for. The "provisioning a credential needs a credential" worry came
from imagining a caller that does not exist instead of finding the one that does.

`iam/pkg/store` stays read-only and is not the path — it is how cloud READS IAM
in-process. Creation is an act, and acts go through the client that holds the
authority for them.

`apps/iam` and `apps/sandbox` are separate PROCESSES, so a sandbox never reads
this itself; it asks over the plane. Noted in advance because that mistake has
been made five times in this codebase and caught five times afterwards.

## `android` — a phone is a WINDOW, and that is the whole design

An Android emulator is qemu drawing an ordinary X window. The desktop class
already runs an X server and already has that display carried out through the
exec channel, so looking at a phone needs no port, no socket, no client and no
second streaming path — it needs an SDK on the display that exists. That is what
the class is: `FROM desktop AS android`, plus an AVD, plus one argument.

`sandbox-desktop` now takes a command. The desktop script's last line was `exec
sleep`; it is `exec "$@"` when it is given something, so `CMD ["sandbox-desktop",
"avd"]` is one screen with the class's own program on it. The alternative — a
second script that starts a second X stack — is how two copies of the same four
processes come to drift, and only one of them gets the next fix.

**A class is now ONE ROW, and adding one is filling in a struct.** `classes` was
a `map[string]bool` here, `defaultTTL` was a second map in runtime.go, and the
pod's command was a third fact spelled `m.Class != "desktop"`. Each was a place a
new class was silently half-added, and both silent failures are real: a class
missing from the TTL map leases for zero seconds and is reaped before its caller
reads the reply, and a class missing from that comparison has its image's CMD
replaced by `sleep infinity` — so the one thing it exists to run never runs and
the pod looks perfectly healthy. The row carries ttl, the resource envelope,
`screen` and `kvm`; `desktop_test.go` quantifies over the table rather than
naming a class, so an assertion cannot be right about the old set and wrong about
the new one.

**The resource envelope existed only as a comment.** `classes`' own doc said
"each is one image tag and one resource envelope" while the envelope half had
never been built — resources were `MACHINE_*`, fleet-wide, one size for every
sandbox. An emulator holds a whole guest machine's RAM before it draws a pixel
and would have been handed 512Mi. A class's row wins where it states a value and
the fleet default stands where it is silent, so the other three are unchanged.

**/dev/kvm is a DEVICE PLUGIN and the reason is EPERM, not EACCES.** Measured on
do-sfo3-hanzo-k8s, worker-pool, 2026-08-13: a privileged pod gets
`KVM_GET_API_VERSION → 12` and `KVM_CREATE_VM → ok`, so the nodes can do it — DO
droplets expose the virtualisation flag, which is also why kata-fc works here. An
unprivileged pod with a `hostPath` mount of `/dev/kvm` sees the file, `ls -l`
shows it, and every `open()` returns EPERM: the container's device cgroup, which
a volume does not touch and a supplementalGroup does not fix. The kubelet adds a
device to that allowlist only when a device plugin hands it over, so the pod asks
for `devices.kubevirt.io/kvm` and a node without the plugin never receives the
class. Pending naming a resource is the honest failure; the alternative is qemu
interpreting the guest CPU, where a cold Android boot does not finish in any time
a person waits.

So the premise "the cloud pool has no /dev/kvm" is wrong in a way worth writing
down: the pool has the CPU and lacks the PLUGIN. `hanzoai/universe
infra/k8s/kvm/` is that DaemonSet, unwired until the android image is published.

**Proven end to end on 2026-08-13, off-cluster, because the image is not built
yet.** On evo (x86_64, KVM): the SDK, `sandbox-desktop avd` verbatim from
hanzoai/bot, `Boot completed in 24655 ms`, `adb devices` → `emulator-5554`, one X
window titled `Android Emulator - hanzo:5554`, `127.0.0.1:5900` and
`127.0.0.1:6080` LISTENing on loopback only, `socat -u TCP:127.0.0.1:5900`
answering `RFB 003.008` — the exact bytes `screen()` carries — and a booted
Pixel 6 rendered in Chromium over noVNC. Driving the phone with `adb shell am
start` changed the browser's frame, so the stream is live rather than a still.
What is NOT proven is the pod: no android image exists, and no cluster node runs
the device plugin.

**amd64 only, and this time it is the image's limit rather than the fleet's.**
Google publishes no linux-aarch64 emulator, so the arm64 answer is Cuttlefish,
which is a different program and would be a different stage.

## The harness

```
hanzo.chat ─┐
hanzo.app  ─┼─→ one agent door ─→ sandbox ─→ `dev` as the harness
Slack @hanzo┘
```

`dev` runs INSIDE the sandbox and is the harness; the surfaces are channels onto
it. `plane.ChannelsIngest` is the talk-through path, connectors are how it
reaches third parties, and `runtime.go` derives isolation from two facts —
`kernel` (is this our own org?) and `shares` (does it need a filesystem) — so a
tenant gets gVisor and our own agents get runc.

## Two facts about the runtimes that cost real time

- **kata-fc has no shared filesystem.** `configuration-fc.toml` declares no
  `shared_fs`, so a PVC mounted under it silently becomes tmpfs and writes are
  LOST. Firecracker rejected virtio-fs upstream (issues #1180, #1351, closed
  2020-08-11). With emptyDir it gets a real block device and is ~16x faster than
  gVisor on interactive work (git status 17ms vs 280ms).
- **The 175s figure was cold start, not steady state** — devmapper is a separate
  image store and takes ~188s to unpack. A negative control (kata-clh, which uses
  no devmapper) came within 15% of kata-fc, so the microVM costs ~4x and the pool
  is ~15% of that. Do not conflate launch latency with the runtime.

## Chat and the sandbox — what is actually wired

CORRECTION. An earlier version of this section said `apps/coding` registers zero
typed ops and that chat therefore has no computer. That was wrong, and wrong in
a way worth remembering: it was concluded from grepping `apps/` alone, and the
doors are registered in `plugin/`.

Both exist, in `plugin/agents/coding.go`:

```go
zip.Post[...](cloud.ZipApp(app), "/v1/coding",    httpCodingStart)   // product op -> agent tool
zip.Post[...](cloud.Plane(),     "/coding/start", planeCodingStart)  // plane op   -> chat bridge
```

So the sandbox IS a typed product op and DOES project as a tool an agent can
call. The `code:` prefix in Slack (`codingIntent`, slack_coding.go) is a
SHORTCUT onto the plane door, not the only way in — a keyword that skips the
model rather than one the model needs.

The credential is already right and needs no redesign: `start.go` resolves it
server-side via `agentCredential(ctx, repo, project, "refs/heads/"+branch, ttl)`
AFTER the session exists, because the session names the branch and the branch is
what the grant is FOR. It expires with the run's own budget plus a minute. A
sandbox run without one fails closed rather than discovering at push time that
it cannot write.

What is genuinely unverified is whether a chat turn ever REACHES it — whether
the tool is offered and chosen. That is a question to answer by watching one
turn, not by reading more code, and it is the next thing to check rather than
anything to build.

## How the shortcut works, and why it reads as the only path

`apps/agents` is the chat brain and reaches no sandbox. `apps/coding` has the
sandbox and no chat. Slack bridges them with a literal prefix:

```go
if !strings.EqualFold(t[:len("code:")], "code:") { return "", false }
```

`@hanzo code: repo fix the bug` gets a machine. `@hanzo fix the bug in repo`
gets a model with no machine. hanzo.app and hanzo.chat do not have the magic
word at all, which is the whole of why they are not computer-using.

**The cause is one fact: `apps/coding` registers ZERO typed ops.** The agent's
tools come from the one registry —

```go
for _, t := range tools.Default().List(ctx, tools.Scope{Org: org}) {
    if !wanted[t.Name] || !t.Dispatchable || !t.Activated { continue }
```

— so anything in the registry that the agent's list names is callable, and the
sandbox is not in the registry. A keyword gate got bolted on beside the brain
because the brain had no way to reach it. Same shape as bridge.go beside
channels, and the two doors beside one connector registry.

**The fix is structural, not an integration.** Give coding a typed op and it
lands in the registry, becomes a tool, and every surface — Slack, hanzo.app,
hanzo.chat, MCP — reaches it through the one brain. `codingIntent` then deletes,
and the model decides what a model should decide.

The input is NOT RunRequest. That type carries `CredToken`, and a tool schema
the model fills in must never be able to name a credential:

```go
type CodeRunIn  struct { Repo, Task, Tool, Branch string; Desktop bool }
type CodeRunOut struct { RunID, Status, Output, Branch, URL string }
```

The org's GitHub credential is resolved on the ANSWERING side from its
connection row — the same rule the rest of this file is about. An agent says
what to do; it never says what to authenticate as.
