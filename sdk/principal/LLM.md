# LLM.md — sdk/principal

Guidance for sdk/principal. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Support sessions: IAM impersonation, contained (`impersonation.go`)

IAM (iam LLM.md "Impersonation") signs a SuperAdmin in AS a person: the person's
sub/owner/orgs, `act {sub, owner, name}`, `imp: true`, ≤15 min, no refresh. Off until
IAM sets `IAM_IMPERSONATION_APPS=hanzo-app,hanzo-cloud`; cloud's half below must ship
first. Both clients are shared: `hanzo-cloud` also signs in console, admin, sentry,
growth, cloud, api, tracker, meet and todo.hanzo.ai; `hanzo-app` also hanzo.app,
hanzo.team, hanzo.bot, dev/commerce.hanzo.ai and the desktop app (`hanzo://`). A
support token is accepted on every one of them; containment is cloud's, by address,
so it holds on all.

**Start one.** `POST /v1/admin/users/:org/:name/impersonate {site, reason}`
(`apps/admin/customer/impersonate.go`): `core.Change` (SuperAdmin + CSRF), 503 with no
audit store. Forwards the operator's bearer unchanged (`cloud.CallerBearer`) and
`geo.IP` as `X-Forwarded-For` to IAM `POST /v1/iam/impersonate`
`{target, reason, clientId}`; `site` `platform` → `hanzo-cloud`, `hanzo.ai` →
`hanzo-app` (the clients those sites sign in as); hosts and `iss` from
`brand.For(s.Brand)`. Relays IAM's status and `msg`;
200 → `{"status":"ok","url":"https://<host>/auth/initiate?iss=…&login_hint=…"}`, handed
out only after `admin.user.impersonate` {operator, target, site, client, reason} lands
on the trail. No gate of cloud's own: IAM decides.

**Identity.** The boundary reads `act` off the verified payload onto
`principal.Principal.Act` (`principal.Impersonated(c)`; `ActFrom(ctx)` for typed ops via
Bridge, or the plane's `X-Acted-By` = act.sub). Authorization reads sub/owner/orgs only.
An `imp` token authenticates nobody unless IAM's rule holds on it too (`impersonation`
in auth_identity.go): an operator named, `act.owner == "admin"`, and a target that is
neither a SuperAdmin (`sudo`) nor `Machine()`. `cloud.As` carries the operator as
`ActedBy` on every delegated call, so a callee in another process contains, frees and
audits it the same way.

**Refused** (403 "a support session cannot change …"): every non-GET under the `support`
table — API/SSH/principal/daemon keys, webhooks, connections and provider
connect/claim, AI providers, sessions, KMS, codebase secrets, all of `/v1/iam`,
`/v1/account/{orgs,avatar,appearance}`, all of `/v1/billing`, plan subscriptions, card
charges, domain purchases, transfers, payouts, wallets, and acts in the person's name
(e-sign, EIN, formation advance, KYC, W-9 and tax certification, the cap table and
fundraising, document signatures, legal and tax filings, 1099s, all of
`/v1/compliance`), commerce wallets, store trials, tunnel credentials;
links, tokens and invitations (data-room and chat share links, affiliate links,
e-sign recipients and send, sandbox previews, all of `/v1/meet`); publication (the
trust centre, `/v1/trust`, marketplace listings, the affiliate handle and leaderboard
opt-in, research grants, repo and project visibility, project publish and activate,
social posts, the marketing calendar, content publish, workers.dev subdomains, ad and
campaign launches); and ALL of `/v1/o11y` (dashboards and their public links, alerts,
channels, ingestion and service-account keys, roles; its POST queries too, so a
dashboard's panels do not render in a session) —
plus any read in `custody` (a connection's token, cloud-integration credentials, IAM
tokens). A pattern is an address and everything under it, or `METHOD address` for
that one operation. Matched as the router reads the address and as a net/http handler
would (decoded, cleaned). `Contain` (inside AuditTrail, `Witness`) asks the address;
`Confine` (in `Rule`) asks the operation a door reached. Not contained, by design or
pending a decision: deploys (`/v1/projects/*/deploy`, `deployments`, `sites`, platform
apps, Cloudflare scripts, functions), creates whose default or flag is public (a
project, a repo with `public`, a benchmark claim, an agent session's public story),
broadcast sends (marketing campaigns and sequences, `/v1/transport/*/send`), and
session-length tickets (terminal and screen 30 s, presigned S3 5 min, registry pull
token).

**Ending itself.** The one IAM write it keeps (`ending`): `POST /v1/iam/oauth/revoke`
whose `token` (read as IAM reads it: query, then form) is its own bearer, and `POST
/v1/iam/oauth/logout` with no `id_token_hint` or one naming the same person and
operator. Any other token, method or spelling is refused. Cloud validates JWTs without
a revocation list, so a revoked support token still passes cloud until its `exp`
(≤15 min); IAM's Guard and introspection refuse it at once.

**o11y.** Its main is hand-written, so it opened no trail: `cloud.Witness` (Trail,
AuditTrail, Contain, as Listen installs them) with `cloud.Recorder(cfg, …, "o11y",
false)`, every record appended to admin's chain over the plane. A session's reads there
are on the trail with `act`; so is every POST from anyone, as in every other app. Floors where an RPC body or cloud's own
IAM credential hides the act: `apps/account` `supported` (every non-GET `iamClient.do`)
and `apps/team` `refusedToSupport` (`sendInvite`, `selectWorkspace` — a 12 h unmarked
space token). KMS values already need a named holder. x402 (`supporting`) refuses a
priced settle and a hold: the ledger draws the payer ORG whoever signed, so a signature
made in the window is no consent of the person's; free calls pass.

**Billing.** Gates still weigh the person's balance; the debit is skipped at
`BillingGate`, `Toll` and `Charge.Debit` (`Payer.Operator`). Still billed to the
person: hanzoai/ai's model lane (`apps/ai` `debit`/`record` ← `UsageRecorder`, called
with `context.Background()`; needs ai to carry `X-Acted-By` onto `UsageEvent`), free
allowance counting (`admit`), `meteredAI` (in-process inference), and every direct
`Meter.Record` (no ctx).

**Audit.** `audit.Actor.Act` (`omitzero`, hash-compatible; columns `act_sub/owner/name`;
`client.AuditRecord.Act*` appended last). AuditTrail records EVERY request of a support
session, reads included; `Recorder.Acting(actOf)` stamps hand-built rows from the call's
context. `Filter.Impersonated` = org switch OR support session. The OLAP mirror carries
neither `home` nor `act`. Rollout: the trail's holder (admin) ships before the senders,
or a sender's `ActSub` reaches a holder that drops it.

## Delegated tokens: a run bills and acts as its person, for model calls only (`confined.go`)

**Which dev (`renewsFrom`).** The box's dev is asked `dev --version` once per flight, before the
first turn. The override needs a dev that merges `-c model_providers.hanzo.auth.*` into its provider
table; older ones replace the table and exit 1 on `missing field name` before a model call. Measured
against the published release binaries (linux aarch64, sha256 verified) with the endpoint on
localhost, HANZO_API_KEY set and the command pointed at a file: 0.6.94, 0.6.100 and 0.6.108 fail;
0.6.115, 0.6.118, 0.6.119, 0.6.120, 0.6.122, 0.7.0, 0.7.9, 0.7.10 and 0.7.14 send the file's bearer
and, answered 401 with the file replaced, ask the command again and send the new one (no release
exists between 0.6.108 and 0.6.115). `renewsFrom` is 0.6.115. A dev older than that, or whose answer
cannot be read, takes the path claude and codex do, and the run's steps say why.

**A harness that cannot renew** (claude and codex read the credential once; a dev older than
`renewsFrom`) holds the one token it was started with. When IAM clipped that token to less than
the run (`Delegated.Capped`: the four hours of `delegationTTL`) the run is ended a `leaseMargin`
short of it with its work kept and the true reason, `errUnrenewable` → `RunResult.Unrenewable` →
a status event with `unrenewable` → "ended when its model credential was about to: this harness
cannot renew it" (the thread's last word and the chat turn's); a token that covers the run is no
limit on it. python and node make no model call and are held to nothing: they get the ceiling.

**What a renewal weakens, and what it does not cover.**

- *Blinding.* The old code put the run's one token in `RunIn.Blind` of every exec, so `env` printed
  `[redacted]` in every turn. The new code blinds every token the cloud knows at the START of each exec
  (`turn` reads the token it feeds and the blind list together, under one lock), but a token renewed
  DURING a running turn is not in that exec's `RunIn.Blind` (fixed when an exec starts; a turn can last
  hours). So after the first renewal, about 40 minutes in, `cat /tmp/hanzo-run/model.<session>` in that
  turn publishes the live token to the stream, the Slack thread and the session events; the file's path is
  also visible in dev's argv. That is a net WEAKENING of redaction mid-turn, offset by a shorter token life
  (an hour, where the old token lasted up to four) and by the bounds: model calls only (`confined.go`), the
  person's own account, revoked when the run ends. The real fix is a live-updatable blind list in the
  sandbox plane, which can add to a running exec's list; that is separate work and the follow-up.
- *python and node scripts that call the gateway directly* hold the token they were started with, which
  IAM clips to four hours, for a run that may last a day: after four hours their calls answer 401. The
  run is not ended for it (an interpreter makes no model call of its own).
- *No fence between flights of one session.* A flight's timer writes the credential file and nothing stops
  an older flight's timer from writing over a newer one's. It cannot happen while one pod holds a session
  (`agents.Hold`; the boot Resume touches only runs no pod holds). A pod that is partitioned and alive
  while another resumes its run would break it, as it would already break a push.
- *The audit trail is mandatory* for a deployment that holds an IAM identity: the first mint is on the
  trail before the token reaches a box, and a process with no trail (`CLOUD_AUDIT_DISABLED`, no audit
  store) refuses every coding run, as item 2 refuses a relaunch. A deployment with no IAM identity mints
  nothing and is unaffected.

**What the token is.** The person (`sub`, username), `orgs` = [run org] only (so
X-Org-Id cannot switch it, Home = run org, never `admin`, never Sudo), `owner` = run
org, `aud` = https URI, `azp` empty, `act {sub: admin/hanzo-cloud, …}`, ≤ 4h, no
refresh. So `principal.BillingOrg` = run org and `principal.Payer` =
`account.Payer{run org, username}` — the pool in a tenant org, `hanzo/<name>` in the
signup org — exactly as the person's own token switched into that org; hanzoai/ai
reads the same (`sdk/iam` homeOrg = orgs[0]) for its gate, lift and debit, and
its `UsageEvent.Actor` is `<run org>/<username>`. No ai change.

**Confined.** `confinedScope(claims.Scope)` (the issuer's `schema.Confined`; cloud pins
an iam without it, so the word is restated once in confined.go) sets
`idClaims.confined`. `admits` then allows ONLY `POST /v1/responses`,
`/v1/chat/completions`, `/v1/messages`, `/v1/messages/count_tokens` and `GET|HEAD
/v1/models[/…]` (`modelCalls`, narrower than spend.go's `inference`) — 403 everywhere
else, doors (MCP, call plane, graph) included, logged `identity: a delegated token
reached past model calls`. A confined token that fails verification is refused
(`presentsConfined` reads the unverified payload only to choose the path), never anonymous.
A confined model call that asks for retrieval is 403 (`noRetrieval`): it would
search the org's document index into the prompt. The question is ai's own
(`github.com/hanzoai/ai/retrieval.Asked`, the leaf its controllers answer it with),
so no copy here can fall behind a spelling ai adds.

**Kill switch.** Verified offline, a confined token would outlive anything IAM
did to its grant. So the boundary also asks IAM whether it stands (RFC 7662
introspection as hanzo-cloud, `liveness` in delegation.go) on every request,
cached hit included, remembering each answer 30s (bounded 4096): revoked → 401
"was revoked", IAM unreachable → 503 "cannot confirm … retry". The run revokes its
token when `Run` returns (`apps/coding` `end` → `cloud.Revoke`, RFC 7009,
cancel-immune, 10s; a failure is logged and the token still dies at its
deadline), so a copy lifted from the box is dead ≤ 30s after the run.
NOT bound to the sandbox network: `api.hanzo.ai` resolves to the public
100.24.244.37, ingress is ClusterIP + NodePort, and CoreDNS rewrites only
`ssh.hanzo.ai` in-cluster (universe infra/aws/k8s/dns.yaml) — so a run's model call
leaves the pod network and arrives from the cluster's egress, with no pod CIDR on
the path to bind to, and no existing config names that egress. Once `api.hanzo.ai`
is rewritten in-cluster like `ssh.hanzo.ai`, a sandbox call resolves to no client
address (`ClientIP == ""`, sdk/geo/ip.go) and `admits` can require that of a
confined token in one line.

**Attribution.** `act.sub` (no `imp`) → `idClaims.agent` → `principal.Principal.Agent`
→ `audit.Actor.Agent` (`omitempty`, hash-compatible; column `actor_agent`;
`client.AuditRecord.Agent` appended last; `Wire.agent`). Every model POST a run makes
is on the trail as the person, in the run's org, with the agent. It is NOT
`Principal.Act` (the impersonation operator), which would zero the debit. Refusals at
the boundary are in the request log and the warn line, not the trail (same as a key
grant's). Edge verification allows 2 min clock leeway past `exp`.

**Rollout.** Production IAM is the one EMBEDDED in this binary (IAM_URL =
127.0.0.1:8000), so it ships in one cloud release: go.mod `hanzoai/iam` at v1.34.150
or later (v1.34.150 is the tag on iam `run-delegation`'s tip), and in universe `cloud.yaml`
`IAM_DELEGATION_APPS=hanzo-cloud` and `IAM_DELEGATION_AUDIENCES=https://api.hanzo.ai`,
the `cert-delegation` Cert row (`scope: delegation`) in init_data.json, and its PEM
in KMS feeding the `iam-signing-keys` Secret (file name = Cert name). Missing any
of them fails every coding run that has a machine identity, never falls back.
The bump adds one untyped route (`GET /v1/iam/oauth/delegation/keys`, iam 64 → 65 in
openapi/untyped.json): it authenticates the client by HTTP Basic on a GET (RFC 6749
§2.3.1) and answers a JWKS document a JOSE library reads by address (RFC 7517) —
the shape of the public JWKS beside it, which is untyped for the same reason.

**A run's sandbox is paid by its request's payer** (`apps/coding` `paying`). The run
context states only the tenant, so `paying` states the request's `Name` and
`billing_account` claim on it, the pair `account.Payer` reads beside the org (the
same pair `cloud.Detach` carries), and a steered follow-up states what its run had.
Without the name a token's run leased on `<org>/<sub>`, a wallet nothing funds, and
was refused "this wallet holds 0" while `<org>/<username>` held money
(`wallet_test.go`). Resume after a rollout still states the subject alone.

In the signup org who pays is IAM's claim: `org:hanzo` for a machine or an owner or
admin of the home org, else `hanzo/<name>`. IAM reads the home role off the user
row's `IsAdmin` (iam `store.HomeRole`); a membership row for the home org is
ignored. So a person's key pays their own wallet everywhere, the run included.

**A key's sessions are `<org>/<owner>/<name>`** (`hanzo/hanzo/z`): `billingActor`
prefixes the org to a subject that is the holder's row. It is not a doubled prefix
to collapse: a username may be a UUID, so `hanzo/<name>` would equal a person's
`hanzo/<sub>` and own their sessions (`apps/agents/owner_test.go`).

## A person's things in a shared org

Every self-serve customer was a member of `hanzo`, so anything scoped by org alone
was every customer's. The rule, one per store: a row belongs to the subject the
identity boundary attested (`principal.SubjectFrom(ctx)`, `principal.SubjectOf(Minted)`;
a token with no stable subject owns nothing), and a row with no recorded owner is
nobody's: a SuperAdmin reads it (the audit middleware records every SuperAdmin
request) and nobody acts on it. In a held org (`authz.IsHeldOrg`) an org admin is
one more stranger to what members made; org infrastructure stays the admins'.

| Store | Owner | Reads | Changes |
|---|---|---|---|
| agent runs (`apps/agents` `runScope`) | `Run.Actor` = `org/subject` | the person; schedule/empty runs a SuperAdmin | — |
| sessions (`reader`, `actable`) | `Session.Actor`, from the subject, never the body | the person, an admin of a customer org, a SuperAdmin; empty actor a SuperAdmin | not on an empty actor |
| agents (`manages`) | `agents.created_by` | the org | creator, admin of a non-held org, SuperAdmin |
| channel inbox (`listInbox`) | `sender_user` (the sender's own link; no transport sets it yet) | the person; none a SuperAdmin | — |
| knowledge files (`actor.manages`, `readsFile`) | `files.uploader` | the org — but in a held org the uploader (plus a SuperAdmin), not other members | uploader, admin of a non-held org, SuperAdmin |
| buckets, provisioned resources | the org | the org | org admin or SuperAdmin (`cloud.Admin`) |
| held-org storage (`internal/fare` `held`) | the org | in a held org an org admin, a SuperAdmin or the org's own app; a customer org's members share it | same |
| the S3 STS gateway (`plugin/s3/iam.go` `role`) | the org | in a held org the org's own application alone (`oidc:owner`+`oidc:type=application`); a customer org's members assume it | — |
| chat conversations (hanzoai/agent) | `User` = subject | the person; none a SuperAdmin | the owner |

A held org's object storage (`/v1/s3`, `/v1/space`) and its STS role are reached
only by the org's admins, a SuperAdmin or its own application — `hanzo` holds the
CI cache every release restores and the model weights, and every self-serve
customer is a member, so membership is no claim on them.

**A 200 to another org's selection is the caller's own org.** The boundary
discards an `X-Org-Id` its signed membership does not open (`cloud.Acts`) and the
request acts in the caller's default org, so an admin of org B who asks for org A
is answered B's buckets, 200, and none of A's. Judge isolation by what the store
was asked and what came back, never by the status: `apps/s3/tenancy_test.go` does
that for every `/v1/s3` address and every kind of caller. `fare.held` is still
load-bearing: an API key and an application act in the org their credential
names without the membership check, and a plain member of a funded org (adnexus,
bootnode, osage) opens it. The stdout request line's `org` is the org the client
SENT, read before the boundary; the shipped log record's `hanzo.org` and the
audit row's `org` are the org the request acted in.
