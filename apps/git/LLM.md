# apps/git — typed-op status

COMPLETE at **24 typed / 24 refused**. Every route with a JSON request/response
shape is a typed op (ops.go states the seam; routes() in git.go registers them
on the /v1/git group behind bridgePrincipal). The 24 raw registrations that
remain each carry a wire fact a typed op cannot express — verified against the
handlers, not the prose. Do not re-type them; re-measure here before believing
any route counter that says git has untyped work left.

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
   renders zip's flat `{status, code, error}` — typing renames the error field
   on a wire the bridge's clients parse. Expected to shrink: the shared /zap
   plane already replays the typed /v1 ops frame-for-frame (see zap.go's
   header comment); retiring these is a client migration, not a typing task.

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

## Known class instances (blocked on zip, counted not prosed)

- Bodyless POST (playbook #7 in the root LLM.md): **1** — `POST
  /v1/git/repos/{name}/gc` publishes a required body over `repoRef`, whose
  only property is the `name` path param. Wire unharmed (bindURL binds the
  path last); waits on zip declaring a bodyless POST.
- Conditional status (playbook "Statuses"): **0** — every git op has one
  success status; the four 201 creators declare `zip.WithStatus`.
