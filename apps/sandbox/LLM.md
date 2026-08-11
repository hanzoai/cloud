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

## The screen, and the three things that were not obvious

A `desktop` sandbox has an X server, and `/v1/sandboxes/:id/screen` is how you
look at it: the terminal's three doors again — a ticket, a page, a socket —
differing only in the bytes on the wire (RFB) and the client in the page (noVNC,
inlined the way xterm is). The ticket, the bridge, the lease that bounds a
session and the attention that keeps it from being reaped are the SAME code;
what a session runs is the only argument.

**The pixels come out through the exec channel.** The image binds its VNC server
to `127.0.0.1:5900` on purpose — a screen reachable from the pod network is a
screen whose only defence is a NetworkPolicy — so there is no address to dial.
`socat - TCP:127.0.0.1:5900` on the exec subresource IS the transport. Nothing
new is exposed and there is nothing new to authorize.

**Not on a pty, and this one is a silent corrupter.** A pty translates bytes; RFB
is bytes. A screen on `tty()` works until a pixel happens to be 0x0d. It is
`stream()` for that reason and no other, and stderr comes back with it so a pod
whose screen is not running says "connection refused" instead of showing a black
rectangle.

**The image's screen died at thirty seconds, and nothing could see it.**
`x0vncserver` is TigerVNC's session MANAGER, not its server: it starts
X0tigervnc and then waits for the port by binding INADDR_ANY with SO_REUSEADDR —
a test a LOOPBACK listener can never pass, because 127.0.0.1:5900 leaves
0.0.0.0:5900 free. After 300 tries at 100ms it killed a server that had been
serving since boot. Every symptom pointed elsewhere: the pod is Running, exec
answers, X and openbox are up, and only a VNC client ever finds out. hanzoai/bot
965d91487 calls the server directly.

Same shape, one layer up: zsh was installed in every class and was the prompt of
none, because the user's login shell stayed `/bin/bash` and tmux asks passwd what
to run. Installed is not in use — for a shell, for a VNC server, for anything a
supervisor stands in front of.

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
