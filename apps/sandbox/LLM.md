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

**An identity outage is not a sandbox outage.** The exchange reaches IAM, and a
lease whose exchange fails still happens — the pod holds nothing, which is the
sandbox everybody had before this existed. Refusing the lease would turn one
unreachable dependency into a total sandbox outage while denying nothing that is
not already denied, so the failure is LOUD in the log and the lease continues.

**Two doors, one of them silent.** The HTTP door relays `cloud.CallerBearer`. The
agent plane passes "" — a plane call carries an ATTESTED caller, not the caller's
own token, so there is nothing to exchange and substituting a credential of ours
would put an identity in the pod nobody presented. An opaque API key is likewise
not a relayable bearer, so those leases start with no session rather than with a
key in a shell.

**The far end.** `git.hanzo.ai` accepts the same token as a git credential
(hanzoai/git `services/auth/iam.go`), verified against this deployment's own OIDC
login source and resolved to an account only through the link that sign-in
already wrote — so a subject that never signed in to the forge resolves to
nobody, and nothing is created from a credential. Its scope there is
`write:repository`: a credential that exists so a checkout works administers
nothing.

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


There is now exactly ONE door, in `plugin/agents/coding.go`:

```go
zip.Post[plane.CodingStartIn, plane.CodingStarted](cloud.ZipApp(app), "/v1/coding", httpCodingStart)
```

A typed op is four surfaces at once, so that single registration IS the REST
route, the OpenAPI operation, the CLI command and the MCP tool. The plane door
(`coding_start`) is DELETED: its only caller was Slack's `code:` prefix, and a
second way in for one caller is a caller whose model never has to choose.

## The magic word is gone, and here is the evidence it was never the only path

MEASURED on the deployed door before anything was deleted — twice more of this
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
check; a name nobody listed is refused by the door two layers earlier. So the op
was genuinely offered and genuinely dispatchable, and `codingIntent` was pure
subtraction. It is deleted, along with `slack_coding.go` entirely — the parse,
the usage sentence, the target lookup and the dispatch were reachable only from
the prefix. `SLACK_AGENT_REF` did not become an orphan knob:
`channelAgentRef("slack")` reads the same variable for the chat turn.

`post_v1_coding` is published as **`create_coding`**, because the door renames a
derived operation id to the verb phrase it already contains (`fleet/verbs.go`).
That is the string a `tools/call` carries; do not look for `post_v1_coding` on
the wire.

## What actually blocked the model, and it was not the wiring

The description. `zipdoc` strips an exact leading match of the handler's own
name, so `// httpCodingStart is the app's door.` reached every SDK, the document
and the MCP tool list as:

> Is the app's door. It answers 202 with the run's handle the moment the run is
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
  Attribution-by-argument was sound while every door was an adapter we wrote and
  stopped being sound the moment the op became a tool.

Both halves of the identity are now parameters of `coding.Start(ctx, org,
subject, in, log)`, read off `cloud.Who(ctx)` by the door. The check lives in
the HANDLER and not in middleware, and that is not a style choice: zip dispatches
an MCP `tools/call` and a call-plane op STRAIGHT into the handler
(`registeredOp.direct`), so a middleware guard covers one door in three — the
same hole `apps/exec` was walked through.

`TestNothingInTheInputCanNameWhoTheRunIsFor` walks the type by reflection rather
than listing the fields it does not want, so a field added tomorrow is judged by
the same rule.
