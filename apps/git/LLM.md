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
  `plugin/git/seams.go` that published them. A run's ref check read a bare repo
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

Both doors take the actor — the sandbox one to be handed a key, the ROUTED one
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

**Still here, and still on local bare repos:** the import/inbound-sync pair, the
outbound mirror, the code index, Slack notify, the deploy tree read, the LSP
tree/rev reads, project visibility and the git figures. Retiring those needs a
decision that is NOT a coding-path decision — see the inbound fast-forward guard
below.

**The open blocker.** `InboundSync` is fast-forward-only and reports a
divergence as a Conflict with native PRESERVED. Forgejo's pull mirror is
`git remote update --prune` over a mirror refspec (`services/mirror/mirror_pull.go`),
which force-overwrites, and its push mirror is `git push --mirror`
(`services/mirror/mirror_push.go`), which pushes every ref. Neither preserves the
guard. Moving inbound sync to the forge therefore needs a decision: accept
upstream-wins, or have cloud fetch and push fast-forward-only itself.


COMPLETE at **28 typed / 18 refused**, and that partition is now a **GATE, not
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
seam; routes() in git.go registers them on the /v1/git group behind
bridgePrincipal). The four refusal families below were re-verified against the
handlers and against zip v1.18.15 itself, not against this file. Do not re-type
them; run the gate before believing any route counter that says git has untyped
work left.

**A refusal is still DESCRIBED.** Seven of the 18 read a request body, and each
declares it through `openapi.Register` (webhook.go's init) without becoming a
typed op: the three ZAP procedures that bind one, and the four pack POSTs as
`openapi.Binary`. They previously published an operationId, tags and nothing
else — which no SDK generator can tell apart from a route that takes no body.
`declaredBodies` + `TestRefusedRoutesDeclareTheBodyTheyRead` pin it both ways: a
declared body that vanishes fails, and a body declared for one of the eleven
that read none fails too. What a refusal still cannot have, and only a typed op
gives, is prose, an MCP tool and a CLI command.

## The four refusal families

1. **POST /v1/git/webhook** (git.go:315, handler webhook.go:159) — 1 route.
   Auth IS the HMAC over the RAW received bytes, verified before parse
   (webhook.go:165); zip's `invoke` unmarshals BEFORE the handler (typed.go),
   so a typed In destroys the exact bytes the signature covers. It also
   answers a benign 204 to deliveries it ignores (non-push, ref delete, bot)
   where a typed op's decode failure would 400 and retry-storm the forge, and
   it is wrapped in cloud.Terminal so its 401/400 cannot be flattened by the
   co-mounted /v1 error handler.

2. **Smart-HTTP git protocol** (routes() in git.go, under /v1/git) — 6 routes:
   the org/repo trio and the project-scoped org/project/repo trio. The git pack
   wire: request bodies are `application/x-git-*-request` pack streams,
   responses are `application/x-git-*-advertisement|result`, the packfile
   streamed via SendStream (smart_http.go:151,180) — not JSON in either
   direction.

3. **Browser UI** (uiRoutes in ui.go) — 6 routes, under /v1/git beside the JSON
   ops. Server-rendered text/html (html/template); a typed dispatch ends in
   c.JSON(out).

4. **ZAP procedure adapters** (zap.go:120-124) — 5 routes. The published
   envelope contract: success is the cloud.OK envelope, failure is a non-2xx
   `{status:"error", msg}` body (zap.go:138), while a typed op's returned error
   renders zip's `HTTPError` — `{status:<int>, code, error:<msg>}`
   (zip/ctx.go:200-206, written by `errorHandler` at ctx.go:222) — so typing
   both RENAMES the message field (`msg`→`error`) and changes the TYPE of
   `status` (string→int) on a wire the bridge's clients parse. `msg` is
   LOAD-BEARING, not cosmetic: `zapface/dispatch.go:89-91` unmarshals the
   non-2xx envelope and forwards `env.Msg` to the ZAP client as its error text,
   so a typed op's `{..., error}` body decodes to an empty `Msg` and every ZAP
   failure arrives with no message at all. There is no shim:
   `cloud.Bridge` applies a handler-set status only when the handler returned
   `err == nil` (typed.go:78), and the only exported setters are
   `Created`/`Accepted`, so nothing lets a typed op answer a 4xx with a body of
   its own. Expected to shrink: the shared /zap plane already replays the typed
   /v1 ops frame-for-frame (see zap.go's header comment); retiring these is a
   client migration, not a typing task.

## The ref policy stands in NINE doors, not one

`refpolicy.go` used to say receive-pack was "the point every path that changes a
ref passes through and no client can decline to". That was false, and it was the
whole argument. It guarded **one** of eight ref writers into the same bare repo;
a run refused at the front door walked in through any of the other seven with
the same credential — most cheaply through `POST /repos/:name/push`, which lands
a **fast-forward child** on the branch a reviewer just approved and so trips no
"the branch was rewritten" signal anywhere.

The writers, and how each states its intent, are enumerated in `refpolicy.go`'s
own doc comment (writers 1–9) so the list lives beside the rule. Two of them
cannot be judged command-by-command and are refused **structurally** instead:
the mirror's `+refs/*:refs/*` takes a negative refspec (`^refs/heads/agent/*`,
git ≥ 2.29) so the machine namespace is outside its refmap and therefore outside
`--prune`; and `HEAD` is not under `refs/`, so the two importers and the
client-less push run it past `checkHeadRef`.

Writer 9 is the newest: **merging a pull request** (`merge.go fastForward`)
advances base, which is a ref write, so it states its one command as a
`refCommand` and calls the same `checkRefPolicy`. It arrived guarded rather than
being retrofitted, which is this list doing its job. It is also the only writer
that COMPARE-AND-SETS: it read base in order to judge the merge, so it hands
that value back to go-git and the write fails rather than silently discarding a
push that landed in between.

**A tenth door is a change to that list, not just a new function.** The proof
that each is closed is `refwriters_wire_test.go` — real git CLI, real SSH
listener, real server, one test per door.

## The pull request is a native noun, not a GitHub round trip

`pulls.go` (the noun + its four ops) and `merge.go` (the one thing it does to
the repository). Before it, an agent could push a branch and had nowhere to
propose it: `propose.go` answers "where do I read this" by opening a REAL pull
request on GitHub when the repo mirrors there, and by returning a branch URL when
it does not — so a repository living only in the forge had no door at all.

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

The one exception is `resolvePackRepo` (smart_http.go), so the set of doors a
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

`POST /git/files` (files.go) and `POST /git/publish` (community.go) are typed
ops on `cloud.Plane()` — the in-fleet call plane, not the public app, so they
never appear in plugin/git/openapi.json. Their handlers are NAMED
(`planeFiles`, `planePublish`) on purpose: zipdoc lifts prose only from a
named function or method — a closure is a call expression with nothing to
read. They were first registered as closures and committed WITHOUT
regenerating zipdoc_gen.go, which left `zipdoc -check` red for this package on
main; if you add a plane op, name the handler, write the doc comment, and run
`go generate -run zipdoc ./...` here before committing.

## Why the principal bridge is git-local

Twenty apps install `cloud.Bridge()` on their group; git is the only one that
parks its own value (`bridgePrincipal` + `tenantOf`, ops.go). That is not a
divergence for its own sake: `cloud.Bridge` parks the ORG and nothing else
(`principal.WithOrg` → `principal.OrgFrom`), and git's typed ops need two more
facts off the validated request — the `X-Project-Id` sub-scope every repo row is
keyed by, and the acting user an SSH key is owned by (keys.go). `principal` has
no context form for either, so an app needing them has exactly one option today,
which is the one this package took. The way to make it ONE bridge again is to
widen the shared one — park the whole principal, not just the org — and delete
this copy; typing more apps that carry a project scope will keep re-finding it.

## Known class instances (blocked on zip, counted not prosed)

- **The ZAP five have no declarable RESPONSE, and that is a shared-name problem,
  not a missing seam.** Their envelope is real (`cloud.OK` → `{status, msg,
  data}`), but `data` is `repoView` / `[]repoView` / `usageView` — names zip's
  typed fold ALREADY publishes as components off the typed /v1 ops. Reflecting
  them a second time through `openapi.schemaOf` would put two derivations behind
  one schema name, which is exactly what `openapi.Weave` refuses ("every
  generated SDK would bind whichever it read last"). So the request halves are
  declared and the response halves wait on the two seams agreeing who owns a
  shared view type. The pack responses and the twelve HTML pages have no seam at
  all: `openapi.Binary` is request-only by design, and a text/html response is
  the second half it deliberately does not invent.
- Bodyless POST (playbook #7 in the root LLM.md): **0** — `POST
  /v1/git/repos/{name}/gc` used to publish a required body over `repoRef`,
  whose only property is the `name` path param; zip now recognises an In whose
  every field is URL-borne and publishes no requestBody for it.
- **CLOSED: the four void ops declared a status they never sent.** `noContent`
  was a DEFINED type (`type noContent struct{}`), and zip keys 204 on the Out
  type having NO NAME — so `DELETE /v1/git/repos/{name}`, `/keys/{id}`,
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
  /v1/git/repos/{name}/subscriptions/{id}`, so two of the 28 typed ops are
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
  28 typed ops now have no reachable command. Both families are pinned in
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

## The store is a CACHE, and only a copy is ever released (`reclaim.go`)

**201.7G of the 214.3G on cloud's ReadWriteOnce claim was `/var/lib/cloud/git`**,
against 11G for every other thing on it. That is what pins the deployment to one
replica: a claim that large is RWO, RWO attaches to one node, and the chart
refuses a second pod rather than watch it hang on Multi-Attach. Measured on the
live volume, 2026-08-21:

| | |
|---|---|
| bare repositories | 3,160 across 74 orgs, all org-level (`_`) |
| also on the forge | 2,603 (82%) |
| volume-only | 553 — of which 74 are empty shells, 344 under 10MB, **137 substantial (29.3G)** |
| substantial volume-only whose tip IS on the forge under another owner | 87 (22.5G) |
| substantial volume-only found NOWHERE on the forge | **54 (6.9G)** |

**Every third-party org is 100% forge-backed.** The volume-only set is entirely
first-party (`hanzo-inc` 343, `hanzo` 73, `hanzo-templates` 69, `hanzoai` 12,
`luxdao` 7, …) plus `maxpower` (2 repos, 132KB) and `admin/tel-probe-a` (36KB).
Cross-org duplication is real and verified by SHA, not by size:
`hanzo-inc/phi` and `hanzoai/phi` are both 10.4G at the same HEAD
`9c96a299…`; so are `enso-browser`, `insights` and `erp`. `kms` is NOT
(`hanzoai/kms` a44b5ea6… against `hanzo/kms` 8c64ef62…), which is why identity
is asserted per repository and never inferred from a name.

**Both stores are independent mirrors of the same GitHub estate.** The forge's
repos carry `remote.*.url = https://github.com/<org>/<repo>.git` with
`mirror = true` — Forgejo pull mirrors. Cloud's carry `[core] bare = true` and
NOTHING else: no remote, no record of where the bytes came from. So the two
copies never knew about each other, and nothing on this volume can say what it
is a copy OF.

### The rule: a copy with an ORIGIN can be made again

`Repo.Origin` is the URL a repo's objects can be fetched from, written by
`ops.mirror` on a fetch that SUCCEEDED — a source that has answered, not a claim
a caller made. It lives on the metadata ROW because eviction deletes the
directory a git config would be in, and a fact that dies with the thing it
describes cannot be the reason it was safe to delete it.

Everything else is PINNED, and the failure direction is the safe one in both
halves: an unrecorded origin costs disk, and disk is recoverable. So all 553
volume-only repos are pinned by construction — no migration decision is required
before turning the bound on, and none is taken by turning it on.

**Idle is the safety criterion, size is the target** — the split
`cloud.OrgStore`'s reclaim makes, for the same reason: `materialize` hands back a
bare directory its caller streams a pack out of long after the call returns. A
repo is a candidate only after `repoIdle` untouched AND with no reader inside it,
and among candidates the COLDEST goes first. Over the bound with nothing
releasable is SAID and the store stays over.

**`GIT_CACHE_BYTES` is unset by default and unset means unbounded.** Bytes, not a
count: the 3,160 repositories range from 36KB to 10.4GB.

### A LANDED LOCAL WRITE ENDS THE COPY, and a test is why we know

The first version released on the origin alone. Measured, in
`TestPushIsStillADeployAfterARelease`: a push landed on a released-and-refetched
repo, the reader let go, reclaim released it again, and the next clone refetched
the UPSTREAM and answered with its tip — the pushed commit gone, 200 OK, nothing
logged. A repository that has been written to is not a copy of anything.

`fireBranchBuild` clears the origin, and it is the right place because it is
already **the** place every local ref advance is announced — HTTP receive-pack,
SSH receive-pack, the client-less `/push` and a pull-request merge all funnel
through it (`smart_http.go`). A tenth writer announces itself there or it fires
no build, and a repo whose pushes fire no build is a defect somebody notices; a
repo that quietly lost a commit to a refetch is not.

It clears BOTH the row and the live cache entry (`cache.diverged`). The entry is
a projection of the row read at the start of the serve, so clearing only the row
leaves the in-flight entry still claiming an origin — which is the second bug the
same test found, one layer down.

### Why this is NOT a thin proxy to the forge, yet

The forge is the authority and `apps/git` imports `forge` NOWHERE, so the obvious
answer is to serve reads through it. The read seam even exists and is clean:
`Repository` (`repository.go`) with `openRepository` (`gitbackend.go`) as its ONE
door, so a `forgeRepository` would move browse, the UI, the code index, the CI
config read and repo detail with no handler change.

**What blocks it is addressing, not plumbing.** `forge.Owner` is a CLOSED table
holding exactly `{"hanzo": "hanzoai"}`, and its closedness is a documented
control: it used to fall back to the org's own name, which made an IAM org a
forge coordinate whenever it was unmapped — sign up as `hanzoai` and every read
and write addresses the estate's own repositories, on repos carrying Actions
workflows on in-cluster runners. So 73 of the 74 orgs on this volume have no
forge coordinate cloud may derive, and widening the table to give them one is
that vulnerability, not a config change.

The pack plane is blocked by the same fact from the other side: a transparent
proxy must present a credential the forge accepts for git-http, and the only one
this process holds is the machine token — a site administrator — which is
precisely the privileged-credential presentation `mirrorOutHostAllowed` refuses
(Red MED-1). A redirect instead of a proxy moves the credential problem to the
client, which then needs a forge login it does not have.

**An ORIGIN is not that mapping and does not need the table.** It is a URL an
operator states per repository, exactly as `mirrorReq.Source` already is — an
operator statement about one repo, never a namespace derived from a tenant's
name. That is the whole reason this shape is available today and a proxy is not.

Two supporting facts about how far the retirement has already gone, both
measured: `apps/sync`'s importer answers the same `GitImporter` and
`GitMirrorController` seams AGAINST THE FORGE and says so in its own header;
`apps/deploy` and `apps/lsp` already read tree and rev from the forge; the live
push door is `/v1/platform/hook` and `apps/git/webhook.go` is a 410 tombstone
pointing at it. And the SSH front door is already dead: it binds `:2222`, no
chart or compose exposes it, cloud's Service publishes no 22 or 2222, and
`gitSSHHost` advertises `git@git.hanzo.ai:` — which resolves to the Forgejo
Service, not to this listener.
