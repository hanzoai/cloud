# apps/git — typed-op status

## The coding orchestrator no longer comes here

A run's push credential, its clone URL, its ref check and its pull request are
now the FORGE's (git.hanzo.ai, `forge/`), not this app's. What that removed:

- `grant.go` and the `hgg_` credential class. It was the one bearer in the whole
  binary that resolved to no principal, honoured in exactly one place
  (`resolvePackRepo`). Nothing in cloud is now reached by anything but a
  validated principal or a public read, and `packCaller` has no `ref` field
  because no caller is confined to one any more.
- `export.go` (`CloneURL`, `VerifyRef`) and `propose.go`, with
  `plugin/git/clients.go` that published them. A run's ref check read a bare repo
  off local disk; it now reads the forge, which is where the run actually pushed.
- Five plane ops: `git_grant`, `git_revoke`, `git_clone_url`, `git_verify_ref`,
  `git_propose`, and their wire shapes.

Propose had two backends chosen by a mirror row — a GitHub pull request, or a
link to a branch page nobody can approve. The forge has native pull requests, so
that question has one answer now.

**Two controls bound that credential, and both are enforced.** The ORG picks the
namespace through a CLOSED table (`forge.Owner`) — an unmapped IAM org is
refused, never turned into a forge namespace by being spelled one. The ACTOR
picks the repository, as a SUDOED read, so the forge's own ACL applied to the
human is the answer; a run with no forge login is refused rather than falling
back to the machine, which is a site administrator.

Both endpoints take the actor — the sandbox one to be handed a key, the ROUTED one
because the machine it dispatches to holds credentials broader than the caller's,
so an address resolved as a site admin is a repository the caller could not open
themselves.

**Confinement is the forge's, not the orchestrator's.** A run pushes SSH
straight to Forgejo, which never sees our refspec, so the branch rules live in
`forge/protect.go`: repositories this code creates are born with their default
branch refusing every direct and force push, and an EXISTING repository that
does not already refuse a deploy key is refused a grant rather than silently
re-policied. The predicate is the fork's own
(`routers/private/hook_pre_receive.go:270-285`), and it covers the default
branch plus the conventional release lines.

Two residuals, both named in `forge/protect.go`. TAGS are uncovered and are the
bigger of the two: this fork publishes no tag-protection API, and Actions reads
workflow files from the pushed commit, so `on:push:tags` fires — a movable tag
is a supply-chain primitive, not a mislabelled commit. Closing it needs a change
in the fork's pre-receive tag path. Branches outside the protected patterns are
the smaller one; what bounds both is that a run reaches only repositories its
actor could already write.

**A run's credential is a write DEPLOY KEY** minted per run on the one repository
and deleted at run end (`forge/grant.go`). It is a deploy key and not a token
because Forgejo's token scopes are categories rather than repositories
(`models/auth/access_token_scope.go`), and because minting a token at all needs
the target's PASSWORD (`POST /users/{u}/tokens` is behind
`reqBasicOrRevProxyAuth`) — the machine credential cannot mint one. Deploy keys
are SSH-only: the HTTP path resolves permission from an authenticated user and
has no deploy-key branch, so a run's remote is the forge's `ssh_url`.

**Still here:** the outbound mirror, the code index, Slack notify, the deploy
tree read, the LSP tree/rev reads, project visibility and the git figures, over
local bare repositories and linked repositories' caches alike. The copy-style
GitHub importer (`github_import.go`, `import_plane.go`, the `/git/import`,
`/git/inbound` and `/git/status` plane ops and the `inbound_conflicts` table) is
gone: nothing called it (`apps/sync` answers `cloud.GitImporter` against the
forge), and a repository on GitHub is linked, not imported (below).


COMPLETE at **30 typed / 18 refused**, and that partition is now a **GATE, not
prose**: `untypedByDesign` (typed_wire_test.go) is the closed list of the 18
refusals with the wire fact behind each, and `TestEveryRouteIsTypedOrNamed`
fails three ways — a served operation that is neither typed nor named, a name
that describes an operation git no longer serves, and a name that IS a typed op.
So the next git route is typed BY DEFAULT and a stale reason cannot outlive the
route it described. `TestEveryTypedOpIsDescribed` pins the other half: every
typed op carries lifted prose, because an op added without regenerating
zipdoc_gen.go is a nameless MCP tool. Prose was the previous form of this
partition, and prose cannot fail — that is the whole reason it moved.

Every route with a JSON request/response shape is a typed op (ops.go states the
client; routes() in git.go registers them on the /v1/git group behind
`cloud.Bridge` and `bridgePrincipal`). The four refusal families below were
re-derived against the handlers and against **zip v1.36.3**, the go.mod pin, not
inherited from this file — and TWO of the four had a reason that had EXPIRED, one
of them twice over. A version in prose is a claim about a dependency and it
expires; re-read the pin, not this paragraph. Do not re-type these; run the gate
before believing any route counter that says git has untyped work left.

**A refusal is still DESCRIBED.** Seven of the 18 read a request body, and each
declares it through `spec.Register` without becoming a typed op: the three ZAP
procedures that bind one (zap.go's init), and the four pack POSTs as
`spec.Binary` (smart_http.go's init). They previously published an operationId,
tags and nothing else — which no SDK generator can tell apart from a route that
takes no body. `declaredBodies` + `TestRefusedRoutesDeclareTheBodyTheyRead` pin it
both ways: a declared body that vanishes fails, and a body declared for one of the
eleven that read none fails too. What a refusal still cannot have, and only a
typed op gives, is prose, an MCP tool and a CLI command.

**Each family's declarations live WITH the family**, read off the same table its
prose is. They used to sit together in webhook.go's init, one hand-written list
beside a loop, and that is exactly how four of them came to name the root-host
addresses `routes()` had already deleted — six `Describe` calls and four
`Register` calls for patterns nothing registers, rendering NOTHING. Prose written,
reviewed and silently dropped is the class `spec.Complete` exists to catch, and
it cannot: it judges an orphan only inside the products the app publishes
(sdk/spec/prose.go), and `manifest.OwnerOf("/:org/:repo/info/refs")` names none.
Deleting all ten moved the published subset by zero bytes, which is the proof they
had never rendered.

## The estate's year of work: GET /v1/git/activity (`activity.go`)

One public typed op: the estate's last 365 days. The headline is `commits` and
`merges` across every repository the estate holds, the upstream projects it carries
and keeps current included, each commit once by SHA; `months` series say how the
estate, each group (hanzo, lux, zoo), each org and each public repository grew.
`ours` (the part the estate's people wrote) is a breakdown beside it. Also: active
repositories, contributors, a per-day series, the newest public commits, and one
page of public repositories (`?org=&group=&page=&limit=`).

It computes nothing. The repositories are on the forge's disk, so the index runs
beside them — universe `charts/app/files/hanzo/estate/activity.py`, resident, told by
the forge's own push and repository hooks which repository moved — and rewrites one
document in the object store, bucket `git`, key `activity/activity.json`, seconds
after each sync. This op reads that document at most once a minute per process,
answers a stale copy while the next is fetched, and keeps the copy it had when a
fetch fails; only a process's first read waits on the store (503 until the estate
has been indexed). A warm read is memory: microseconds, whatever the estate's size.

Private repositories are counted in every figure and named nowhere: the document
the index writes carries no private repository, message or author, so this op
cannot leak one. That is the index's property, and it is why the op needs no
principal. The counting rules (committer date, branches and tags) are the index's,
and the document states its window and its months.

## The estate's open source figures: /v1/admin/git/metrics (`metrics.go`)

Two SuperAdmin ops in the operator's family (manifest: git answers `/v1/admin/git`
too; HIP-0139 keeps every route under /v1/<app> or /v1/admin/<app>), for #456.
`POST` takes the collector's daily batch, which holds figures (one
per date, ecosystem, entity and metric) and package registrations (which GitHub
repository a package is published from). The batch is checked whole and written in
one transaction, and a re-post of a key replaces its value, so the collector re-sends
GitHub's 14 days of traffic every run without double-counting. `GET` answers series
by entity, org or ecosystem, per day or per month. A gauge (stars, all-time totals)
takes an entity's last day in a month, a count (a day's downloads, clones) sums its
days, and a group sums its entities. A window or match too wide is refused, never cut.

The store is the PLATFORM's, not an org's: the system namespace's `forge_metrics`
(`metrics_store.go`), on hanzo-sql where the DSN is set (`manifest.SQLStores`),
opened on first use like the SSH key registry (`metricsHolder`). A store that does
not open is logged and costs only these ops (503) and the repo rows' packages; the
next use tries again. It never costs the mount.
Their /v1/admin address keeps them in private.yaml and out of every public
client, tool list and skill.

**The collector is the platform's own daily run (`collect.go`), and it needs no
identity.** An API key is never SuperAdmin, and neither is a client_credentials token
of an admin-org application (`authz.Claims.Machine`; `sdk` `sudo`), so nothing
outside cloud could post as one. The collector is therefore a plane op, `git_metrics_collect`
(`POST /git/metrics/collect` on the plane, not an HTTP route), which the cron app asks by
name as the platform (`cron` `planeOps`; universe schedule `oss-metrics`). It refuses any
caller whose stated org is not `authz.AdminOrg`, and writes through `metricsStore.put`,
never through the SuperAdmin ops above. No token, no KMS path, no service account.

What it reads, each run:

- Repositories: the public repositories of the estate's index (`activity.go`) that name
  a GitHub address, which is every GitHub org the forge mirrors. Nothing lists orgs in
  Go. Provider answers GitHub for it (`integrations_github_figures`, 100 repositories a
  question, one GraphQL query): stars, forks, open issues, watchers, and the root
  `package.json`, `pyproject.toml`, `setup.cfg` and `Cargo.toml`. The App's key stays in
  provider and the answer is facts about public repositories; a repository GitHub does
  not publish is missing, never an error.
- Packages: the one a root manifest names, registered against its repository
  (`collect_rules.go`): npm unless private, PyPI, crates unless `publish = false`. A
  workspace's members and a package published from a subdirectory are not read (register
  them by `POST /v1/admin/git/metrics`).
- Downloads: npm and PyPI per day for the 14 days ending yesterday, crates.io all-time
  (`downloads_total`), and every public Docker Hub repository of a namespace named like an
  estate org (`pulls_total`), registered against the estate repository of the same name
  when there is one. Public registry APIs, a generic user agent, 429 and 5xx retried with
  a doubling wait, crates.io and PyPI one read a second.

Idempotent: a figure is one per entity, metric and UTC day and a run replaces what it
reads. A source that fails or runs out of its 13 minutes is named in the answer
(`failed`) and costs only itself; a run that wrote nothing fails, so the schedule's
`cron-status` shows it. **Not collected: release downloads and traffic (clones, views),
and history before the first run (npm and PyPI daily figures start 14 days back).**

Repo rows carry `upstream` (`github.com/<owner>/<repo>`: a GitHub link, or the
forge mirror's `original_url`) and `packages`, joined from the registry by that
upstream in one read (`withPackages`). Keying the registry by upstream is what
lets a tenant's repo row and the estate's GitHub org meet without mapping an IAM org
onto a forge org. The rules (`metrics_rules.go`) and the SQL (`metrics_store.go`)
import nothing of cloud's but sdk/dialect, so they test without the rest of the app.

## The four refusal families

1. **POST /v1/git/webhook** (git.go:336, handler webhook.go:107) — 1 route, and
   its recorded reason was WRONG TWICE. It is no longer an HMAC-verifying
   handler — webhook.go is a 113-line tombstone that reads no body, holds no
   secret and answers 410 to everything — so the "the signature covers the raw
   bytes" reason described code that is gone. The reason that replaced it, "a
   typed op needs an In or an Out; a tombstone has neither", was false too:
   `noInput` and `noContent` are this package's own and six typed ops use them.

   What holds is ORDER. **ANY bytes answer 410**, which is what retired MEANS and
   what `TestWebhookIsGoneForEveryDelivery` drives with a real push, an empty body
   and `{not json`. `op.invoke` json-decodes a non-empty body BEFORE the handler
   is entered (zip typed.go:485-490) and answers an undecodable one 400, so a
   typed op would tell a malformed delivery its body is bad instead of where the
   delivery belongs. Measured on a typed prototype of this exact route: an empty
   body and a real JSON delivery answered 410 byte-for-byte, a JSON array and a
   non-JSON body answered 400. It is not one flag away either — `encoding/json`
   validates the whole document before invoking any custom unmarshaler, so an In
   that tolerates every SHAPE still cannot tolerate bytes that are not JSON. The
   refusal is RUN rather than asserted:
   `TestATypedTombstoneWouldRefuseABodyItAnswersToday`.

2. **Smart-HTTP git protocol** (routes() in git.go, under /v1/git) — 6 routes:
   the org/repo trio and the project-scoped org/project/repo trio. The git pack
   wire: request bodies are `application/x-git-*-request` pack streams,
   responses are `application/x-git-*-advertisement|result`, answered with
   `c.Bytes` (smart_http.go:176) and the packfile STREAMED with `c.SendStream`
   (smart_http.go:206) — not JSON in either direction, and both of those live on
   `*zip.Ctx`, which a typed handler never receives.

3. **Browser UI** (uiRoutes, ui.go:143-148) — 6 routes, under /v1/git beside the
   JSON ops. Server-rendered text/html (html/template): ui_templates.go:96 sets
   `Content-Type: text/html; charset=utf-8` and :98 answers `c.Bytes`, where a
   typed dispatch ends in `c.JSON(out)`.

   **Two of the six carry a SECOND, independent mechanism that nothing recorded
   until it was measured.** The tree and blob pages address a path INSIDE a
   repository, so their routes end in fiber's greedy `*` (ui.go:146-147) and the
   handlers read it back with `c.Fiber().Params("*")` (ui.go:299,321). zip's
   `Template` (zip address.go:61) rewrites only `:name`, so a typed registry
   would publish the path VERBATIM as `…/tree/*` while cloud's router reading
   names the segment `{wildcard1}` (openapi/openapi.go:808-823); `Fold` then finds
   no live route at the registry's key and refuses (openapi/openapi.go:705) — not
   one mis-named parameter but the WHOLE document, i.e. `make -C apps/git
   describe` failing and git publishing nothing at all. Run it:
   `TestThePageWildcardCannotBeATypedOp`.

   These two are also the only addresses git publishes that reach NO generated
   client: `openapi/public.go` drops any path containing `{wildcard`, so
   `openapi.yaml` carries 37 of git's 39 paths.

4. **ZAP procedure adapters** (zap.go:144-148) — 5 routes, and this reason had
   EXPIRED twice over. It said a typed op's returned error renders
   `{status:<int>, code, error:<msg>}` and that nothing lets one answer a 4xx with
   a body of its own. Neither holds at the pin: a refusal renders RFC 9457
   problem-details — `{type, title, status, detail, code}`, with no `error` key
   written at all (zip problem.go:39-77) — and `WithStatus` is variadic over any
   status with `StatusCoder` picking one (zip typed.go:154, 189), so the success
   envelope and a 400/404/409/500 carrying `{status:"error", msg}` are both an
   ordinary typed Out today.

   What still holds is ORDER, and it is a fact no op can reach from inside itself.
   `op.invoke` decodes the body BEFORE the handler is entered (zip
   typed.go:485-490) and answers an undecodable one with that problem document —
   whose `status` member is a NUMBER. The bridge unmarshals every non-2xx body
   into its own envelope, whose `status` is a STRING
   (zapface/dispatch.go:35-40), so the unmarshal FAILS and the ZAP client is told
   `INVALID_RESPONSE — non-envelope response (HTTP 400)` (dispatch.go:82-86)
   instead of the sentence the handler wrote; today that same body answers
   `{status:"error", msg:"invalid body"}` and the bridge forwards the msg
   (dispatch.go:88-91). The 403 leg is NOT what holds them, and the old reason
   implied it did: dispatch short-circuits 401/403 before it parses anything
   (dispatch.go:76-80).

   Expected to shrink: the shared /zap plane already replays the typed /v1 ops
   frame-for-frame (see zap.go's header comment); retiring these is a client
   migration, not a typing task.

## The ref policy stands in FIVE endpoints, not one

`refpolicy.go` used to say receive-pack was "the point every path that changes a
ref passes through and no client can decline to". That was false, and it was the
whole argument. It guarded **one** of eight ref writers into the same bare repo;
a run refused at the entry point walked in through any of the other seven with
the same credential — most cheaply through `POST /repos/:name/push`, which lands
a **fast-forward child** on the branch a reviewer just approved and so trips no
"the branch was rewritten" signal anywhere.

The writers, and how each states its intent, are enumerated in `refpolicy.go`'s
own doc comment (writers 1–5; there were nine until the importer's three went,
and SSH receive-pack went with the SSH listener)
so the list lives beside the rule. Two of them
cannot be judged command-by-command and are refused **structurally** instead:
the cache fill writes only a LINKED repository, which every local writer refuses
or sends to its host, so its forced `refs/heads/*` and `refs/tags/*` have nothing
local to overwrite, and a link over a repository holding history here is 409
(`linkTo`); and `HEAD` is not under `refs/`, so the fill and the client-less
push run it past `checkHeadRef`.

Writer 5 is the newest: **merging a pull request** (`merge.go fastForward`)
advances base, which is a ref write, so it states its one command as a
`refCommand` and calls the same `checkRefPolicy`. It arrived guarded rather than
being retrofitted, which is this list doing its job. It is also the only writer
that COMPARE-AND-SETS: it read base in order to judge the merge, so it hands
that value back to go-git and the write fails rather than silently discarding a
push that landed in between.

**A sixth endpoint is a change to that list, not just a new function.** The proof
that each is closed is `refwriters_wire_test.go` — real git CLI, real server,
one test per endpoint.

## The pull request is a native noun, not a GitHub round trip

`pulls.go` (the noun + its four ops) and `merge.go` (the one thing it does to
the repository). Before it, an agent could push a branch and had nowhere to
propose it: `propose.go` answers "where do I read this" by opening a REAL pull
request on GitHub when the repo mirrors there, and by returning a branch URL when
it does not — so a repository living only in the forge had no endpoint at all.

Four typed ops under the address shape every other repo op uses (org from the
validated principal, repo from `:name`, never from a body field):

    POST   /v1/git/repos/{name}/pulls                open   → 201
    GET    /v1/git/repos/{name}/pulls?state=          list
    GET    /v1/git/repos/{name}/pulls/{number}        get
    POST   /v1/git/repos/{name}/pulls/{number}/merge  merge

Three decisions worth keeping:

- **A pull is metadata plus two BRANCH NAMES, not a snapshot.** base and head
  keep moving while it is open, so merging asks the question again against the
  refs as they are now. A pinned revision would answer a question nobody asked.
- **Fast-forward only, and it says so.** go-git v5 has no tree-level three-way
  merge; writing one would be writing a merge engine, not a pull request. So base
  moves to head exactly when base is already an ancestor of head, and every other
  case is a 409 naming the reason and the fix. `TestMergeRefusesNonFastForward`
  asserts the branch did NOT move on the refusal — the alternative is an op that
  reports a merge for a base whose commits are nowhere in the result.
- **The ref moves BEFORE the row is settled.** A crash between them leaves a row
  saying `open` about a branch that already contains head, which the next merge
  recognises (base == head ⇒ nothing to move) and settles. The reverse order
  leaves a row claiming a merge that never happened, and nothing in the
  repository can correct that.

Numbering is per-repo and dense from 1, allocated inside `CreatePull`'s
transaction against the single-connection org store. One OPEN pull per
(base, head) — checked in that transaction and backed by a partial unique index —
so a retried agent run leaves one thing to review rather than a pile.

Not built: closing a pull without merging. The loop is open→merge; a proposal
nobody wants is abandoned by deleting its branch, which is how it worked before
there was a row to look at.

## A coding run holds a GRANT, not a credential

`grant.go`. A run used to carry the org's sealed `agent` git token — an ordinary
IAM `sk-` key. IAM resolves that to a user, cloud mints a full org principal from
it, and `cloud.Member` "admits any validated principal": the one process running
untrusted model output held something that opened `/v1/kms/secrets` (every other
secret the org has, including whatever posts to its Slack) and every other
org-scoped API. The push was confined; the credential was not.

A grant is a bounded permission to drive the pack protocol against ONE
repository, creating ONE ref, until it expires. It **authenticates nobody** — it
is not a JWT and carries none of `APIKeyPrefixes`, so `validatedPrincipal`
returns nil, no `X-User-Id` is minted, `principal.Validated` is false, and every
`cloud.Guard` and every `tenantOf` refuses it **by default**. Nothing had to be
told to say no.

The one exception is `resolvePackRepo` (smart_http.go), so the set of endpoints a
grant opens is the set of callers of that function: the three pack handlers, and
nothing else in the binary. A principal always wins — the grant is consulted only
where there is none, so it can never widen an authenticated caller.

It lives in the process that judges it and dies with it: no key to manage, no
signature to verify, nothing at rest to leak, and a restart fails an outstanding
push **closed**. That makes it process-local, which is right for a forge serving
bare repos off one RWO volume; a replicated forge would simply not know a grant
minted elsewhere, which degrades to a refusal and never to an admission.

Minting is `POST /git/grant` on the plane (org from `cloud.Who`, never an
argument) and withdrawal is `POST /git/revoke` by handle, so a grant's life is
the RUN's life and the TTL is only the backstop. **There is no per-org agent git
secret any more** — the constants naming one were deleted rather than left
unused, because a constant naming a secret is an instruction to seal one.

## Internal plane ops

**SIX** typed ops sit on `cloud.Plane()` — the in-fleet call plane, not the
public app, so none appears in plugin/git/openapi.json. This section said TWO for
months, which is the count going stale the way every count in prose does; read it
off the source instead: `grep -n 'cloud.Plane()' apps/git/*.go`.

    /git/landed                              link.go (the provider's GitHub push webhook)
    /git/mirror                              mirror_control.go:90
    /git/files   /git/rev                    files.go:105,108
    /git/figures                             figures_rpc.go:37
    /git/publish                             community.go:159

Their handlers are NAMED (`planeFiles`, `planePublish`, …) on purpose: zipdoc
lifts prose only from a named function or method — a closure is a call expression
with nothing to read. They were first registered as closures and committed WITHOUT
regenerating zipdoc_gen.go, which left `zipdoc -check` red for this package on
main; if you add a plane op, name the handler, write the doc comment, and run
`go generate -run zipdoc ./...` here before committing.

## Two bridges on one group, and each carries what the other cannot

git installs `cloud.Bridge()` — the fleet's ONE client for the facts a typed
signature drops (the validated org, validated-ness itself, the brand, the project)
— and `bridgePrincipal` beside it, on the same `/v1/git` group ahead of every leaf
(git.go). Nesting is harmless by design: the inner one is what the handler sees
and the outer finds nothing to apply.

It carried only its own for a long time, and the recorded reason for that is now
half stale. It said `cloud.Bridge` "parks the ORG and nothing else"; it parks the
project too (`principal.WithProject`). But `principal.ProjectFrom` answers the
header VERBATIM, and git's `tenant.project` is the FOLDED scope — `projectScope`
degrades `default`, an over-long value and one that fails `projectRE` to `""`,
because that string is a physical key every repo row is stored under. So the two
facts git's own bridge carries are the folded project and the ACTING USER, the
who a record names (a pull request's author, a push's pusher), which `principal` has
no context form for at all. Both ride ONE `tenant` value with the org, so a
handler cannot hold one and forget the others (ops.go).

So `bridgePrincipal` is no longer a second implementation of the shared client;
it is the one fact the shared client does not carry. The cost of the old
arrangement was real and worth stating: a fleet-wide change to `cloud.Bridge`
did not reach git at all. The way to delete git's copy entirely is to widen the
shared one to park the whole principal.

## Known class instances (blocked on zip, counted not prosed)

- **The ZAP five have no declarable RESPONSE, and that is a shared-name problem,
  not a missing client.** Their envelope is real (`cloud.OK` → `{status, msg,
  data}`), but `data` is `repoView` / `[]repoView` / `usageView` — names zip's
  typed fold ALREADY publishes as components off the typed /v1 ops. Reflecting
  them a second time through `openapi.schemaOf` would put two derivations behind
  one schema name, which is exactly what `openapi.Compose` refuses ("every
  generated SDK would bind whichever it read last"). So the request halves are
  declared and the response halves wait on the two clients agreeing who owns a
  shared view type. The pack responses and the six HTML pages have no client at
  all: `spec.Binary` is request-only by design, and a text/html response is
  the second half it deliberately does not invent.
- Bodyless POST (playbook #7 in the root LLM.md): **0** — `POST
  /v1/git/repos/{name}/gc` used to publish a required body over `repoRef`,
  whose only property is the `name` path param; zip now recognises an In whose
  every field is URL-borne and publishes no requestBody for it.
- **CLOSED: the four void ops declared a status they never sent.** `noContent`
  was a DEFINED type (`type noContent struct{}`), and zip keys 204 on the Out
  type having NO NAME — so `DELETE /v1/git/repos/{name}`, `/keys/{id}` (gone since),
  `/repos/{name}/subscriptions/{id}` and `/repos/{name}/mirrors/{id}` published
  "200 with a `noContent` body" about a wire that has always answered 204 with
  none (git_test.go:188, ssh_test.go:118, lifecycle_test.go:95,170,434). git was
  the only app in the fleet that defined the type rather than aliasing it, and
  the only publisher of a `noContent` schema. Now `type noContent = struct{}`
  (ops.go) and `TestVoidOpsPublishTheStatusTheySend` holds the document to the
  wire per op, so the two cannot drift apart again.
- Conditional status (playbook "Statuses"): **0** — every git op has one
  success status; the four 201 creators declare `zip.WithStatus`.
- **CLOSED: the `summary` carried a raw newline** — was **20 of git's 24** typed
  ops and **215 of the fleet's 387** described operations across 18 packages;
  now **0 and 0**, fixed once in `firstSentence` (zip v1.18.13), never in 215
  doc comments. It returned the substring up to the first `". "` VERBATIM, so a
  first sentence that wraps in the Go source shipped its line break into the
  OpenAPI `summary`, the CLI command summary (clispec.go:41, cli.go:133) and
  every generated SDK's first docstring line.
  Re-measure from the committed subsets, never from prose:

      python3 - <<'EOF'
      import json,glob,os,collections
      n=collections.Counter(); tot=0
      for f in sorted(glob.glob('plugin/*/openapi.json')):
          a=os.path.basename(os.path.dirname(f))
          for p,ops in json.load(open(f)).get('paths',{}).items():
              for m,op in ops.items():
                  s=isinstance(op,dict) and op.get('summary') or ''
                  if '\n' in s: n[a]+=1; tot+=1
      print(tot, n.most_common())
      EOF

  The fix belongs in `firstSentence` — collapse internal whitespace, once — NOT
  in 215 doc comments. Reflowing the prose so sentence one fits one 100-column
  line would leave the class alive for the next op anybody writes and make cloud
  a special case of a general bug. Do not "fix" this file's comments.
- **NEW: THREE of git's typed ops project to ONE CLI command name.** `git
  repos-delete` is the name zip derives for `DELETE /v1/git/repos/{name}`, for
  `DELETE /v1/git/repos/{name}/mirrors/{id}` AND for `DELETE
  /v1/git/repos/{name}/subscriptions/{id}`, so two of the 30 typed ops are
  **unreachable from the CLI projection** — the runner has one name and three
  routes behind it. `commandName` (zip/cli.go:253-311) keeps the path segments
  BEFORE the first parameter and AFTER the last one and drops everything
  between, so `mirrors` and `subscriptions` — the words that say WHICH thing is
  being deleted — never reach the name. Neither the wire nor the document is
  involved: both DELETEs are served, published and described, each with its own
  operationId. It is the CLI, and only the CLI, that cannot spell them apart —
  which is exactly the failure only TYPING can surface, because an untyped route
  has no command at all to collide.

  Reading ONE pull request joined the same class: `GET
  /v1/git/repos/{name}/pulls/{number}` spells `repos-get`, the name `GET
  /v1/git/repos/{name}` already had, because `pulls` sits BETWEEN two parameters
  and is dropped. This pair differs from the DELETEs in one way worth knowing —
  the DELETEs collide with each other so which one answers is arbitrary, whereas
  here the two-segment route wins on specificity, so reading a repo works from
  the CLI and reading a pull request is the projection that is lost. Three of the
  30 typed ops now have no reachable command. Both families are pinned in
  `cliNameCollisions` (typed_wire_test.go) in BOTH directions, so the zip fix
  retires the list instead of outliving it.

  git holds the worst instance in the fleet (three ops on one name). The class is
  **20 colliding names hiding 23 ops across 11 packages**, in four shapes:
  interior segments dropped (git, cloudflare ×2, o11y), a collection colliding
  with its own item when the last word is singular (compliance
  `accreditation-get`, marketing `calendar-get`, framework `get`), PATCH and PUT
  both spelling `update` (base, exec ×4, iam ×2, websearch), and one op mounted
  at two addresses whose only difference is the version segment `isVersion`
  strips (tasks ×5, `/tasks` vs `/v1/tasks`).

  The fix is `commandName` carrying the interior static segments — NOT
  `WithOperationID` here, which would make git's ids a special case (root
  LLM.md, failure mode 6). Re-measure with zip's OWN derivation over the
  committed subsets; a reimplementation of `commandName` in python would be a
  second answer free to disagree with the one the CLI uses:

      mkdir -p /tmp/cliname && cat > /tmp/cliname/main.go <<'EOF'
      package main

      import ("fmt"; "os"; "path/filepath"; "sort"; "github.com/zap-proto/zip")

      func main() {
          files, _ := filepath.Glob("plugin/*/openapi.json")
          sort.Strings(files)
          names, ops := 0, 0
          for _, f := range files {
              b, err := os.ReadFile(f); if err != nil { continue }
              cmds, err := zip.CommandsFromSpec(b); if err != nil { continue }
              by := map[string][]string{}
              for _, c := range cmds { by[c.Service+" "+c.Name] = append(by[c.Service+" "+c.Name], c.Method+" "+c.Path) }
              for n, ps := range by {
                  if len(ps) > 1 { sort.Strings(ps); names++; ops += len(ps) - 1
                      fmt.Println(filepath.Base(filepath.Dir(f)), n, "<-", ps) }
              }
          }
          fmt.Println(names, "colliding names hiding", ops, "ops")
      }
      EOF
      go run /tmp/cliname/main.go   # from the repo root, so it resolves this module's zip
- **CLOSED: the twelve host-gated root paths are gone.** The six root UI pages
  (`/`, `/explore`, `/{org}/{repo}`, and the tree/blob/commits forms) and the six
  root smart-HTTP routes (the org/repo trio and its project-scoped twin) were
  registered on the ROOT router and gated to the git host by `onGitHost`, which
  fell through with `c.Next()` on every other Host. The document has ONE
  `servers` entry, `https://api.hanzo.ai`, and no notion of a per-path host, so
  all twelve landed in `plugin/git/openapi.json` and then in `openapi.yaml`
  unqualified: every SDK generated from the golden gained twelve methods that
  404ed against the server the document itself names. `GET /` was the
  sharpest — its operationId was the bare word `get`.

  It read as the root playbook's #8 shape, a document describing a FALSE wire,
  and the fix was neither of the two the note expected (a per-path `servers`, or
  a host-gated route declaring itself out of the document). It was that the
  routes had no reason to exist: `git.hanzo.ai` is served by the standalone
  forge, a separate process, so nothing in production ever reached them, and a
  manifest prefix must begin with a literal segment, so `/:org` could not be
  declared and under the plugin host they were undeliverable even in dev.
  Deleted. The six browse pages are `/v1/git`, `/v1/git/explore` and the
  `/v1/git/{org}/{repo}` forms — HTML, which is why they are still in
  `untypedByDesign`. Count them from the golden, never from this list:

      python3 - <<'EOF'
      import json
      d=json.load(open('plugin/git/openapi.json'))
      print([p for p in sorted(d['paths']) if not p.startswith('/v1/')])
      EOF

## A repository on another host is LINKED, never copied (`link.go`, `reclaim.go`)

The owner's rule (2026-10-08): history that lives on an external host stays
there; this store links it, and only caches, reads through and relays.

**The link** is one fact on the row, `Repo.Origin`: the host's clone address.
The view derives `link {provider, owner, name, cloneUrl}` from it (`linkOf`,
`parseLink`); `provider` is `github` for github.com and `git` for any other host.
`POST /v1/git/repos/:name/link {source}` records it (made on first use, as a
create is; refused 409 when the repository holds refs here, since a link would
hide them), and `POST /v1/git/link {owner}` (org admins) links every row with no
refs here and no link to `github.com/<owner>/<name>` where GitHub lets the caller
read it, listing the rest with GitHub's answer and the rows holding history.
`owner` defaults to `forge.Owner(org)` (the estate's table). Neither fetches.
`POST /repos/:name/mirror` is gone: a mirror was a permanent copy.

**Reads** (smart-HTTP info/refs and upload-pack, browse
refs/tree/blob/commits/readme/files, the HTML UI, `/git/files` and `/git/rev`,
repo detail, CI's workflow read) all go through `materialize` (or `open`, its
`Repository` form). For a linked row it asks the HOST whether the caller may read
(`answers.see`), then makes the copy answer (`cache.fresh`): filled on a miss,
brought up to date when the host last answered more than `linkFresh` (30s) ago, one
question for N callers, the copy here answering if a refresh fails. **The copy is rows
and packs in the org's objects store, like any repository's, and nothing is on a disk**
(`linkstore.go`, `syncFromHost`): the host's refs are listed first (one request) and when
the store's branches, tags and HEAD already equal them that is the whole cost; otherwise
go-git's `Remote.FetchContext` runs with the store's `Tx` as its storer, so negotiation
reads the store's commits (a refresh downloads what is new), the objects arrive through
`SetEncodedObject` as packs of at most 64 MiB in the bucket, refs move
(`+refs/heads/*`, `+refs/tags/*`, forced, pruned, no tag auto-following) and HEAD is set
from the host's symref, and it all commits as one transaction under the repository's lock
(a failed fetch leaves the last copy; with none, the read fails). Requests go through
`upstream` (hanzoai/outbound: the address judged where it is dialled, no redirect) and
carry the credential over https only. `materialize` returns no directory for a linked
repository; `vault.reading` makes it current and `vault.home` then returns the store, so
info/refs, upload-pack, the browse API and the indexers take the path a native repository
takes. **Limits:** an object over `MaxObject` (256 MiB) cannot be fetched (go-git hands
`SetEncodedObject` the whole object), and a host is spoken to in protocol v0/v1.

**Who may read is the host's answer, never ours.** A repository its host serves
with no credential (`advertised`: info/refs answers anonymously) is read by
anyone, kept 5 min. A private GitHub repository is read by a person whose GitHub
view includes it (provider `IntegrationsGitHubRepo`: their own account, or the
org's installations when they administer the org), kept 1 min per (org, person,
link); by the org itself, a caller with no person (a plane peer, CI delivery),
when its own installation reaches it (`IntegrationsGitHubGrant` read); and by no
anonymous caller. Its cache fills under that org's read token (kept 30 min),
never under a token this deployment holds for a host (`linkEnv` adds no
`gitcred` fallback, and no token over http). A private repository on any other
host is read by no one here. A raw handler states the caller itself (`caller`):
its own context carries none.

**Writes go to the host and none lands here.** A smart-HTTP push is relayed
(`relay`: info/refs?service=git-receive-pack and the pack, as the client sent
them, after the same `checkRefPolicy`) under the write token GitHub grants the
pusher (`IntegrationsGitHubRun`: their push access, or an admin's installation;
revoked after); the client-less `/push` builds its commit on the host's tip in
the store's copy (in a `Tx` that is never committed) and go-git pushes it from there
without force (`send`: it walks from the commit down to what the host has, reading the
store; 409 when the branch moved).
Either marks the cache stale, so the next read shows the push. A linked repo
fires no build here: its host's events are its builds. A pull request merge onto
a linked repo is refused, naming the host. A repository's `sshUrl` is its host's
(`git@github.com:<owner>/<name>.git`, `git@git.hanzo.ai:…`), empty when it is not
linked: this app serves no SSH.

**The code index and the skills follow the host.** A link that is made (or
pointed at another host) ARRIVES (`arrive`): as the org itself, the cache is
filled and the host's default branch and tip go to `indexOnPush` and
`skillsOnPush`, the same reactors a push landing here runs. Each push GitHub
reports does the same: the provider app's push webhook (`apps/provider
github_webhook.go tellGit`, for the org the signed installation names, every
pusher, before the token mint) calls `git_landed` (`planeLanded`), which marks
every org repository linked to that address stale and arrives at a default-branch
push; a tag, or a branch that is not the cache's default, is read on the next
read and indexed nowhere. Arrivals run two at a time (`arrivals`) under the
service's life, and `Shutdown` waits for them, so a sweep that links hundreds
fills a few at a time. The index and skills readers, and the durable index
activity, open through the cache as the org (`reach`), so an evicted cache is
fetched back rather than failing the job. A repository linked under a name
GitHub has since renamed is matched by the name in its link, so its pushes are
not reported until it is linked under the current name.

**The forge is the one home of a repository no other host holds** (owner's
call, 2026-10-08). A link to `git.hanzo.ai` is provider `forge`, read and written
through the same cache and relay as GitHub's: the forge's ACL decides, asked as
the person (`forgeAs`: their forge login from `forge.Client.Caller`, then
`As`), and the org itself (no person) reads only its own namespace there
(`forgeHome`: the estate's table, else its made workspace). The forge requires a
sign-in even to read, so a fill always carries the deployment's credential
(`forge.Client.Basic`), asked for only after that decision. A push goes as the
pusher: the forge takes a Hanzo IAM token as a git credential for the account
their Hanzo sign-in linked (hanzoai/git `services/auth/iam.go`, `basic.go`), so
cloud relays the caller's own bearer (`bearerOf`: `cloud.CallerBearer`). What
that needs and does not have: a person who never signed in to the forge has no
linked account there, so their token resolves to nobody and the push is 401;
an API key is no person's sign-in and is refused before anything is sent.
`POST /v1/git/repos/:name/home` (org admins) moves a repository whose history is
here to the forge: `forge.Client.Receive` makes it empty in the org's namespace,
every branch and tag is pushed while nothing protects it, the forge's refs are
read back and must equal these, then it is sealed (private, Actions off, the
default branch protected) and the row is linked to it. A forge repository of
that name is never overwritten. The forge's push webhook (`apps/provider
forge_webhook.go forgePush`, the system webhook's Push trigger) tells git as
GitHub's does, for namespaces `forge.Org` maps.

**There is no cache to bound.** `GIT_CACHE_BYTES` and the release of idle linked
directories are gone with the directories: the copy is the bucket's. What a replica
remembers is when it last asked each host (`cache`, 30 s). A row linked to another
repository drops the old copy (`linkTo` removes it from the store). The one scratch
directory left is `exportDir` (a push to the forge or a downstream mirror, which is the
CLI's): made under `GIT_TRANSIT_DIR` (universe: the emptyDir at `/var/lib/cloud/transit`),
holding the branch it sends, removed when the push ends.

**Directories left on the volume** (`audit.go`). A repository made before the store, and
every linked repository's old cache, is a bare directory under
`<data>/git/<org>/<project|_>/<name>.git`. The first sweep of a process after the
adoption sweep (`vault.auditOnce`, where the store is hanzo-sql) walks them, deletes none,
and writes `audit/directories.json` to the objects bucket and a log line
(`git.vault audit`): `deletable` lists every directory that is safe to delete, `unsafe`
every one that is not and why. Safe: a linked repository's cache; a repository whose every
directory ref is in the store at the same commit (or at a commit the store has an object
for, a push since) with HEAD agreeing. Not safe: `orphan` (no metadata row), `unadopted`
(the store holds no rows), `differs` (a ref the store lacks).

**Repositories with no link (Hanzo-only)** are the org's objects store
(`objects/`, `vault.go`, `serve.go`; the root `LLM.md`, "Git objects"), not
directories: object bytes are packfiles in S3 (bucket `GIT_BUCKET`, default
`hanzo-git`), the index and the refs are rows, any replica serves any of them, a
push is one pack streamed to the bucket as it is read and one transaction that indexes it,
and nothing is on a volume. `packBlobs` (blobs.go) builds the S3 `objects.Blobs` from the one
object store (`sdk/object`); a production process with none configured refuses objects. Tests
mount one `objects.Memory` that every service of a test shares, as replicas share
the bucket; `objects/s3_test.go` runs the same cycle against a real server when
`S3_ADMIN_*` names one. `vault.maintain` runs `Store.Maintain` every 10 minutes on
the replica that owns the org's store: geometric repacking per repository, collection per
repository and per shard of the bucket, all under leases (root `LLM.md`, "Git objects"). A repository made before the store existed has a bare
directory under `<data>/git/<org>/<project|_>/<name>.git`; the first use of it, or
the sweep, copies it in, equal, and leaves the directory read-only as the rollback.

**Measured, 2026-11-13, one workstation (10 cores), a local hanzoai/s3 server over loopback,
hanzo-sql on PostgreSQL.** Commands: `go test -run TestMeasurePush/postgres -pushmb=5120`,
`go test -run TestMeasureEstate/postgres -bigmb=1024 -smallobjs=500000 ./apps/git/objects`
(`measure_test.go`; `S3_ADMIN_*` names the server).

- **A 5 GiB push** (1,282 incompressible 4 MiB blobs, one 5.4 GB pack read from a file):
  `Ingest` (read, SHA-1, inflate-and-hash every blob, multipart upload) **40.8 s, 125 MB/s**;
  Go heap peak during it **195 MB**, process peak RSS **219 MB** (56 MB before; the pack is
  never in memory).
- **A 1 GiB repository of 258 large objects**: ingest 7.5 s, attach + commit 26 ms; clone
  (plan + `Repo.Pack`) **1.7 s cold / 2.1 s warm, 480-600 MB/s**, 258 range requests, 3 groups
  in flight; through `git index-pack` 10.5 s (git-bound).
- **A 500,001-object repository of 500,000 small files** (30 MB pack): ingest 1.4 s, attach +
  commit **6.6 s for 501,002 index rows**; clone **plan 0.41 s + pack 2.2 s cold** (0.27 s + 2.0 s
  warm), 367 range requests, heap peak 356 MB (the caches are in it: up to 256 + 64 MiB);
  through `git index-pack` 2.5 s.
- **One commit on the 1 GiB repository** (465-byte thin push, a client that has the old tip):
  ingest 12 ms, commit 3 ms; the fetch is **plan 6 ms + pack 2 ms** cold, 3 objects, 3 range
  requests (1 warm).
- **Read-ahead** against a store that takes 20 ms a request: 12 groups serial 265-287 ms, with
  `readAhead` 77 ms (`TestAClonesRangeReadsRunAhead`). Over loopback, where a request costs
  microseconds, it gains nothing and costs nothing.
- **`ANALYZE entries` after a big stage** (`analyzeAfter` = 5000 new rows): without it, on a
  fresh table PostgreSQL planned the first clone's lookups with no statistics (100,000 objects:
  cold pack 1.51 s against 0.43 s with it). A 500,000-row first run without it took 51 s and 113 s
  against a server that was also degraded, so the two causes are not separated; the measure
  above is with it.
- The `process peak RSS 6187 MB` the estate test prints is the TEST holding a 1 GiB pack and
  1 GiB of random files in memory to generate the repositories, not the store; the push
  measure above reads its pack from a file and is the figure for the store.

What was not measured: the whole path through the HTTP edge (a push above the edge's
`GATEWAY_BODY_LIMIT`, 16 MiB, is refused before the store sees it; see the root LLM.md "A push
is one pack"), a store across a real network (latency enters only through `readAhead`), and
repack of a multi-GiB repository (the merge is streamed and tested at MiB scale).

Tests: `link_test.go` (read from the host, refresh past the window, private
read as GitHub lets the caller with 404/403, push relayed and client-less push
sent upstream with nothing landing here, a link over
history refused, the sweep, a link and a reported push arriving at the index and
the skills while a feature branch and another org's push do not, a tag and a
branch of one name both filled), `apps/provider` `TestAPushTellsGitWhatLanded`, `reclaim_test.go` (a linked repository is in the store and not on the volume, one
question for N misses), `linkstore_test.go` (the copy follows every ref of the host,
deletions included; an unchanged host costs a listing and no pack; relinking drops the old
copy; the same on a real object store), `audit_test.go`.

### What the store held before links, measured 2026-08-21

**201.7G of the 214.3G on cloud's ReadWriteOnce claim was `/var/lib/cloud/git`**,
against 11G for every other thing on it — what pinned the deployment to one
replica. 3,160 bare repositories across 74 orgs; 2,603 (82%) also on the forge;
553 volume-only, all first-party, 54 of them (6.9G) found nowhere on the forge.
Cross-org duplication is real and verified by SHA, not by size (`hanzo-inc/phi`
and `hanzoai/phi`, 10.4G, same HEAD), and `kms` is NOT (`hanzoai/kms` a44b5ea6…
against `hanzo/kms` 8c64ef62…), which is why identity is asserted per repository
and never inferred from a name. The forge's copies are Forgejo pull mirrors of
GitHub; cloud's carried no remote at all.

What had already moved off this store, measured:
measured: `apps/sync`'s importer answers the same `GitImporter` and
`GitMirrorController` clients AGAINST THE FORGE and says so in its own header;
`apps/deploy` and `apps/lsp` already read tree and rev from the forge; the live
push endpoint is `/v1/platform/hook` and `apps/git/webhook.go` is a 410 tombstone
pointing at it. And the SSH entry point was already dead: it bound `:2222`, no
chart or compose exposed it, cloud's Service published no 22 or 2222, and
`gitSSHHost` advertised `git@git.hanzo.ai:` — which resolves to the forge's
Service, not to that listener. It is deleted (ssh.go, pack.go, keys.go,
keystore.go and `/v1/git/keys`, with `ssh_keys.db` and the host key it wrote
under the data root): the forge serves SSH for every repository whose home it is.

## SSH is served again (`ssh.go`, `sshkeys.go`), as part of the forge parity work

The 2026-08 deletion above is reversed on purpose: git.hanzo.ai is to be served by
this app (PARITY.md). The new listener shares nothing with the old one. It starts
only when `GIT_SSH_ADDR` is set and refuses to start without `GIT_SSH_HOST_KEY` (a
generated key would differ per replica). Keys are rows of the system-namespace
store `ssh_keys` on hanzo-sql, so any replica authenticates any key; a fingerprint
belongs to one row across all orgs. It serves repositories in the objects store
through the same `advertisement` / `serveFetch` / `receive` the HTTP transport
calls (`afterReceive` is the shared push effect), adapting git's stateful protocol:
fetch answers each have-round NAK and hands the accumulated request to serveFetch
on `done`; push finds the pack's end from its own framing. Not served: linked
repositories, directory-backed repositories, protocol v2.

## Run logs (`apps/git/logs.go`)

A daemon's task log is objects under `logs/<org>/<task>/` in the git bucket (`packBlobs`, `GIT_BUCKET`), one per
append, named by the index of its first line (12 digits), so a resend writes the same key. A read lists the prefix,
takes the last `taskLogRead` bytes (a range of the first chunk it needs) and streams them through the same line
reader. The files this host kept (`<data>/git/logs/<org>/<task>.jsonl`) are carried once: at mount (`carryLogs`) and
on the first read of a task (`carryOne`), as object `<task>/000000000000-file`, then renamed `.carried`. A deployment
with no object store refuses a runner's log append (503), as it refuses pack bytes.
