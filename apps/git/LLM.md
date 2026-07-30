# apps/git — typed-op status

COMPLETE at **24 typed / 24 refused**, and that partition is now a **GATE, not
prose**: `untypedByDesign` (typed_wire_test.go) is the closed list of the 24
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
handlers and against zip v1.18.6 itself, not against this file. Do not re-type
them; run the gate before believing any route counter that says git has untyped
work left.

## The four refusal families

1. **POST /v1/git/webhook** (git.go:296, handler webhook.go:73) — 1 route.
   Auth IS the HMAC over the RAW received bytes, verified before parse
   (webhook.go:79); zip's `invoke` unmarshals BEFORE the handler (typed.go),
   so a typed In destroys the exact bytes the signature covers. It also
   answers a benign 204 to deliveries it ignores (non-push, ref delete, bot)
   where a typed op's decode failure would 400 and retry-storm the forge, and
   it is wrapped in cloud.Terminal so its 401/400 cannot be flattened by the
   co-mounted /v1 error handler.

2. **Smart-HTTP git protocol** (git.go:334-336 under /v1/git, git.go:344-346
   root-level on the git host) — 6 routes. The git pack wire: request bodies
   are `application/x-git-*-request` pack streams, responses are
   `application/x-git-*-advertisement|result` streamed via SendStream
   (smart_http.go:73,100-102,151) — not JSON in either direction. The
   root-level trio additionally falls through with c.Next() on non-git hosts
   (onGitHost, git.go:404), and a typed dispatch cannot decline into the next
   route.

3. **Browser UI** (ui.go:47-60) — 12 routes. Server-rendered text/html
   (html/template); a typed dispatch ends in c.JSON(out). The root-level six
   are also host-gated with the same c.Next() fall-through as family 2.

4. **ZAP procedure adapters** (zap.go:69-73) — 5 routes. The published
   envelope contract: success is the cloud.OK envelope, failure is a non-2xx
   `{status:"error", msg}` body (zap.go:88), while a typed op's returned error
   renders zip's `HTTPError` — `{status:<int>, code, error:<msg>}`
   (zip/ctx.go:200-206, written by `errorHandler` at ctx.go:222) — so typing
   both RENAMES the message field (`msg`→`error`) and changes the TYPE of
   `status` (string→int) on a wire the bridge's clients parse. There is no shim:
   `cloud.Bridge` applies a handler-set status only when the handler returned
   `err == nil` (typed.go:78), and the only exported setters are
   `Created`/`Accepted`, so nothing lets a typed op answer a 4xx with a body of
   its own. Expected to shrink: the shared /zap plane already replays the typed
   /v1 ops frame-for-frame (see zap.go's header comment); retiring these is a
   client migration, not a typing task.

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

- Bodyless POST (playbook #7 in the root LLM.md): **1** — `POST
  /v1/git/repos/{name}/gc` publishes a required body over `repoRef`, whose
  only property is the `name` path param. Wire unharmed (bindURL binds the
  path last); waits on zip declaring a bodyless POST.
- Conditional status (playbook "Statuses"): **0** — every git op has one
  success status; the four 201 creators declare `zip.WithStatus`.
- **NEW: the `summary` carries a raw newline** — **20 of git's 24** typed ops,
  and **215 of the fleet's 387** described operations across 18 packages.
  `firstSentence` (zip/openapi.go:606) returns the substring up to the first
  `". "` VERBATIM, so a first sentence that wraps in the Go source ships its
  line break into the OpenAPI `summary`, the CLI command summary
  (clispec.go:41, cli.go:133) and every generated SDK's first docstring line.
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
  /v1/git/repos/{name}/subscriptions/{id}`, so two of the 24 typed ops are
  **unreachable from the CLI projection** — the runner has one name and three
  routes behind it. `commandName` (zip/cli.go:253-311) keeps the path segments
  BEFORE the first parameter and AFTER the last one and drops everything
  between, so `mirrors` and `subscriptions` — the words that say WHICH thing is
  being deleted — never reach the name. Neither the wire nor the document is
  involved: both DELETEs are served, published and described, each with its own
  operationId. It is the CLI, and only the CLI, that cannot spell them apart —
  which is exactly the failure only TYPING can surface, because an untyped route
  has no command at all to collide.

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

- **NEW: NINE host-gated root paths are published as if `api.hanzo.ai` served
  them.** The six root UI pages (`/`, `/explore`, `/{org}/{repo}`, and the
  tree/blob/commits forms) and the three root smart-HTTP routes are registered on
  the ROOT router and gated to the git host by `onGitHost`, which falls through
  with `c.Next()` on every other Host — `TestRootSmartHTTP_HostGuard` and
  `TestRootUI_HostGuard` pin the 404 on `api.hanzo.test`. The document has ONE
  `servers` entry, `https://api.hanzo.ai`, and no notion of a per-path host, so
  all nine land in `plugin/git/openapi.json` and then in `openapi.yaml`
  unqualified: every SDK generated from the golden gains nine methods that 404
  against the server the document itself names. `GET /` is the sharpest — its
  operationId is the bare word `get`.

  This is the root playbook's #8 shape (a document describing a FALSE wire), not
  its #7 shape (a true wire under-described), and it is the one class here that
  cannot be closed inside this package: either the projection learns a per-path
  `servers` (OpenAPI 3.1 allows it on a path item) or a host-gated route declares
  itself out of the document. The `/git/*` six are NOT in this nine — those are
  registered without a host gate and do serve on every host, so publishing them
  is true (they answer HTML, which is why they are in `untypedByDesign`). Count
  them from the golden, never from this list:

      python3 - <<'EOF'
      import json
      d=json.load(open('plugin/git/openapi.json'))
      print([p for p in sorted(d['paths']) if not p.startswith(('/v1/','/git'))])
      EOF
