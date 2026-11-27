# LLM.md — apps/studio

Guidance for apps/studio. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Hanzo Studio (`apps/studio`, `/v1/studio`)

Every creation — image, poster, document, deck, type system, 3D model, ad, film,
voice, music — is a RUN of a graph of steps that the person who started it owns.
Eager, for auto's reason: runs are durable workflows on this process's engine, and
the render lane's workers claim from it.

| file | what it owns |
|---|---|
| `wire.go` | the contract's shapes (`Graph`, `Spec`, `Run`, `Step`, `Template`, `Flow`, `Document`, `Brand`, pages, inputs) |
| `graph.go` | the op table `known` (executor, required params, meter, floor), graph checks, `{{path}}` resolution, the graph as a flow/core graph |
| `durable.go` | the workflow `studio.run`: flow/core's one Scheduler in `flow.Durable` mode, backed by `stepper`, which runs each step as its parts' activities (a fan-out's parts dispatched together) |
| `activities.go` | `studio.load`, `studio.part`, `studio.step`, `studio.close` |
| `exec.go` | the gateway executor (chat via deps.AI; media via the `ai_media` plane op), lane job inputs, the native seam (`flow.run`, no op routed yet) |
| `lane.go` | the render lane (this engine, shard `studio-render`, namespace `render-jobs`), the GPU lane (the workflow app's `gpu-jobs`, over `workflow_enqueue/activity/cancel`), the render door |
| `library.go`, `planes.go` | every file is a Library item (`library_put`, or `library_reserve` + PUT + `library_commit`), written and read AS the run's person; `auto_job_track` |
| `money.go` | estimate, reserve (402), per-part debit, settle |
| `events.go` | `GET /v1/studio/runs/:id/events`: SSE from the `events` rows, replayable by Last-Event-ID |
| `chat.go` | `POST /v1/studio/chat` (SSE) and threads |
| `saved.go`, `catalog.go`, `catalog.json` | templates (catalog + org), flows, documents, brands |
| `store.go` | the tables, on `locus.Tenants` (hanzo-sql database `studio`, RLS on `org_id`; SQLite off-cluster) |

**A run is durable twice.** The engine replays the workflow from history, so a
step that finished resolves from history; and each part records its result in
`parts` before it answers, so a retry (a new workflow, `<run>.<attempt>`) finds
every part that already succeeded and neither runs nor charges it again. A lane
job's id is derived (`<run>_<step>_<item>_<attempt>`), so a part re-dispatched by
Recover after a restart finds the job it made. `TestARunKilledMidStepResumes`
restarts the engine mid-render and holds the finished step to one model call.

**Ownership.** Org from `principal.Acting`, creator from `principal.SelfFrom`
(refused when the credential names no person). Rows are the creator's until
`shared`; another org gets 404 for everything. The store binds the org twice:
orm/tenant's builder and PostgreSQL row-level security.

**The render lane is NOT in an org shard of the workflow app.** org hanzo holds
every self-serve customer, and anything there is listable and claimable through
`/v1/workflow`. Render jobs are standalone activities in THIS engine under the
shard `studio-render`, which no request maps to; workers reach them only through
`POST /v1/studio/render/*`, which admits `STUDIO_RENDER_WORKERS` (default
`app:hanzo/hanzo-render`, the `render` workload) and 403s everyone else. A job
carries signed GET URLs for its inputs (minted by the Library for the run's
person) and no output: the worker asks the door's `output` verb once the file
exists, the door reserves the Library item as the run's person, and the part
commits it when the job completes. Worker: `hanzoai/render` (Node 24, Chrome,
FFmpeg, @hanzo/frames); deployment `charts/app/values/hanzo/render.yaml`.

**Money.** `POST /v1/studio/runs` prices every step (`RateCents("studio", meter,
floor)`) and authorizes estimate + everything the org's open runs still hold;
402 when the wallet cannot. Each finished part is debited once under
`studio_<run>_<step>_<item>`; chat is debited by deps.AI's own meter and only
reserved here. Commerce has no credit verb, so the estimate is held, never taken.

**Media.** `ai_media` (apps/ai/media_rpc.go) buys one media call for the stated
caller exactly as `ai_chat` buys a completion: the model API covers it
(`callerPays`) and Studio's meter charges it.

**GPU (staged).** `studio.render` enqueues on the org's `gpu-jobs` lane with the
graph as `prompt` and signed inputs; landing its files needs the GPU worker
(`hanzo link`) to upload through the door's `output` verb, which it does not yet.
