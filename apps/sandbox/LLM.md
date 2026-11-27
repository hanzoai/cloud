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

## The fourth answer: the sandbox is signed in as the owner who leased it

Every lease now carries a token for the caller who asked for it, and it clears the
bar `cred.go` set: nothing is minted on an authority of ours.

```
create call  →  the caller's own bearer  →  RFC 8693 exchange (IAM)
                                            subject = the caller
                                            life    = the lease
                                            refresh = none
```

**The caller's token IS the authorization.** IAM mints for the subject the
presented token names, so this path cannot reach an identity that did not just
call — there is no "act as user X" anywhere in it, and a sandbox holds exactly
what its owner already held. That is the property the three reverted designs
lacked and the `<org>-agent` proposal this section used to describe could not
have: an org machine identity is a NEW credential, one that exists whether or not
anybody asked for it, and it authenticates as the org rather than as a person.

**It dies with the lease.** The exchange asks for the lease's own remaining life
(IAM's `lifetime`, clamped one way — a request can shorten a token and never
lengthen it), and token exchange issues no refresh token, so nothing inside the
pod can renew what it holds. A 15-minute `exec` sandbox gets a 15-minute
credential.

**It arrives through the exec channel**, so it exists in the container and in no
Kubernetes object: an ordinary Pod spec still states no `env` key at all, which
is what `TestAnOrdinarySandboxIsHandedNothing` has always asserted and still
does. `hanzo` is handed the token on STDIN — never argv, which `ps` and /proc
publish to every process in the pod — and keeps its own store. `git` is pointed
at that store through a credential helper SCOPED TO THE FORGE HOST, so there is
one credential in the pod and it is offered nowhere else.

**An identity outage is not a sandbox outage.** The exchange reaches IAM and the
delivery reaches the pod, and neither failing takes the lease with it — the pod
holds nothing, which is the sandbox everybody had before this existed. Refusing
would turn one unreachable dependency into a total sandbox outage while denying
nothing that is not already denied. Both halves are therefore ONE policy, stated
in `Lease` where the lease is decided rather than in the mechanism: loud in the
log, and the lease continues. The SuperAdmin kubeconfig keeps the opposite policy
and still fails its lease, because a caller who asked for the admin image asked
for the toolchain that spends it.

**Two endpoints, one of them silent.** The HTTP endpoint relays `cloud.CallerBearer`. The
agent plane passes "" — a plane call carries an ATTESTED caller, not the caller's
own token, so there is nothing to exchange and substituting a credential of ours
would put an identity in the pod nobody presented. An opaque API key is likewise
not a relayable bearer, so those leases start with no session rather than with a
key in a shell.

**The far end.** `git.hanzo.ai` accepts the same token as a git credential
(hanzoai/forge `services/auth/iam.go`), verified against this deployment's own OIDC
login source and resolved to an account only through the link that sign-in
already wrote — so a subject that never signed in to the forge resolves to
nobody, and nothing is created from a credential. Its scope there is
`write:repository`: a credential that exists so a checkout works administers
nothing.

## The screen, and the three things that were not obvious

A `desktop` sandbox has an X server, and `/v1/sandboxes/:id/screen` is how you
look at it: the terminal's three endpoints again — a ticket, a page, a socket —
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
down: the pool has the CPU and lacks the PLUGIN. `hanzo-inc/universe
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

## Issuing the token — the constraint that decides it

VERIFIED, not assumed: **IAM never discloses an application's client secret.**
`pkg/schema/mask.go:124` — `Application.Mask()` sets `ClientSecret = ""`, and
BOTH `Create` and `Get` return `in.Mask()`. Not masked on read and returned once
on create; never, on either.

So the obvious implementation does not exist. Cloud cannot create `<org>-agent`,
read its secret back, and exchange it for a client_credentials token, because
there is no moment at which it is handed the secret.

ANSWERED — and it is neither of the two shapes below, but the second one's
mechanism, found by reading every route IAM registers:

**`POST /v1/iam/admin/applications/upsert`** (`internal/bootstrap/bootstrap.go:62`)
is authenticated by the SERVICE TOKEN (`HANZO_API_KEY`/`KMS_SERVICE_TOKEN`/
`IAM_SERVICE_TOKEN`, `httpx.ServiceAuth`) and returns the app's clientSecret in
cleartext — including the EXISTING secret when the app is already there
(`resolveSecret`, bootstrap.go:456-467; emitted at :334-337). It is the one
unmasked Application credential in the whole service, and it is deliberate: its
own comment says "this is where it learns a secret it did not send". cloud
already holds that token and already calls this endpoint for `<org>-platform-kms`.

So the sequence is: upsert `<org>-agent` -> read the secret back -> exchange at
`POST /v1/iam/oauth/token` `grant_type=client_credentials`. IAM mints, cloud
delivers, nothing is invented here.

**No route mints an app-subject token on any other authority.** `client_credentials`
(token.go:213) requires that app's own secret, constant-time, with no bypass;
`/v1/iam/tokens/issue` is user-only and refuses an application target
(`mintTarget`, issuetoken.go:248-267); token-exchange yields a USER subject.
Verified by enumerating every route and every `ClientSecret` reference.

**THREE LANDMINES, and the first one means `ensureAgentApplication` is WRONG as
written.** It creates via `POST /v1/iam/applications` with `owner=<org>`. The
upsert looks up hard-pinned to owner `"admin"` (bootstrap.go:252, and sets
`admin/<name>` at :315/:329), so it will not find that row — it will create a
SECOND one. Worse, the bootstrap path does not enforce clientId uniqueness
(`ensureClientIdUnique` is only on the typed CRUD path, applications.go:57-71),
and resolution is admin-preferring (`morePreferredApp`, store.go:64-83), so the
admin row silently wins at the token endpoint. Create it THROUGH the upsert.

Second: an app created through `/v1/iam/applications` with no `clientSecret` is a
PUBLIC client with no secret, which `clientCredentialsGrant` refuses outright
(token.go:226) — and its secret can never be read back, so it is unusable and
unrecoverable. Third: `publicTokenEndpointForbidden` (token.go:230, :776-778)
refuses a name ending `-iam` or an app whose ORGANIZATION is reserved (admin,
built-in, app). `Organization` must be the tenant org. Note it tests Organization
and not Owner, so an admin-OWNED app serving a tenant org mints fine — which is
exactly the shape the upsert produces.

The rejected alternative, recorded so it is not re-proposed: cloud generating the
secret itself and supplying it at create. `Create` does persist a caller-supplied
`ClientSecret` verbatim (applications.go:202-206, no server-side generation on
that path at all), so it WOULD work — and it makes cloud a minter of credential
material, which "IAM is all auth and tokens, nothing else has that responsibility"
forbids. The upsert costs nothing extra and keeps issuance IAM's.

Superseded framing (kept so the reasoning is legible):

  a. **Cloud supplies the secret at creation.** `Create` takes a whole
     `*schema.Application`, so a caller-provided `ClientSecret` is stored. Cloud
     would generate it, seal it in KMS, and exchange it later. Standard OAuth
     client registration — but it means cloud generating credential material,
     which sits close to the line "never build custom auth" draws.
  b. **IAM issues the token for an app it owns**, without the secret leaving.
     IAM mints, cloud receives — which is exactly what the rule asks for, and it
     is a small addition on IAM's side rather than a new authority on cloud's.

(b) is the one that needs nothing bent. It also matches every other credential
here: `cred.go` reads what an issuing service already made and mints nothing.

What is NOT in doubt any more, and cost this session to establish:
- the identity exists and is provisioned per org (`apps/account/iam.go`)
- a client_credentials token of that class is ALREADY denied platform sudo,
  structurally, by `isClientCredentialsPrincipal` — including in the reserved
  `admin` org, which is the escalation attempt #1 was reverted for. There is a
  test for it. No suffix list, no allowlist, nothing to maintain.
- delivery belongs in `cred.go`, beside the SuperAdmin delivery, under that
  file's existing rule
- `apps/sandbox` is a different PROCESS, so it asks over the plane

## The harness

```
hanzo.chat ─┐
hanzo.app  ─┼─→ one agent endpoint ─→ sandbox ─→ `dev` as the harness
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
endpoints are registered in `plugin/`.


There is now exactly ONE endpoint, in `plugin/agents/coding.go`:

```go
zip.Post[plane.CodingStartIn, plane.CodingStarted](cloud.ZipApp(app), "/v1/agents/coding", httpCodingStart)
```

A typed op is four surfaces at once, so that single registration IS the REST
route, the OpenAPI operation, the CLI command and the MCP tool. The plane endpoint
(`coding_start`) is DELETED: its only caller was Slack's `code:` prefix, and a
second way in for one caller is a caller whose model never has to choose.

## The magic word is gone, and here is the evidence it was never the only path

MEASURED on the deployed endpoint before anything was deleted — twice more of this
file's prose had gone stale in the meantime, so read the numbers, not the claim:

```
POST https://api.hanzo.ai/v1/mcp  tools/list
  → 109 tools; the `agents` tool's `op` enum carries **create_coding**

POST .../v1/mcp  tools/call {"name":"agents","arguments":{"op":"create_coding",…}}
  → {"text":"coding: org required","isError":true}      ← startCoding's OWN sentence
POST .../v1/mcp  tools/call {"name":"agents","arguments":{"op":"create_bogus_probe"}}
  → {"error":{"code":-32602,"message":"unknown tool"}}  ← the bogus control
```

The call reaches the handler and is refused by the handler's own fail-closed org
check; a name nobody listed is refused by the endpoint two layers earlier. So the op
was genuinely offered and genuinely dispatchable, and `codingIntent` was pure
subtraction. It is deleted, along with `slack_coding.go` entirely — the parse,
the usage sentence, the target lookup and the dispatch were reachable only from
the prefix. `SLACK_AGENT_REF` did not become an orphan knob:
`channelAgentRef("slack")` reads the same variable for the chat turn.

`post_v1_coding` is published as **`create_coding`**, because the endpoint renames a
derived operation id to the verb phrase it already contains (`fleet/verbs.go`).
That is the string a `tools/call` carries; do not look for `post_v1_coding` on
the wire.

## What actually blocked the model, and it was not the wiring

The description. `zipdoc` strips an exact leading match of the handler's own
name, so `// httpCodingStart is the app's endpoint.` reached every SDK, the document
and the MCP tool list as:

> Is the app's endpoint. It answers 202 with the run's handle the moment the run is
> ADMITTED — not when it finishes…

Every word true; none of it says the thing writes code. A model picks a tool by
reading it, so an operation described by its own HTTP semantics is an operation
nothing chooses — which is exactly what makes a keyword nobody can delete look
load-bearing. The doc comment now opens with the ACT, and
`plugin/agents/coding_test.go` asserts the words a coding request matches on.
Keep the Go idiom (open with the identifier); write the rest for the model.

## The input says what to do and never who to be

`CodingStartIn` carries no org, no subject and no credential. Each left for the
same reason one step later than the last:

- **the credential**, because a token with write access to every repo in the org
  no longer needs to exist outside the engine (`agentCredential` mints a grant
  for ONE ref, AFTER the session names the branch, expiring with the run);
- **the org**, because a caller that can name a tenant can spend another's
  balance;
- **the subject**, because the thing filling in a tool's arguments is a MODEL.
  Attribution-by-argument was sound while every endpoint was an adapter we wrote and
  stopped being sound the moment the op became a tool.

Both halves of the identity are now parameters of `coding.Start(ctx, org,
subject, in, log)`, read off `cloud.Who(ctx)` by the endpoint. The check lives in
the HANDLER and not in middleware, and that is not a style choice: zip dispatches
an MCP `tools/call` and a call-plane op STRAIGHT into the handler
(`registeredOp.direct`), so a middleware guard covers one endpoint in three — the
same hole `apps/exec` was walked through.

`TestNothingInTheInputCanNameWhoTheRunIsFor` walks the type by reflection rather
than listing the fields it does not want, so a field added tomorrow is judged by
the same rule.

## A port is an origin of its own (`web.go`, `preview/`)

`https://sandbox-<24 hex>-preview-<port>.<sites apex>/` serves whatever listens on
that port in the sandbox. The label is longer than a project slug may be (40), so no
published site can claim it. The router (`cmd/cloud`, and `app.go` when fused)
rewrites a request for that host onto `/v1/sandbox/preview/<hex>/<port>/…` ahead of
the mounts; the handler refuses any request whose Host is not the preview host the
path names, so the route never serves a sandbox's bytes under `api.hanzo.ai`.

`POST /v1/sandbox/:id/preview {port}` mints the terminal's 30 s ticket for the door
`<id>:<port>`; `/_hanzo/enter?ticket=` spends it and sets `__Host-hanzo_preview`
(HttpOnly, Secure, SameSite=None, Partitioned) naming an in-memory pass for one org,
one sandbox, one port, 12 h. Replica-local like tickets: a restart forgets passes and
the frame is told `{source:"hanzo-preview", status:401}`. The bytes go over the exec
channel (`socat - TCP:localhost:<port>`); HTTP reuses those connections, WebSockets
are relayed frame by frame (`NextReader` → `NextWriter`, never a whole message in
memory). `pipeWS` returns only after both copies stop: a hijacked fasthttp conn's
Close is a no-op until the handler returns and is pooled after, so its reader is
ended with a read deadline — writing to it after release panics the process. `GET
/v1/sandbox/:id/ports` reads `/proc/net/tcp{,6}` LISTEN.

Bounds, all in the one shared process: a sandbox holds at most `tunnelsPer` (64)
exec tunnels, HTTP and WebSocket together, each released once however it ends
(`relay.dial` → `join`'s `done`), 429 past it; HTTP waits in a queue of
`streamsPer` (16) a port, so a dev server's burst of modules loads; one
`answerWithin` (5 min) bounds a request's queue, dial and headers together and each
silence of a body; a WebSocket not relaying within `handshake` (30 s) of its dial
gives its tunnel back. `kin`: a request naming an Origin is served only from a
preview of the same sandbox, and one naming none crosses sites only as a navigation
(`Sec-Fetch-Mode`), so another `*.hanzo.app` page can neither post to nor load
scripts from a preview with its viewer's pass. The app is handed no platform, proxy
or client-IP header.

## A sandbox with its own disk is parked, not ended (`reap.go` suspends)

When its clock runs out a running, paid sandbox whose disk is its own (`Spec.Disk`,
no project) is parked: pod stopped, disk and row kept, billed to its payer, retired
with the disk after `parkHold`. A free lease (no payer, prepaid.go) ends with its
disk: the free lane keeps nothing past a lease's end. A coding run's sandbox is one (apps/coding sandboxrunner.go), so it can
be looked at after the run and resumed by a follow-up. Resume restores files, not
processes.

## A paid run lasts as long as it works and its wallet pays

Three clocks decide how long a command lives, and each used to end a long run
early on its own:

- **The exec deadline is the caller's.** `Cmd.TimeoutSec` is the deadline, longer
  or shorter than `SANDBOX_EXEC_TIMEOUT_SEC` (the default for a call that names
  none). `Run` clamps it to the sandbox's whole life (`clocks.absolute`, a day).
  It could only shorten: an hour asked for was cut to 900s and the run died with
  `context deadline exceeded`.
- **A command in flight is not idle.** `pulse` stamps the sandbox every minute
  until the command ends. Without it a build or a model that printed nothing for
  a quarter of an hour, with nobody watching, was reaped as `idle-disconnected`.
- **A lease is carried while its wallet covers the next hour and its org's budget
  has room for it** (`covers`, `budgets.allow`, reap.go). The sweep extends a
  working paid lease by an hour, up to the day; it does that only when the payer
  holds `hourly(class, disk)` and the org's ledger (`ledgerAllows`) admits that hour.
  The hour either one refuses, the lease keeps its own expiry and ends there (parked
  when it keeps a disk). A balance or a ledger that cannot be read declines this
  sweep and asks again next minute, so a finance plane back inside the ten-minute
  window costs the lease nothing; a silent ledger is asked once per payer a sweep.
  A free lease is not carried: it is held `freeTTL`, an hour.

A lease says when its sandbox will be ended whatever it is doing and however it is paid
(`Leased.EndsAt`, `clocks.ends`: `absolute` after the sandbox was CREATED, the sweep's `max-life`; none
for a desktop), so a run in a resumed sandbox ends a margin short of it with its work kept instead of
being cut by the sweep.

`apps/coding` bounds a run by `ceiling()`, the day a session may live (`agents.MaxLife`,
the figure the reaper holds a session to), and renews the run's model credential for as long
as it lasts: a delegation is an hour long and asked again before it ends (apps/coding
`key.go`), because IAM caps one at four hours. A task longer than a day is a chain of runs,
each ending with its work kept on its branch. A lease that is not carried (a free one, or a resumed free one with minutes left)
bounds the run by its own expiry, which the lease reports (`Leased.ExpiresAt`,
`Leased.Carried`), less the keep.

## A reaped run says why, and money is a reason (`lifecycle.go` ends, `store.go` Bury)

`clocks.ends` is `over` with the money asked. A paid lease that reaches its own
expiry was not carried, and the sweep carries every paid lease its wallet covers and
its budget has room for, so when its payer's balance was read and is short of
`hourly`, or its org's ledger answered that the budget is spent, it ends as
`cost-ceiling`, not `lease-expired`; the detail names the balance or the budget. A
balance or ledger that could not be read stays `lease-expired`. `SANDBOX_MAX_SPEND_CENTS` (default 0, off) also ends a running
paid lease once `spent` (its fee plus its hourly price for the time since
`CreatedAt`, which a resume renews) reaches the cap. Free and parked leases are not
held to it.

The row is deleted with the lease, so `end` buries a tombstone first
(`sandbox_ended`: id, org, actor, reason, detail, ended_at), in the same ship as the
deletion, kept `endedKeep` (a week) and pruned on each burial. A park the reaper
took is buried with the reason it was parked for, not `parked`. `find` answers a
missing id its caller held with that reason: still a 404, with the reason in the
message and as `reason` beside it, so a coding run whose sandbox was reaped under
its turn reports which clock or ceiling ended it. Anyone else gets the plain 404.

## A lease asks the org's budget, not only its wallet (`prepaid.go` budgeted, #806)

`afford` reads the wallet; `budgeted` then asks the org's ledger (`ledgerAllows`, which is
`cloud.Meter.Authorize`) for the same first hour (`quote`), so an org whose enforced spend cap is
spent is refused before the pod, on a lease and on a resume, with 402 `spend_cap_exceeded`. It
used to ask only for the one-shot fee, which is 0 unless priced, so the budget was never asked.
A free lease bills nobody and is not asked. The refusal is returned `cloud.Denied` so it crosses
the plane as a 402 (a bare sentinel crosses as a 500). The carry asks it too (`reap.go`
`budgets`), at each next hour, so a lease admitted under budget runs at most about an hour past
it rather than to its max-life (a desktop has none).

## The quota is staff's, counted per account (`quota.go`)

A paid lease is admitted by its balance (`afford`) and then by `within`: how many
paid sandboxes (running or pending, every class) its account holds against the
limit `sandbox.running`.

- **The account** is the org the lease is taken in, whoever pays. A SuperAdmin
  leasing in `acme` pays from their own wallet (`principal.ledgerOf`) and counts
  against acme. In the signup org each person is their own account.
- **The value** is resolved by commerce: the `org:<org>` row, else the
  `plan:<served rung>` row, else the registry default of 100 (`client/limit.go`).
  Staff set rows in admin.hanzo.ai (`PUT`/`DELETE /v1/admin/limits`).
- **The cache.** An answer is kept 15s per account (`quotas`). When commerce
  cannot answer, the last answer holds, else the default. The quota never refuses
  for want of an answer; the balance read beside it fails closed already.
- **No self-raise.** `GET /v1/sandbox/quota` reads the quota. The account's own
  `PUT` is gone; the per-org `quota` table it wrote is no longer read.

## A lost pod is replaced under its id (`api.go` fault, restart)

The kubelet ends a pod on its own — over its 2Gi `work` emptyDir
(`SANDBOX_WORKDIR_SIZE`), OOM, a delete — and the row still reads running. An
exec into it fails "container not found". `fault` asks the pod (`runtime.lost`:
NotFound, deleting, phase Failed/Succeeded) and, when it is gone, `restart` puts a
new pod under the same id and lease (no fee, no new clocks) and answers 503 naming
the cluster's reason and what survived (a disk does, a scratch working directory does not). The next
call runs. An exec that races the eviction fails while the pod still reads
Running, so `fault` asks again for up to `settle` (5s) first. A channel failure on
a live pod stays 502. `Lease` with an id checks
the pod the same way before handing a running row back. One restart wins:
`Store.Swap` moves the row only from the pod it names, so a stale read starts
nothing.

## A session's activation is timed apart from its model (`activation.go`, #809)

A turn that waited for a parked sandbox and then for a model read as one slow
turn, and the obvious reading — "the model is slow" — was usually wrong. Every
door to a running sandbox now records ONE `hanzo.session.activate` span
(`sdk/activation`) whose duration is the time to ready and whose
`hanzo.session.activation` says how:

- **warm** — `Lease`/`resume` named a sandbox whose pod is alive; nothing started.
- **relocated** — its pod was gone under a running lease and `restart` put a new
  one under the id (from `Lease`, or from `fault` when an exec found it gone).
- **cold** — a pod was started for a session that had none: `unpark` after a park
  (`hanzo.session.resumed=true`) or a new lease (`false`).

Only the call that did the work records it: a refusal (any 4xx — no funds, no
quota, a conflict) is no activation, and a resume or restart that lost its
compare-and-set (`Shift`, `Swap`, `errMoved`) started nothing. A start that failed
is recorded with an error status and kept out of the percentiles. The span carries
no `gen_ai.*` key, and the model's own time to first token is `hanzo.ai.ttft_ms`
on its gen_ai span (`sdk/infer` `AttrTTFT`), measured from the model call — so
neither number can contain the other. `GET /v1/admin/o11y` reads p50/p99 per kind
and the model's p50/p99 TTFT side by side (`sessions`). Spans, not a histogram:
spans leave every plugin process over ZAP, a metric only leaves the process that
pushes its registry (apps/o11y metricspush.go).

Timing changes nothing a retry does. `activation_test.go` pins it: a retried
resume is warm and creates no second pod, and a command whose pod was lost is
answered 503 unrun, is not replayed by the relocation, and runs once when its
caller asks again.

## The sandbox sweep is bounded by construction (`apps/sandbox/bound.go`)

An agent's sandbox cleanup deleted every sandbox on a node, **including
kube-system DaemonSet pods**. They self-healed. That is luck, and the reason it
was possible is that "which pods are sandboxes" was a selector written at a call
site — so the answer was only ever as good as whoever was typing, and a cleanup
gets written exactly when that person is in a hurry.

So the answer stopped being a flag. `Bound` is a TYPE carrying both halves
together, and there is no constructor that yields one without the other:

- **namespace** — `bindTo` REFUSES `kube-system`, `kube-public`,
  `kube-node-lease`, `default`, `hanzo`, empty, and anything prefixed `kube-`.
  The refusal happens in `newRuntime` **before a Kubernetes client is built**, so
  a deployment that sets `SANDBOX_NAMESPACE=kube-system` gets a subsystem that
  cannot start a pod, let alone delete one — `ready()` fails closed with the
  reason, rather than logging a warning nobody reads.
- **label** — the selector is always `hanzo.ai/sandbox`, which only this
  package's pods carry.

Either half alone is insufficient and that is the whole design: a label with no
namespace reaches every namespace in the cluster (this is how a sweep meets
kube-system), and a namespace with no label reaches whatever else was scheduled
there.

**A delete is not a selector at all.** `stop` now READS the pod back, checks the
object carries the label (`Bound.covers`), and deletes BY NAME with a UID
`Preconditions` — so the label check is not a TOCTOU and a recycled name cannot
be deleted in place of the object that was inspected. `IsConflict` is treated as
"already gone", never retried without the precondition.

- `known()` returns **nil** when any store could not be read, and nil aborts the
  sweep. An unreadable store read as "no sandboxes are claimed" would delete
  every live sandbox in the fleet.
- It reads `Store.IDs` and **not** `Store.List`, because `List` is `LIMIT 200` —
  right for a page a human reads, catastrophic for a set a sweep differences
  against, where truncation means "delete everything past row 200".

The grace is `idleAfter` (1h), not one reap interval, because the two mistakes
do not cost the same: a leaked pod costs an hour of a node, a pod deleted out
from under its owner costs their work.

**ONE DEPLOYMENT PER SANDBOX NAMESPACE.** The sweep's whole claim is "no row of
ours names this pod", so a second cloud pointed at the same `SANDBOX_NAMESPACE`
would read the first one's live sandboxes as orphans. Same invariant the stores
already have; stated where breaking it deletes something.
