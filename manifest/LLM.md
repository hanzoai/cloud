# LLM.md — manifest

Guidance for manifest. Moved from the root LLM.md; cross-cutting rules stay in the root.

### Where a subsystem's binary comes from — ONE ladder, and the last rung is the network

    CLOUD_<NAME>_ADDR   already listening there — start nothing, just mount it
    CLOUD_<NAME>_BIN    this exact path, honoured as given (the operator named it)
    <dir>/<name>        a dedicated binary beside the running host
    <dir>/cloud         the multi-call binary beside it, with --enable=<name>
    plugins.lock        the pinned release (internal/pin, manifest/release.go) —
                        a host whose image carries NO plugin binaries at all

`<NAME>` upper-cases the app name and folds `-` to `_`. On-disk wins over the
index because it is what this host was BUILT with; a dedicated binary wins over
the multi-call one because its presence is someone's explicit intent. Both link
modes stay: a developer builds the single lean plugin they are editing and the
host prefers it (1.3s), a release ships the unified binary and the host falls
through to it.

**Eager vs lazy is a property of the WORK, not of the app.** `manifest.App.Eager`
marks the subsystems that must start WITH the host, such as `pubsub` (NATS :4222)
and `o11y` (its OTLP collector must accept spans before anything has one to send).
A wire protocol is not a reason: `kafka` (:9092) and `amqp` (:5672) are lazy
behind `App.Wire`. The host binds the port, the first connection starts the app on
the listener `App.Listen` opens, and a start that fails is tried again by the next
connection instead of leaving the protocol absent until the pod restarts.
Everything else is lazy, which is what makes a 100+-service binary cheap: an app
nobody calls costs a route entry and a struct, not a process.

### The pinned release: `plugins.lock` -> verified fetch

**The image carries a copy; the lock is how a host without one gets the same
bytes.** The Dockerfile builds the light host plus one binary per manifest app
into `/plugins` beside it, and the host resolves each plugin as a file on disk
(`manifest.App.Plugin`). That stays the default, because a pod that pulled one
image must be able to serve without reaching the network.

**`internal/pin/plugins.lock` is the ONE source of what a plugin repo has
released.** One stanza per plugin (name, `owner/repo`, `vX.Y.Z`), one line per
platform (`os/arch`, https URL, `sha256:<hex>`). It is compiled into the host, so
a bump is a commit and review and rollback are a diff. `pin.Parse` is strict: a
platform without a digest, a floating tag, a second stanza for a name, or a plain
http URL is a parse error, and the gate's `TestCurrent_TheCommittedLockParses`
fails the build on it. The old single index (`CLOUD_PLUGINS`, `binaries.json`
read at runtime) is gone: N plugin repos would have been N indexes, and a bump
nobody reviews is a binary the host runs.

Each plugin repo releases itself: on a `v*` tag, `hanzoai/ci`'s `plugin.yml`
builds `<name>-linux-{amd64,arm64}` with `CGO_ENABLED=0` through
`POST /v1/build` (the artifact lane: the publisher, not the caller, holds the
object-store credential), reads back the `binaries.json` it wrote, and commits the
stanza to this file. Only the release identity may publish a protected name
(apps/platform `protected.go`); an ordinary org admin is refused.

`CGO_ENABLED=0` is the ONE build mode for a published plugin: it runs on a box we
did not build, and a cgo binary would demand a matching libsqlite3 on it. The
image's `/plugins` are cgo + libsqlcipher because there the host owns the
filesystem. Codec-only tests stay in the image's codec step.

**Open: no host can read a private store yet.** The artifact lane publishes into
the `plugins` bucket, which is private. zip's download (zap-proto/zip load.go,
`http.Get`) sends no credential, so a host can fetch only an object readable
without one. Closing it needs one read credential in zip's fetch, or signed reads.

- **No digest, no trust.** `pin.Parse` refuses a platform line missing its
  digest, and `remote` returns a `zip.Plugin` with both `URL` and `Sum` set. zip
  verifies before `chmod`, and caches BY DIGEST: restart and rollback touch no
  network.
- **Publishing cannot push.** The lock is compiled in, and a running host keeps
  what it has. New bits reach a running process exactly two ways: restart it onto
  an image with the new lock, or `zip.App.ReloadTo(name, Plugin{URL, Sum})` via
  `POST /v1/admin/plugins/:name/reload`.
- **Reload is SuperAdmin-gated, and audited BEFORE it acts.** `apps/plugin`
  mounts four routes (apps/plugin/plugin.go:78-81): list, reload, enable, disable. Every
  mutation is SuperAdmin-gated and written to the hash-chained audit trail BEFORE
  it is reported as done; a deployment with NO durable audit store REFUSES the
  operation rather than performing an unrecorded one (apps/plugin/fleet.go:154-155).
  Reload starts the replacement and proves it LISTENING before any traffic moves,
  so a bad build leaves the old one serving and returns an error rather than a
  hole; naming a digest this host has run before IS the rollback, and costs no
  network because the digest is the cache key. Fleet scope applies one host at a
  time and STOPS at the first failure, so a build that cannot come up reaches
  exactly one host.
- **`disable` answers 503, never 404, and routes never unregister.** Removing
  routes would mutate the route table and re-adding them on enable would grow it
  without bound across cycles. It is also the truer answer: 404 says "no such
  API" and a client may cache it and stop retrying; 503 says "this API exists and
  is down right now", which is retryable.

## A capability answers at its own name (HIP-0139 §3), and ten came home

Ten apps served under a noun that was not their own. Each fold moves the ADDRESS
and nothing else — the name, the package, the plugin and the store key all stand.

    plan          /v1/plans        → /v1/plan
    provisioning  /v1/instances/*  → /v1/provisioning/*
    link          /v1/links        → /v1/link
    knowledge     /v1/kb/*         → /v1/knowledge/*
    explorer      /v1/{indexers,oracles}    → /v1/explorer/{indexers,oracles}
    web3          /v1/{chains,rpc,tokens}   → /v1/web3/{chains,rpc,tokens}
    dataset       /v1/risk/datasets  → /v1/dataset
    label         /v1/risk/labels    → /v1/label
    reference     /v1/risk/reference → /v1/reference
    product       DISSOLVED

`product` opened no store, so it was not a capability but a proxy sitting on two
others' roots. §7.2 permits a split only along a store boundary and there was
none, so its four reads went to the apps that own the roots: the Meilisearch pair
to `search` at the SAME addresses (`apps/search/inventory.go`), and the Qdrant
pair to `provisioning`, which allocates into that backend, at the operator's
depth `/v1/admin/provisioning/vector/*` (`apps/provisioning/inventory.go`). The
operator depth is also the only collision-free home: `/v1/provisioning/vector/
collections` would shadow the tenant read of an instance named "collections".

Four things this move taught, all of them the kind that only shows up when you
pull the address out of the app:

- **A store key is not an address.** `cek.Open(namespace.System(), "provisioning")`
  and `locus.System("link")` name FILES; renaming one with the address opens an
  empty store. Same for the warehouse tables (`hanzo.risk_dataset`,
  `hanzo.risk_label`) and knowledge's framework `Module = "kb"` and `kb-` DocType
  slugs. None of them moved.
- **A prefix built from two constants does not answer a grep.** `apps/reference`
  composed its address as `parentPrefix + leaf`, so the literal `/v1/risk/reference`
  appeared nowhere in the file. Its routes stayed put while its manifest row
  moved, and `describe` projected ZERO paths. The floor ratchet caught it —
  `reference 6 → 0` — which is the whole reason the floor is per product.
- **A routing claim with nothing behind it is still load-bearing.** provisioning's
  row claimed `/v1/s3`, `/v1/search/query` and `/v1/vector` with no route
  registered behind any of them. Deleting them was supposed to be bookkeeping;
  it narrowed `meteredTrees`, because `spend.go` resolves the metered trees
  through the manifest. `POST /v1/s3/bucket` had been billable only through
  provisioning's dead claim over storage's root.

`openapi/misfiled.txt`: 100 pairs → 86. `/v1/plans commerce` stays — commerce's
`/v1/plans/{entries,seed}` belong to the money wave, not this one.

## A row is a process, so two things that could not be processes stopped being rows

`product` opened no store. `rollingcap` and `catalogsync` opened no store AND
answered no path: each claimed a `/v1/<name>` with nothing ever registered behind
it, so the fleet published two capability names no customer could call. GA
capabilities: **123 → 121**, and the published document does not move by one byte,
which is the cleanest evidence they were never capabilities — the weave passes
unregenerated, `openapi/floor.json` names neither, and `openapi.yaml`,
`public.yaml` and `private.yaml` are byte-identical across the change.

They held rows for a reason that reads as reasonable and is not: **a row is how
the host starts a process**, and both wanted to be started. But in this fleet a
row means the host routes a prefix to a CHILD PROCESS (`cmd/cloud` `mount` →
`zip.Load`), and a process is precisely where neither could do its work:

- **rollingcap** installed the AI-spend ceiling by writing `cloud.SetRollingCapReader`
  — a package global in the root `cloud` package — and the only reader is the `ai`
  module's gate, in **ai's** child. A global written in one child is invisible in
  every other, so the ceiling has never applied to a completion. It never even ran:
  the row was lazy on a prefix nothing requests, so its Mount was never called
  either. Two independent reasons for the same nothing.
- **catalogsync** consumed `commerce.product.created` off the bus and called
  `content.EnsureCatalogAsset`, which refuses when content's own singleton is nil —
  and it is nil in every process but content's. `paid_egress_test.go` had recorded
  exactly that ("content.mounted is nil in this process, so it dead-ends before the
  render") in a ledger of reaches that are HARMLESS, where a reach that is the app's
  whole purpose reads as fine. The producer was unwired too: commerce publishes that
  event only when `PUBSUB_URL` is set, which nothing in this repo or its deploy sets.
  A loop open at both ends.

So the fix is not a sixth fact on the row saying "start this, it is not a
capability". That concept would have had **zero** live instances the moment these
two were dealt with honestly, and it would bless the very arrangement that cannot
work. Instead:

The lesson generalises past these two: **`Eager` is not a way to start something
the router cannot reach.** It means a listener binds at boot, and the four rows
that set it all own one. Something that must run inside another app's process is
not an app; it is code in that app.

## Eager apps must never be evicted on idle, and KMS must start eagerly

The S3 store and Traefik ingress dial S3_ADMIN_ENDPOINT (:8333) directly.
Because zero traffic hits /v1/s3/* through the host HTTP router, zip's
Reap(time.Minute) idle check was evicting s3 after 15 minutes of idle:
	zip warning: name not matched: plugin
	zip warning: name not matched: evicted

zip error: Nothing to do! (idle.zip).

Two distinct causes produced this:

1. s3 was declared Eager: true, but failed eager start during host boot
   because plugin/s3/iam.go (sessionKey()) invokes kms.GetSecret() before
   the router's wake socket existed and while kms was still cold/lazy.
   On start failure, mount() in cmd/cloud/main.go demoted s3 to p.Lazy = true
   with IdleAfter = 15m. Later, ExecStartPost (curl /v1/s3/health) started
   s3 as a lazy plugin, which zip then evicted after 15 minutes of inactivity.

2. manifest/plugin.go was setting IdleAfter: idleAfter (15m) unconditionally
   even for a.Eager apps.

Fix:

- Declare kms as Eager: true in manifest/apps.go so it starts before s3 and
  listens on its socket for secrets resolution at boot.
- For all eager apps (a.Eager), configure IdleAfter = 0 in manifest/plugin.go
  and ensure p.IdleAfter = 0 in cmd/cloud/main.go (both primary mount and lazy fallback).

## A rollout: the host binds first, and every process drains what is in flight

`cmd/cloud` registers every app lazily (`mount` starts nothing), binds :8000,
and only then `boot` starts the pinned children: the Vital ones at once, then the
rest at once. `/readyz` answers 503 `starting` (body names them) until every
Vital child runs, 503 `unfit` if one failed, else 200; a pinned child boot has not
reached yet starts on the first request that needs it. A start failure is read
from zip's `Status.Error` (`census`); there is no boot-time absence map. A
Required app or `project` failing to start stops the host.

Shutdown, in both the host and each child: listeners close at once, the drain
ends when the last running handler returns (`sdk/inflight`: a stream's
handler returns when it starts writing, so open streams are cut, not waited on),
bounded by 5s in the host and 15s from SIGTERM in a child. Then zip (≥ v1.37.36)
SIGTERMs every child at once and waits for all of them, killing one only after its
20s `Plugin.Grace`; that wait ignores the shutdown context. So a child closes its
stores with a checkpoint and ships them (`OrgStore.CloseAll`) on every rollout.
universe gives the pod 45s and no preStop sleep (Recreate has no other pod to
drain to).

## The plugin ceiling and a busy child (zip v1.37.33)

The host never stops a child that is serving a host-routed request: a reply
until it is written, a stream until its body closes, an upgrade until the tunnel
ends. Room goes to the child whose last request ENDED longest ago; with every
candidate busy a start waits up to its `Start` bound (`startTimeout`, 90s) and
answers 503. An upgrade over the host's ZAP listener is refused 501 (ZAP has no
connection to hand over). A call that dials a child's socket directly (the
plane, the fleet MCP door after `Start`) is not counted and can still meet an
eviction.
