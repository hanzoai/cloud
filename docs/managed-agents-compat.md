# Anthropic self-hosted Managed Agents and Hanzo's agent contract

Research for hanzo-inc/cloud#804, written 2026-11-19. It compares Anthropic's
self-hosted Managed Agents (released 2026-11-12) with what Hanzo ships and
specifies for agents, sandboxes and workspaces. It is a comparison, not a plan to
run Anthropic's server.

**The question each row answers:** could Anthropic's design sit under or beside
Hanzo's contract without weakening it? Hanzo is one service for many orgs, so a
design that is safe only when one deployment serves one customer is judged on
that.

- **compatible**: the two designs agree, and joining them weakens neither.
- **incompatible**: they disagree on something one side treats as a safety
  property, and joining them gives that property up.
- **needs-design**: no conflict in principle, but Hanzo has no shipped answer
  (sometimes a Draft HIP), or a join needs a seam nobody has built.

**Sources.** Anthropic's are its public documentation, read on 2026-11-19 and
listed at the end; `MA/` stands for
`https://platform.claude.com/docs/en/managed-agents/`. Hanzo's are this
repository at `d5f2f8334`, `hanzoai/HIPs` at `4271795`, and the sandbox network
policy in the deployment repository (`charts/app/values/hanzo-sandboxes/`). A
Draft HIP is a specification, not running code, and each row says which is which.

## Summary

| # | Concern | Verdict |
| --- | --- | --- |
| 1a | Callers and workloads: who may call the agent API | incompatible across orgs |
| 1b | Inference identity: what a model call is made as | needs-design |
| 1c | Environment identity: what a worker or sandbox holds | needs-design |
| 2 | Event-driven bots against a long-lived agent loop | needs-design |
| 3 | Per-org encryption and policy scope | incompatible across orgs |
| 4a | State | compatible |
| 4b | Sandbox isolation | compatible |
| 4c | Filesystem and VFS | needs-design |
| 5 | Model data leaving a "self-hosted" harness | needs-design |

The short version: Anthropic's unit of isolation is the **deployment**. Hanzo's
is the **org**, inside one deployment, and below it the **person** and the
**run**. Every row marked incompatible comes from that one difference. Where the
two meet one deployment to one org, most rows become compatible, at the cost of a
server, a database and workers per org.

## 1. Separate workload, inference and environment identities

Anthropic's server distinguishes three credentials (`MA/self-hosted`, "The three
credentials"):

- the **server API key**, held by applications and CLIs, deployment-wide, with
  administrator reach over every environment (`MA/self-hosted-data-and-access`,
  "Multitenancy"; `MA/self-hosted/configure`, "What each role can do");
- the **inference credential**, held by the server alone; workers and
  applications never see it (`MA/self-hosted/inference-provider`);
- the **environment key**, held by the workers of one environment. It can read
  events and post tool results in every session of that environment, so the
  docs say to keep it out of sandboxes (`MA/self-hosted-data-and-access`, "What
  roles don't limit"). For a container that runs one session, a per-work-item
  secret (`ANTHROPIC_WORK_SECRET`) stands in for it
  (`MA/self-hosted/call-the-api`, "Run workers in production").

Anthropic issues none of these. The customer generates or issues every one
(`MA/self-hosted-data-and-access`, "Credentials that the server accepts").

### 1a. Callers and workloads — incompatible across orgs

**Anthropic.** A caller is the server API key, or a person signed in through the
customer's OpenID Connect provider with one of three roles: `admin`, `developer`
or `user` (`MA/self-hosted/configure`). There is no workspace or org inside a
deployment. One deployment serves one tenant at one trust level, environments do
not separate trust levels, and a second tenant or trust level needs its own
deployment, database, server key and workers
(`MA/self-hosted-data-and-access`, "Multitenancy").

**Hanzo.** Every request acts in an org that IAM validated. The org comes from
the signed principal and never from a body or header (`LLM.md`, "Identity
vocabulary is IAM-native" and "Platform authority"; HIP-1210 "One store, one
org, one file"). A service proves who it is with its projected ServiceAccount
token, which IAM exchanges for a bearer that reads only that service's own
secrets (`secret/doc.go:12-23`). Cloud's own machine identity appears only as
the actor in a delegation and never leaves the process
(`apps/coding/key.go:37-42`).

**Verdict.** One Anthropic deployment cannot carry more than one Hanzo org: its
server key and its `admin` role see every environment, and nothing below the
deployment separates orgs. Anthropic's own guidance says the same. A deployment
per org would be compatible.

### 1b. Inference identity — needs-design

**Anthropic.** One inference credential per deployment: a Claude API key, a
cloud provider's credential chain, or a model gateway's token
(`MA/self-hosted/inference-provider`). Every session in the deployment calls the
model as that credential. A gateway can take short-lived tokens through an OAuth
exchange, and the token callback is told the session, agent, environment and
calling user (`MA/self-hosted/inference-provider`, "Short-lived tokens"). For a
gateway shared between deployments, the docs suggest a per-deployment
`x-tenant-id` header (`MA/self-hosted-data-and-access`, "Multitenancy").

**Hanzo.** A model call is made as the run's **person**, in the one org the run
bills. IAM mints the token per run, naming cloud as the actor, with scope
`ai:inference`. Cloud renews it (the box never does), so a person removed from
the org is refused at the next renewal and a copy lifted from the box dies
within an hour (`apps/coding/key.go:11-24`). Whoever minted it, a token with that
scope reaches only the model routes and the model list
(`sdk/confined.go:61-93`), and it may not search the org's documents into a
prompt (`sdk/confined.go:95-110`). Making model calls as cloud's own identity, in
effect one credential for the whole deployment, was tried and removed: it billed
every customer's run to the admin org and let code in the box spend on Hanzo's
account (`apps/coding/key.go:37-47`).

**Verdict.** The two agree that the inference credential is not the caller's API
key and not the worker's. They disagree on its scope: per deployment against per
person and run. The OAuth-exchange callback, which is told the calling user, is
where a per-person Hanzo token could be minted, so this is a design question
rather than a conflict. A per-deployment tenant header cannot be that design,
because Hanzo never takes an org from a header.

### 1c. Environment identity — needs-design

**Anthropic.** A worker polls with its environment's key, and the server never
connects to a worker (`MA/self-hosted`, "What runs where"). The key lasts 365
days and is rotated by minting a new one and revoking the old
(`MA/self-hosted/kubernetes`, "Renew keys and certificates"). Git credentials
are added by the server on the way out and never reach the sandbox
(`MA/self-hosted/differences`, "Git repositories need an allowlist entry").

**Hanzo.** Shipped:

- A sandbox lease carries the **leasing caller's own** token, exchanged at IAM
  (RFC 8693) for the lease's remaining life, with no refresh. It is delivered on
  stdin of an exec, never in the pod spec. Nothing is minted on Hanzo's
  authority, and a plane call, which carries an attested caller rather than a
  token, delivers nothing (`apps/sandbox/LLM.md:28-86`).
- The pod gets no ServiceAccount token and no service environment
  (`apps/sandbox/runtime.go:1222-1275`).
- A harness gets two separate values: the inference token and the run's ticket
  (`HANZO_RUN`), which is the only door to the org's MCP servers. No MCP server's
  own credential is handed to it (`apps/coding/key.go:659-671`).
- A run routed to an org's own machine carries **no** credential. Offers are
  keyed by `(org, target)`, and the machine authenticates with the credentials
  it already holds, the same ones `hanzo code` uses
  (`apps/agents/mailbox.go:32-40`). A node authorizes once, at its socket, in the
  org the gateway validated (HIP-1325 §3-4).

Specified, not shipped: HIP-1347 §1.1 (Draft) requires workload actors, per-run
inference keys, per-run MCP tickets, sandbox lease tokens and **per-environment
worker secrets** to stay cryptographically separate.

**Verdict.** Same principle on both sides: the thing that executes holds a
narrow credential of its own, and the shell never holds the worker's. Hanzo's
gap is the worker credential. A routed-run machine or a node authenticates as
the person signed in on it, and there is no per-environment credential that can
claim work and nothing else.

## 2. Event-driven bots against a long-lived agent loop — needs-design

**Anthropic.** The API is the hosted one: create a session against an
environment, post events to it, stream them over server-sent events. A session
persists, goes `idle` at the end of a turn and is resumed by the next
`user.message` (`MA/sessions`; `MA/events-and-streaming`, "Resume an idle
session"). The agent loop runs **in the server**, and the server never runs
shell or file tools; workers poll for tool calls (`MA/self-hosted`, "What runs
where"). Self-hosted, model turns run even with no worker polling; a tool call
nobody claims for about three minutes pauses the session. There are no webhooks
and no scheduled runs (`MA/self-hosted/differences`). On Kubernetes a sandbox pod
lives at most about an hour, idle pods go after five minutes, and the session's
`/workspace` volume outlives the pod (`MA/self-hosted/call-the-api`;
`MA/self-hosted/upgrade`).

**Hanzo.** Shipped, a run starts on an **event**:

- a `POST` (one-shot), or a five-field cron for `long-running` agents, which the
  scheduler fires through the same run path (`apps/agents/agents.go:208-218`;
  `apps/agents/scheduler.go:3-26`). "Long-running" means scheduled, not resident.
- The tasks engine is the queue of record for a routed run; the in-memory
  mailbox is only a rendezvous (`apps/agents/mailbox.go:14-21`).
- A paid sandbox with its own disk is **parked** at idle and resumed later; a
  resume restores files, not processes (`apps/sandbox/LLM.md:416-423`). Each
  activation is recorded as warm, relocated or cold, apart from the model's time
  to first token (`apps/sandbox/activation.go:1-14`).

HIP-1347 §1.2 (Draft) makes it a rule: bots and durable sessions wake on cron,
webhook, channel or user events, park at idle, and need no permanently running
agent process. Two things are still resident: the bot runtime is one gateway pod
(deployment repository `charts/app/values/hanzo/bot-gateway.yaml:8`, one
replica), and shipped coding runs put the harness, the loop included, **inside**
the sandbox (`apps/sandbox/LLM.md:269-281`;
`apps/coding/sandboxrunner.go:224-240`). HIP-1330 (Draft, no `apps/dev` on main)
moves the loop out: the loop holds conversation state and calls models, and the
sandbox only executes, which is Anthropic's split.

**Verdict.** At the session level the designs agree: a session idles, an event
resumes it, and the sandbox can come and go under it. Two seams are missing.
Self-hosted Anthropic has no triggers of its own, so Hanzo's cron, channel and
webhook events would have to arrive as posted events. And its workers are
long-lived pollers, where Hanzo leases a sandbox per run and parks it.

## 3. Per-org encryption and policy scope — incompatible across orgs

**Anthropic.**

- **Keys are per deployment.** Credentials stored in the database are encrypted
  with one key per deployment (`store.credentials_encryption_key`), and vault
  secrets can live in a cloud secret manager instead
  (`MA/self-hosted/configuration-reference`, "Database";
  `MA/self-hosted/agent-identity`).
- **Encryption of the database as a whole is not described.** The upgrade guide
  notes that a database dump holds every conversation in plain text
  (`MA/self-hosted/upgrade`).
- **Policy is configuration of the deployment:**
  - egress mode `closed`, `publicInternet` or `gateway`, with an exact-hostname
    list for the gateway (`MA/self-hosted/kubernetes`, "What sandboxes can
    reach"; `MA/self-hosted/agent-identity`);
  - hostname lists for MCP servers and git, and web-tool domain lists that apply
    to every agent (`MA/self-hosted/differences`;
    `MA/self-hosted/configuration-reference`).
- **Per-tool approval** is the one finer setting (`permission_policy`, `MA/tools`).

**Hanzo.**

- **Keys are per org.** An org's SQLite store has its own key, derived from the
  KMS-held master and the store's name (`internal/driver/postgres/LLM.md:642-660`).
- **Production rows sit in an org's own schema** on the shared Postgres. Fields
  that are credentials or personal data are sealed; the rest relies on volume and
  network encryption (`internal/driver/postgres/LLM.md:21-30`, `446-449`).
- **Secrets live in KMS**, at a path built from the validated principal
  (`LLM.md:101-105`).
- **A workspace Disk is sealed under its own key** before any byte reaches the
  object store (HIP-1342 §3). Storage work must never share deduplication or
  caches across orgs (HIP-1344 §2.4 and "Security Considerations"), and neither
  may model KV caches (HIP-1345 §1.1).
- **Policy is per org.**
  - Zero data retention and residency come from the org's settings
    (`apps/settings/settings.go:115-145`), and the model gate refuses a route
    that does not meet them (`apps/ai/capability/gate.go:220-258`).
  - A harness gets only the tools an org admin activated
    (`apps/coding/sandboxrunner.go:216-223`).
  - **The exception is sandbox egress.** It is one policy for the whole sandbox
    namespace: ingress only from cloud, egress DNS plus 80/443 to public
    addresses (deployment repository
    `charts/app/values/hanzo-sandboxes/sandbox-isolation.yaml:17-29`, `43-60`).
    A per-host allowlist exists only as a model in a test
    (`apps/sandbox/egress_policy_test.go:11-14`).

**Verdict.** Every key and policy in Anthropic's server is per deployment.
Sharing one deployment across Hanzo orgs would put their stored credentials
under one key and their agents under one egress and tool policy. Both sides leave
conversation content to volume encryption, so neither has an edge there. Hanzo
has a gap of its own: Anthropic's exact-hostname egress list has no per-org
counterpart.

## 4. State, sandbox and filesystem

### 4a. State — compatible

**Anthropic.** PostgreSQL 16 or later holds agents, sessions and their history.
Without it, state is in memory and lost on restart. Sessions are kept until
deleted (`MA/self-hosted`; `MA/self-hosted-data-and-access`, "How long data is
kept").

**Hanzo.** Production state is the shared Postgres (`hanzo-sql`), one schema per
org and subsystem. A database is bound to one deployment, and a second
deployment is refused (`internal/driver/postgres/LLM.md:21-40`). An org's
agents, runs and sessions are that org's store (HIP-1210 "One store, one org,
one file"). Durable runs are the tasks engine's (HIP-1330 "A session is three
facts").

**Verdict.** Same kind of store, kept server-side on both. They must not share a
database, and Hanzo's binding of a database to one deployment would refuse it
anyway.

### 4b. Sandbox isolation — compatible

**Anthropic.** The worker does not isolate the shell; what it runs in does. Each
session's worker gets its own container or pod, and a worker started by hand
belongs in a disposable container or VM (`MA/self-hosted`, "Your security
responsibilities"). The Kubernetes chart requires gVisor nodes or another
sandboxed runtime and a network plugin that enforces NetworkPolicy
(`MA/self-hosted/kubernetes`).

**Hanzo.**

- **Visor is the runtime boundary.** It is Hanzo's fork of gVisor: a container's
  processes run against a user-space kernel, and files reach them only through a
  separate process (HIP-1343 abstract, §1, "Security Considerations").
- **Visor is the floor.** An unset or unknown runtime setting still gets an
  isolating runtime. The node's own kernel (runc) is kept to Hanzo's own code, on
  a pool of its own (`apps/sandbox/runtime.go:538-553`, `714-735`).
- `apps/sandbox` itself executes nothing (`docs/sandbox.md:108-114`).

**Verdict.** Same principle, and the same runtime family: the runtime is the
boundary, never the process inside it. The difference is what sits inside the
sandbox. Anthropic's loop runs in the server, so no model credential is ever in
the sandbox. Hanzo's shipped harness runs in the box and so holds a confined
inference token (row 1b). HIP-1330 would end that.

### 4c. Filesystem and VFS — needs-design

**Anthropic.** On Kubernetes, `/workspace` is a 10 GiB volume per session that
outlives the pod and is freed on archive or after a retention period; under
Compose it goes with the container (`MA/self-hosted/call-the-api`;
`MA/self-hosted/kubernetes`, "Free a session's volume"). There is no Files API;
data moves through the worker's working directory, and a memory store is copied
into `/mnt/memory` (`MA/self-hosted/differences`). No encryption of the volume is
described.

**Hanzo.**

- **The Disk (HIP-1342).** A Disk is a filesystem on an object store. Every
  object is sealed under the Disk's own key. One writer at a time is fenced by a
  monotone round, and the whole state commits through one conditional write.
- **How a sandbox gets one (HIP-1343 §2-3).** It is served as an ordinary
  directory through visor's file process. The sandbox gets no store credential,
  no Disk key and no mount privilege, and an acknowledged `fsync` means the
  Disk's daemon has the write.
- **Not built:** the node agent that mounts a Disk for a sandbox (HIP-1343 status
  table). Checkpoints to object storage are Draft (HIP-1343 §4).

**Verdict.** Anthropic's `/workspace` is a plain volume, so it carries none of
the Disk's per-Disk sealing, single-writer fence or `fsync` guarantee. To give an
Anthropic sandbox a Hanzo workspace, the node agent would mount the Disk on the
host and hand the pod a directory, which is HIP-1343 §2 with the missing piece
built. A FUSE mount inside the sandbox is forbidden by §3 and is not an
alternative.

## 5. Model data leaving a "self-hosted" harness — needs-design

**Anthropic.**

- **The conversation goes to the model provider.** The server runs the loop, the
  sandboxes and the state on the customer's infrastructure, but every model call
  carries the conversation so far (prompts, tool inputs and tool results) to the
  inference provider: the Claude API, Amazon Bedrock, Claude Models on Google
  Cloud, Microsoft Foundry, or the customer's own gateway
  (`MA/self-hosted-data-and-access`, "What the server sends to its inference
  provider"; `MA/self-hosted/inference-provider`).
- **Anthropic is contacted only when the provider is the Claude API.** The
  server reaches Anthropic in no other case, and Anthropic has no remote access
  to it (`MA/self-hosted-data-and-access`, "What leaves your network").
- **What else goes with a Claude API call:**
  - session, agent and environment ID headers that cannot be turned off;
  - server-side model fallback, on by default, which swaps the model without an
    event;
  - web tools, which the provider runs (`MA/self-hosted/differences`).
- **Retention and residency follow the provider.** Retention is the agreement
  with whoever serves the model. Residency is `inference_geo` with the Claude API
  and the provider's region otherwise (`MA/self-hosted-data-and-access`;
  `MA/self-hosted/differences`).
- **What the public license adds.** It restricts the server to Anthropic models
  unless the documentation allows otherwise (§3). It also says the software may
  send usage signals to Anthropic that a licensee may not block (§5.9), and the
  data docs do not enumerate them (license page, listed below).

**Hanzo.**

- **Hanzo's harness sends the same data to Hanzo's model API.** It calls the
  model routes with its confined token (row 1b).
- **The model API routes each model to Hanzo's own engines or to a third-party
  provider.** Routing is stated per route, not per model, together with what each
  route retains and where it keeps data
  (`apps/ai/capability/capability.go:15-35`).
- **An org's policy is checked before dispatch.** The gate refuses a route that
  keeps data when the org requires zero retention, or that keeps it outside the
  org's region (`apps/ai/capability/gate.go:220-258`). Today no route to a Claude
  model meets a residency requirement (`apps/ai/capability/records.go:24-26`).
- **Model data stays in an org's own network only on its own GPUs.** That is
  `hanzo-engine` on a GPU the org connects, which speaks both the OpenAI and the
  Anthropic `/v1/messages` wire (`docs/bring-your-gpu.md:110-116`).

**Verdict.** "Self-hosted" moves the control plane and the sandboxes. It does
not move the model: with Claude models, the conversation still reaches Anthropic
or a cloud that serves Anthropic's models, and the license keeps the server on
those models. Hanzo's model API already speaks the request format a `gateway`
provider sends (`/v1/messages`, `openapi.yaml`). What is missing is the policy
seam: a call arriving from a customer's Anthropic server would need the right
person and org, and the org's retention and residency gate applied to it, as
rows 1b and 3 describe.

## Recommendations

### Keep

1. **The per-person, per-run inference token**, confined to model routes and
   renewed by cloud (`sdk/confined.go`, `apps/coding/key.go`). One credential
   for the whole deployment is what Hanzo already removed, because it billed
   the wrong org and exposed Hanzo's own spend.
2. **The org from the validated principal only.** No tenant header, body field or
   per-deployment label selects an org.
3. **Visor as the floor**, no ServiceAccount token in a sandbox, and a workspace
   that arrives as a directory with no store credential (HIP-1343 §2).
4. **The event-driven lifecycle:** the tasks engine as the record, park and
   resume, and activation timed apart from the model.
5. **Per-org keys and per-Disk sealing**, and the per-org retention and residency
   gate on model routes.

### Interoperate, if anything

1. **Already there: Anthropic's coding CLI as a harness.** `claude` is one of the
   harnesses Hanzo runs in its own sandboxes, with the confined token and only
   the org's activated tools (`apps/coding/sandboxrunner.go:216-235`). That is
   Claude Code, not the Managed Agents server, and it needs no new work.
2. **Possible: Hanzo's model API as the `gateway` provider for a customer who
   runs Anthropic's server.** The wire exists. A safe version needs the OAuth
   exchange to mint a per-person Hanzo token from the user the callback names,
   the org's gate to apply to those calls, and an owner decision on positioning
   and on the license's model restriction (question 1 below).
3. **Not recommended now:** running Anthropic's server inside Hanzo as a service
   for many orgs, which its own docs rule out; or serving the Managed Agents API
   shape from Hanzo. No customer has asked for it, and HIP-1330's `/v1/dev` is
   the surface to finish first.

### Gaps that would be unsafe to blur

1. **One deployment for several orgs.** The server key, the `admin` role, the
   credentials key and every egress and tool policy would span them.
2. **A shared inference credential standing in for a person's.** Spend lands on
   the wrong org, and a stolen token spends for everyone.
3. **The environment key and a person's token.** Anthropic keeps the environment
   key out of sandboxes, and HIP-1347 keeps worker secrets apart from lease
   tokens. Hanzo's routed-run machines and nodes still authenticate as a person
   (row 1c), so do not let the two converge.
4. **The worker as the isolation boundary.** Anthropic's own docs say it is not.
   An Anthropic worker on a Hanzo node still runs under visor.
5. **Store credentials or Disk keys inside a sandbox**, including a FUSE mount
   there (HIP-1343 §2-3).
6. **"Self-hosted" read as "the model data stays".** It stays only where the
   model is served, which for Claude models is outside the customer's network.

### Hanzo gaps this comparison found

These need no decision from Anthropic's side:

- **A per-environment worker credential:** claim work, report results, nothing
  else (HIP-1347 §1.1, not built).
- **Per-org sandbox egress allowlists.** Today one policy covers the namespace.
- **The loop outside the sandbox** (HIP-1330, Draft), so that no model credential
  sits in a box that runs a model's output.
- **The node agent that mounts a Disk into a sandbox** (HIP-1343 §2).

## Questions for the owner

1. **Should Hanzo offer its model API as an endpoint for customers running
   Anthropic's self-hosted server?** It runs into the brand rule against
   presenting Hanzo as a gateway (`LLM.md`, "Brand rules"), and into the public
   license's restriction to Anthropic models.
2. **Should the per-environment worker credential be an IAM application per
   environment?** It would be org-bound and able only to claim routed runs and
   report on them, so routed-run machines and nodes stop authenticating as a
   person.

## Anthropic sources

All public, read 2026-11-19. None of the documentation pages shows a
last-updated date.

| Page | Address |
| --- | --- |
| Self-hosted Claude Managed Agents | `MA/self-hosted` |
| Data and access in self-hosted Claude Managed Agents | `MA/self-hosted-data-and-access` |
| Differences from the hosted service | `MA/self-hosted/differences` |
| Inference provider | `MA/self-hosted/inference-provider` |
| Deploy on Kubernetes | `MA/self-hosted/kubernetes` |
| Single sign-on | `MA/self-hosted/configure` |
| Agent identity and credentials | `MA/self-hosted/agent-identity` |
| Call the API on a self-hosted server | `MA/self-hosted/call-the-api` |
| Configuration reference | `MA/self-hosted/configuration-reference` |
| Upgrade self-hosted Claude Managed Agents | `MA/self-hosted/upgrade` |
| Self-hosted sandboxes; security model | `MA/self-hosted-sandboxes`, `MA/self-hosted-sandboxes-security` |
| Overview; sessions; session event stream; tools | `MA/overview`, `MA/sessions`, `MA/events-and-streaming`, `MA/tools` |
| Release notes (entry of 2026-11-12) | `https://platform.claude.com/docs/en/release-notes/overview` |
| Software License Agreement: Claude Managed Agents Server (effective 2026-11-12) | `https://www.anthropic.com/legal/self-hosted-managed-agents-license` |

The self-hosted pages tell organizations with access to sign in for more. This
write-up uses only what a reader who is not signed in can see.
