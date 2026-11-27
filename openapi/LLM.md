# LLM.md — openapi

Guidance for openapi. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Two packages: `sdk/spec` declares and projects one app, `openapi` composes the fleet

`sdk/spec` (public: `github.com/hanzoai/cloud/spec`) is what an APP needs. It holds
what an app states because the router cannot: bodies (`Register`, `Bytes`, `Binary`,
`OneOf`, `Params`, `Answers`, `Takes`), prose (`Describe`, `DescribeRest`), the
credential (`Open`), a GET that writes (`Writes`), a route that stands for an API
(`Front` + `Mounted`/`Table`), the document model (`Document`, `Operation`, `Schema`…)
and the per-app projection a plugin serves for `describe`: `spec.Of(app, info)` (was
`openapi.Spec`; `spec.Spec` would stutter) = `Live` + `From` + `Typed` + `Fold` +
`Project` + `Stamp` + `Secure` + writes. It knows no manifest, brand or fleet.

`openapi` is the HOST half and imports it: `Compose`/`Fleet`/`Publish`, the floor
and untyped ratchets, `Misfiled`, GraphQL, the agent door, the index, the command
projection, `Use` (serves the document, commands and slices) and `Synopsis`. The one
fact the two share about each other is `spec.Reserve`: the host states, from its own
init, the addresses it answers that no app owns (the document, its alias, the
commands, the MCP endpoint, the index, the agent rows), and `spec.Complete` reads
them (`spec.Reserved`) to tell a prose declaration no app can own from a misfiled one.
`spec.RootPath` lives in spec because the audience rule (`Stamp`) names it.
Tests that need the manifest stay here (`complete_test.go`); the `describe` gate's
own tests that need the registry live beside it in `sdk/spec`.

## The route table has three projections, and the router is the source

`serve.go` composes ONE route table and projects it three ways, all after
`MountAll` so each sees a complete table: `/zap` REPLAYS the /v1 handlers
(zapface), the console RENDERS them, and `GET /v1/openapi.json` DESCRIBES them
(`openapi.Mount`). None holds a second copy of anything; none can drift.

(The document itself now has four SINKS — the live endpoint, the committed
`openapi.yaml` golden, each app binary's own subset, and the weave of those
subsets. They are four renderings of one value, not four projections; see "The
document pipeline" below.)

- **The public `/v1/openapi.json` is the HOST's, and it is the WEAVE — not a live
  router** (`cmd/cloud` `spec()` → `openapi.MountFleet`). `openapi.Mount` above is
  right for a process that IS the API; the deployed entry point is not one. The
  light host mounts no subsystem, so reading its live router describes 113 proxy
  prefixes and a console catch-all — and mounting the fleet to answer a public GET
  is exactly the cost laziness exists to avoid. Left unclaimed the path was not
  unrouted but MISrouted: it fell to ai's bare `/v1`, and api.hanzo.ai published
  the ai child's own 8-path document as the whole API, 200 OK, to every SDK
  generator that read it. The host answers from `plugin.Spec` — the same committed
  subsets `check` regenerates — through `openapi.Fleet`, the same
  composition that WRITES `openapi.yaml`. So the served bytes and the artifact are
  one document by construction; `cmd/cloud/openapi_test.go` asserts exactly that
  (byte equality) and that a plugin never answers the endpoint.
- **A RELAY publishes the registry behind it, not the wildcard** (`sdk/spec/relay.go`).
  `app.All("/v1/*")` is ONE entry in this process's route table and 190 patterns in
  the registry mounted behind it, so reading the router alone published
  `/v1/{wildcard1}` and seven operations for the whole model API — no generated SDK
  and no MCP tool list carried chat completions, and hanzoai/cli grew a
  `{wildcard1}` command because that is what the document named. The endpoint's owner
  is the one that MOUNTED the thing behind it, so it is the one that can say what
  is there: same process, same objects, same instant. `spec.Front` declares
  that; `Mounted` projects a sub-app through the same `Spec` the host's own
  document is, `Table` projects a foreign registry's route table plus its prose.
  hanzoai/ai's `routers.App.Patterns` and `routers.Prose` are read out of the
  PINNED MODULE at describe time — no network, no vendored copy, no second list,
  reproducible from a checkout and a go.mod.
  - Same laws as every other client here: `Register` declares bodies and renders only
    on a live route, `Describe` declares prose and renders only on a live route, a
    relay declares the routes BEHIND a route and renders only on a live endpoint.
  - Four refusals, each naming the source so a wrong placement is traceable to the
    repo that registered it: a registry that published nothing (the shrink), a
    registry that could not describe itself (the outage), an operation outside its
    own endpoint (the routing bug), and a registry that says nothing about a route it
    serves (the same law `spec.Complete` holds an app's own operations to).
    A name collision goes through the SAME noun gate the weave uses (`nouns`,
    openapi/compose.go) — one schema name meaning two things is one law whether the
    claimants are two apps or an app and the registry behind its endpoint.
  - A endpoint YIELDS where the router says it does, in both directions. Inside a
    binary, a specific route the host registered wins over the relay's claim on
    that address. Across the fleet, `Compose` resolves exactly that one overlap and
    reads it off the data — a relayed operation names its own registry in `x-app`,
    a direct one does not. Two specific claims, or two endpoints, stay a refusal.
    `/v1` is a REMAINDER, not a namespace: ai's row is last in `manifest.Apps`, so
    `manifest.Elsewhere` answers which of its registrations the fleet delivers to a
    sibling, and those are not published.
- **`x-app` is PROVENANCE, on every operation**: the registry that registered it.
  For an app's own route it is the app name, so the code is `apps/<name>` here; for
  a relayed one it is the module behind the endpoint, so the code is that repo. Written
  once by whichever producer knows (`Project` for a relay, `Compose` for the rest)
  and never overwritten. It is the one question a reader of a wrong operation has,
  and until it was recorded the answer took a bisect of 116 subsets.
- **`openapi/floor.json` is THE RATCHET — the surface may grow and may not quietly
  shrink** (`openapi/floor.go`). Every other gate here compares the document to
  something that moved WITH it: the weave compares two derived artifacts,
  `check` regenerates them both from source. Neither holds a line across
  time, so a surface can lose products with everything green — and has: the CLI's
  capture went from 151 products to 4 in one bad reading and shipped 46 products
  short, and `plugin/ingress` lost eight paths because a subset was never
  regenerated. The floor is the counts, per product; a regeneration that comes in
  under any of them fails, names the deltas, and writes nothing. A deliberate
  deletion lowers it by hand in the same commit, where a reviewer sees the number
  go down next to the reason. A MISSING floor is refused rather than treated as
  zero — losing the file must not be a way to lose the guarantee.
- **Reading the LIVE router is the only total source.** `POST /v1/kms/auth/login`
  is registered as `Group("/v1/kms/auth").Post("/login")` — no grep can find that
  path; only the assembled router knows it. And the route set is a function of
  deployment config (`cfg.Enabled`, plus internal gates like kms's `if kc != nil`),
  so **the spec VARIES PER DEPLOYMENT** — correctly: a deployment that does not
  mount admin does not advertise it. That is why the document is generated
  per-process at request time, not built once in CI.
- **What the router CANNOT tell you — do not try to fix this in the generator.**
  Method, path, path params, and product are derivable; request/response schemas,
  query/header params, status codes, and auth are NOT. The router holds a
  `func(*zip.Ctx) error`; the request type is a LOCAL inside the handler
  (`var req secretPutRequest; json.Unmarshal(ctx.Body(), &req)`), and Go cannot
  reflect from a func value into its body. `cloud.Handle[S]` does not help — `S`
  is the SERVICE (service.go:90), not the payload. The ONE path to schemas is
  zip's typed ops (`zip.Get[In,Out]`), which carry the In/Out types and also
  yield an MCP tool and a CLI command from the same registry entry.
  `GetRoutes()` is a strict SUPERSET of `app.ops`, so migrating a handler to a
  typed op adds schema without changing this pipeline — and it needs no generator
  change, because `Fold` picks it up on the next run. **That registry is no
  longer empty**: 165 ops across 15 packages carry types today. See "The document
  pipeline" and "The typed migration" below.
  (Note: `cloud.Typed` is NOT a mount adapter — it is the per-request client that
  carries the validated org, the request and the response status across the typed
  signature, which drops all three. typed.go.)
- **`plugin/iam` WAS the extreme case, and is now the counter-example** — see
  "apps/iam — what composing recovered" below. It published 35 operations, every
  one of them a method on one of five `app.All` wildcards relaying
  `iamserver.Handler(db)`: github.com/hanzoai/iam's ENTIRE standalone zip app —
  94 typed ops of its own — adapted to net/http and hung on a wildcard. It now
  publishes **95 paths / 153 operations / 91 component schemas / 91 MCP tools**,
  because `host.Use` composes the App instead of adapting a handler.
  The opaque class has two shapes and the difference decides what a fix even
  looks like: `bot`/`exec`/`dns`/`base` proxy to ANOTHER PROCESS, where the route
  table is genuinely not in this binary and a splice is the wrong tool — those
  need the MIRROR primitive (the host reads the child's own
  `/.well-known/openapi.json` across the wire at compose time, which is not
  built). `ai`/`o11y`/`tasks`/`licensing` are in THIS process but behind a
  FOREIGN router (gorilla/mux, net/http mux) with **zero** typed zip ops between
  them, so composing has nothing to carry until those repos type their own surface —
  and the moment they do, it lands here with no change to cloud. `ai` has LEFT
  that list: it is a `*zip.App` now (`ai/app.go:73 func App`), so `Use` applies
  today and only the typing is outstanding.
- **Opaque does not always mean UNKNOWABLE, and `tasks` is the case that shows
  the difference.** Its whole product is `/v1/tasks` (a 307 to `/v1/tasks/`) plus
  one `/v1/tasks/*` relay, so it publishes 10 operations and describes none —
  but the remainder behind that relay is not off in another service. It is 64
  operations in THIS process, dispatched by path SEGMENT inside
  hanzoai/tasks' own `net/http` mux, over inputs that are anonymous structs
  local to that module's handlers. So the routes cannot be typed here (there is
  no route in cloud's router to type, and re-shaping a relayed answer is a wire
  break); they become typeable in hanzoai/tasks, which owns them.
  `apps/tasks/tasks.go` records the refusal and `apps/tasks/typed_wire_test.go`
  measures each of its four legs.

## The document pipeline: ONE registry, N projections

A typed op is ONE registry entry with N projections. `zip.Get[In,Out]` (and its
Post/Put/Patch/Delete siblings) is the single registration every consumer reads —
the REST route, the OpenAPI operation's DETAIL, the MCP tool, the CLI command and
the generated SDK method all come from that one entry. An untyped route still
gets a route and a bare operation (method, path, product — all the router knows),
and nothing else: **no schema, no prose, no MCP tool, no CLI command, no SDK
method.** Nothing here is a second source; each stage is a pure function of the
one before it.

    handler doc comments  ──zipdoc─▶  zipdoc_gen.go  ──init─▶  zip.Describe
    zip.Get[In,Out]       ──────────────▶  zip's typed-op registry (app.ops)
                          ──────────────▶  a fiber route (so GetRoutes ⊇ app.ops)

    live fiber router  ──Live─▶  []Route   ──From─▶  Document   (shape)
    typed registry     ──Typed─▶ Registry  ──Fold─▶  Document   (detail)
                                                    │
      GET /v1/openapi.json · openapi.yaml · `<app> openapi` · Compose
- **`Spec` = `From` ∘ `Live`, then `Fold` over `Typed`** (openapi/openapi.go:489).
  `GetRoutes()` is a strict superset of `app.ops` — registering a typed op
  registers a fiber route too — so the router gives the TOTAL set of operations
  and the registry gives DETAIL for the subset that has any. One document, no
  gaps and no invention. The two are not rivals and never disagree, because one
  is a subset of the other by construction.
- **A body that is not JSON is declarable too: `spec.Register(path, method,
  spec.Binary{}, resp)`.** Some routes can never be typed ops because they eat
  RAW BYTES — an uploaded receipt (`POST /v1/books/scan`), an OFX/QFX/CSV
  statement (`/v1/books/bank/import`), a KV value. zip's typed path decodes the
  body with `jsonenc.Unmarshal`, so declaring any `In` on one would turn a working
  PDF upload into a 400: the wire would MOVE, which a description task may not do.
  `Binary` is the honest declaration a Go struct cannot make (no struct describes
  a file) and renders OpenAPI's own spelling, `application/octet-stream` with
  `{type: string, format: binary}` — which is what an SDK generator turns into a
  file parameter. It is REQUEST-only: a byte RESPONSE is a second fact no route
  needs yet, and adding it before one asks is how one client becomes two.
- **A POLYMORPHIC body is declarable too: `spec.Register(path, method,
  spec.OneOf{A{}, []A{}, B{}}, resp)`.** Same shape of fact as `Binary` — the
  honest declaration a single Go struct cannot make — for a route that accepts
  SEVERAL unrelated JSON shapes on one path. `apps/event`'s canonical ingest
  wire is the case that asked for it: `decodeIngest` takes a bare `Event`, a bare
  `[Event]`, and the `{batch:[…]}` envelope, and naming one of the three would have
  published an ingest API that cannot batch — which is most of what `@hanzo/event`
  does. It renders OpenAPI's own `oneOf`, each alternative reflected exactly as a
  lone request type is (so a named struct among them is the SAME component, once).
  REQUEST-only, for the reason `Binary` is.
- **`Register` cannot carry FIELD PROSE, and that is why the fleet publishes 1,627
  bare properties.** The client derives a schema by reflection and Go drops comments,
  while zipdoc — the pass that lifts field descriptions — walks zip's TYPED
  registrations and can never reach a type that arrives this way. So every
  `Register`ed component ships shapes without descriptions: measure it with the
  snippet under failure mode #10, per plugin — admin 710, agents 209, visor 153,
  books 100, guide 90. It is the LAST thing separating a declared-but-untyped route
  from a typed one on the document surface, and it is a generator gap, not a
  diligence gap: do not "fix" it by hand-writing schemas beside the structs, which
  is the drift `Register` exists to prevent. `apps/event` names its twelve such
  components in a `proseless` ledger (typed_wire_test.go) that may only SHRINK — an
  entry that starts publishing prose goes red, so the day cloud learns to lift
  comments for `Register` the ledger empties instead of quietly outliving the gap.
- **Each app describes ITSELF: `<binary> openapi <file>`** (openapi_dump.go). An
  app's subset is generated from the app's OWN live router by the SAME
  `openapi.FleetSpec` the whole document is, over an app with only that subsystem
  mounted. It is never sliced out of the fleet spec by prefix — that would make
  the fleet the source and the app a derivative, which is backwards, and is
  exactly how a catch-all silently swallows a neighbour's routes. **Compose
  upward, never carve downward.** The mode lives on `Serve` because `Serve` is
  the single entry every app binary shares, so every one of them gets the target
  at a cost of zero per-app code. It writes a FILE, never stdout: a subsystem's own
  dependencies print to stdout at mount (hanzoai/commerce emits a sqlite-vec
  warning and GORM debug lines), which a `> file` redirect splices into the front
  of the document — 71 KB of invalid JSON. A writer whose output an unrelated
  library can corrupt is not a writer.
- **`openapi.Compose` composes the subsets, and its only contribution is the
  REFUSAL** (openapi/compose.go). Two apps may not claim one address, and two apps
  may not mean different things by one schema name. That refusal is not
  hypothetical: `apps/git`'s `/:org/:repo` catch-all was swallowing other
  apps' routes, and the manifest's call-graph walk is what caught it. The routing
  order in `Wire()` is load-bearing precisely because overlapping claims exist.
  A merge that took last-write-wins would produce a perfectly valid
  document describing a fleet nobody deploys — every generated SDK wrong, in a
  way no test could see. **A document that cannot be woven is a fleet that cannot
  be routed.** Compose does NOT arbitrate, because there is no policy for who
  should win: an overlap is a bug at the composition root, and the fix is in
  `Wire()`. `TestFleetIsTheCompositionOfItsApps` (compose_test.go:99) proves the
  composition against the golden, admitting exactly ONE class of difference —
  routes behind a PLUGIN's catch-all, which the monolith's router cannot see and
  the app's own binary can. `TestComposeRefusesTwoAppsAtOneAddress`,
  `…OneSchemaNameWithTwoShapes` and `…CollidingOperationIDs` pin the refusals.
- **The document's IDENTITY is a value, not a literal** (openapi/fleet.go). Four
  producers write documents that must compare equal, so title/version/server live
  once and all four read them; an info block that differed would make two
  documents OF THE SAME API compare unequal over a title string. `Version` is the
  API CONTRACT version — `v1` forever, house law — never the build's:
  `cloud.Version` here would make every build differ from the committed golden.
- **Both documents are GOLDENS, woven from the per-app subsets.** `make openapi`
  writes them (through the composition,
  `-compose`); `make test`, and therefore CI, verifies it with the same code and no
  flag (`TestFleetIsTheCompositionOfItsApps`, openapi/compose_test.go). Same code path both ways — there is no second
  generator to disagree with, and no way to change a route without either
  regenerating the file or turning the build red. It is serialised through JSON
  because JSON is what the document IS (the same value served at
  `/v1/openapi.json`); YAML is a rendering, and `encoding/json` orders object
  keys so the bytes are stable run to run.

### The stage: `x-stage`, and the 404 behind it

A capability is `ga`, `beta` or `alpha`, declared ONCE — `manifest.App.Stage`,
absent means ga (HIP-0139 §8). No env var, no flag, no per-app constant. The
fleet's 132 rows are **130 ga, 0 beta and 2 alpha** — `admission` and `research`,
and nothing else; `train` went ga for every org, its gate its money path (`grep -c '^\s*{Name: ' manifest/apps.go`,
`grep -n 'Stage: ' manifest/apps.go`; `manifest/stage_test.go` is the golden and
its `staged` map holds exactly those two).

The beta set is EMPTY, and getting there is the argument for holding the golden by
NAME rather than as a count. 67d0b6e1d took all 43 beta rows to ga in one commit
and left `graph` alpha; d82eec734 staged the R&D evidence plane and the
launch-control gate, which are ours rather than a customer's; ce9c29b81 published
`graph`. Each of those is a diff naming rows. Held as a count the same three
commits read 44 → 1 → 3 → 2, which says nothing about which capability a customer
can now reach.

- **The weave stamps it.** `openapi.Part` carries the app's stage beside its
  name and `Compose` writes `x-stage` onto every operation that part contributed.
  It cannot be stamped earlier: an app describing itself (`<binary> openapi`)
  projects its OWN router and never reads the fleet's manifest, so a subset does
  not carry it — which is also why `openapi` may not import `manifest`
  (`manifest/openapi_test.go` is `package manifest` and imports `openapi`, so
  the edge would close a cycle). `manifest.StageOf` is the lookup both callers
  of `Subsets` pass in.
- **`openapi/floor.json` guards the INTERNAL document and only it**, and it agrees
  with that document to the operation: floor reads 1774 paths / 2404 operations,
  which is `private.yaml` exactly, and its per-product rows carry `admission: 1`
  and `research: 8` — the same 9 staged operations, floored where they actually
  are. Nothing left that document; a staged capability is reached by flag, not
  hidden.
- **The agent endpoint followed, and it had to be MOVED to.**
  `plugin/gen-client-catalog` filters `x-tool AND x-public` — but it read
  `x-public` off the SUBSETS, where the stage is not yet known, so 355 beta
  operations stayed in the endpoint while the same operations were absent from every
  generated SDK. It now reads the audience off `openapi.yaml`, which IS the
  published contract; the roster and the prose still come from each app's own
  subset, because those are the app's own facts and the audience is the fleet's.
  42 subsystem tools and 355 operations left the catalog; no ga app moved.
  `fleet/catalog_test.go` asks the same question of the same file. With no beta
  row left, what the audience withholds from the endpoint is the two alpha rows:
  `client/catalog.json` names `admission` and `research` as keys carrying EMPTY
  operation lists, and `group` builds no tool from a name with nothing under it
  (`jq -r 'to_entries|map(select(.value|length==0)|.key)' client/catalog.json`).
- **The 404 is `cloud.Stage` (stage.go), installed by `Listen`.** One middleware
  per non-ga app — two of them today — over `manifest.PrefixesFor(name)`, asking
  flags across the internal plane: for an alpha row whether the caller's org holds
  the flag whose key IS the capability's name (`plane.FlagsHold`,
  `apps/flags/hold_rpc.go`) — one name, so there is no product→flag table to get
  wrong; for a beta row whether the platform `beta` switch is on for the caller
  (`FlagsOn`, "Beta access is ONE platform flag"). A ga row composes `nil`,
  so 123 of the 125 rows cost nothing. Not 403, which is an existence oracle; the
  body is zip's ordinary not-found, asserted byte-for-byte against an unrouted
  path. A flags outage and a caller with no validated org both fail CLOSED.
  `cmd/cloud` learns nothing and stays light — 408 packages against a bound of 450
  (`CGO_ENABLED=0 GOWORK=off go list -deps ./cmd/cloud | wc -l`, which is
  hanzo.yml's `host-is-light` step).
- **An open read asks about the org it names.** A row may open GET addresses to a
  caller with no org of its own (`manifest.App.Open`; research opens
  `/v1/research/runs`). There the refusal asks flags about the org `?org=` names,
  read from the same query map the typed op binds and matched case-insensitively as
  it matches a field, and a query naming the key twice names nobody. A caller with an
  org is asked about its own, as everywhere else, and every other address still
  answers a stranger 404. Research answers such a caller only the runs whose grant
  says `public`, and the stage's 404 until the named org has published one, so no
  anonymous read tells which orgs hold the flag (a private run is then absent from
  the listing exactly as an unrecorded one is: an empty page). Because the org is a
  caller's word, `hold` answers off for an org with no definitions store without
  creating one (`OrgStore.Has`), and research opens a named org's store the same way.
- **The refusal reads the path as the router does** (`RoutePath`: case folded,
  trailing slash dropped). It compared the raw spelling, and fiber routes
  case-insensitively, so on v8.5.349 an org without the research flag read its
  listing at `/v1/Research/runs` while `/v1/research/runs` answered it 404.
- **A staged key is a SuperAdmin's to set** (`apps/flags/staged.go`). The
  stage reads the org's own default store, which `PUT /v1/flag/defs/:key` writes,
  so a write or delete of a staged key — a manifest row with a Stage, or a
  surface (`machines`, the console's Machines screen) — takes a SuperAdmin
  (`owner == "admin"`, `cloud.Super`), never an org admin, and lands on the audit
  trail first (`flag.stage.put` / `flag.stage.delete`); no trail, no write.
  `flags.PutDef` and an experiment's `flagKey` refuse a staged key outright.
  Keys: `research` (alpha row), `machines` (alpha surface). `flags.Stage(key)`.
  Letting org `hanzo` in, as the SuperAdmin identity `admin/z` with no `hanzo/*`
  identity held (one held makes `--as hanzo` speak as it, which is refused 403):

  ```sh
  hanzo --as admin auth login && hanzo auth use admin/z
  for k in research machines; do
    hanzo --as hanzo flag defs set "$k" --data "{\"key\":\"$k\",\"active\":true,\"filters\":{\"groups\":[{\"rollout_percentage\":100}]}}"
  done
  ```

  That is `PUT https://api.hanzo.ai/v1/flag/defs/<key>` with the admin bearer and
  `X-Org-Id: hanzo`. Revoke with `hanzo --as hanzo flag defs rm <key>`.

### The fold: an address moves under the app that answers it

`openapi/misfiled.txt` is the ratchet (HIP-0139 §3, §5.1) and a line closes one
of three ways — fold, split, rename — with the STORE deciding which. Four
capabilities folded here, 100 lines → 92:

| was | now | why not a split |
|---|---|---|
| `/v1/coding` | `/v1/agents/coding` | the engine needs agents' session store, durable engine and mailbox; a coding run IS an agent run |
| `/v1/upload` `/v1/download` `/v1/files` | `/v1/exec/*` | exec holds no store — a session IS a sandbox, and a session's files are the session's |
| `/v1/skills` `/v1/plugins` `/v1/mcp/servers` | `/v1/tools/*` | each is the SAME registry narrowed; moving one to its own app would put two apps on one store |
| `/v1/code/lsp` | `/v1/lsp` | lsp owns no store either, so nothing is shared with code; §2.5 makes `lsp` a word, so it folds under its OWN name and the two stay cross-referenced siblings |

Two facts a fold has to carry beyond the router:

- **`rank` reads the PATH, so a moved op takes a new bucket.** `fleet/surface.go`
  `productStems` is a list of PRODUCTS, and an op whose first segment matches none
  of them falls to the tail — where `subsystemTool` skips its prose and a
  truncating client drops it first. The coding run once sat there for want of a
  stem; moving it under `agents` retired the `coding` stem, and moving lsp OUT of
  `/v1/code` — where it had been ranking on code's stem — needed one of its own.
- **A base-relative client pays a base, not a wire.** The LibreChat code
  interpreter composes `/upload`, `/download/{sid}/{id}` and `/files/{sid}` off a
  configured base URL, so the exec fold is one config change deployed in lockstep:
  point `LIBRECHAT_CODE_BASEURL` at `.../v1/exec`. Nothing below the base moves.
- **`make -C apps/<app> describe` does NOT regenerate `plugin/<app>/zipdoc_gen.go`.**
  `mk/plugin.mk`'s `generate` runs zipdoc over `$(APPDIR)/...`, which is
  `apps/<app>` and nothing else — so an op declared at the COMPOSITION ROOT
  (`plugin/agents/coding.go` is the one) keeps prose filed under its old path and
  publishes none under the new one, silently, because `spec.Complete` accepts a
  summary alone. Only the Dockerfile's repo-wide `go generate -run zipdoc ./...`
  catches it. Moving such an op means
  `GOOS= GOARCH= go generate -run zipdoc ./plugin/<app>/...` and THEN `describe`.
  The tell is a tool the endpoint lists with an empty description.
- **The tool NAME moves with the address.** `create_coding` is
  `create_agent_coding` now: the endpoint renames a derived operation id to the verb
  phrase the path already contains (`fleet/verbs.go`), so a fold renames the tool a
  model calls as surely as it renames the URL. `plugin/agents/coding_test.go` pins
  that string, which is the only gate that can see it — a router test cannot.

The upstream release was the whole cost. Nothing in the round reads the prefix,
so no handler, no store and no client changed — which is the test to run before
believing any other "upstream must move first" line here: ask whether the thing
that must move is a VALUE the caller could supply.

### The MCP door signs people in (client/mcp.go, openapi/agent.go)

An MCP client (Claude.ai, ChatGPT, Claude Code, Codex, Cursor, VS Code, Gemini CLI, Amp,
Windsurf, Warp) adds `https://api.hanzo.ai/v1/mcp` and either signs in with Hanzo (OAuth) or
sends `Authorization: Bearer <Hanzo API key>`. Both reach the same principal: the bearer rides
to the owning plugin, whose identity boundary resolves a JWT or an API key to one identity.

- **The resource is the URL called** (RFC 9728 §3.3): `/.well-known/oauth-protected-resource`
  names the API origin, `.../v1/mcp` names `<origin>/v1/mcp` (`mcp.MetadataMCP`).
  `authorization_servers` is the brand's issuer; its `registration_endpoint` is IAM's
  (`IAM_REGISTRATION`, hanzoai/iam LLM.md "Dynamic client registration").
- **`aud` is still not a gate** (`TestValidate_AudienceIsNotAGate`): trust is signature + issuer +
  expiry. MCP's audience binding (RFC 8707) is therefore advisory here; IAM sets `aud` to the
  registered client.
- A plain MEMBER of an org the platform runs (`hanzo`) resolves org-less (`defaultOrg`), so its
  tools answer `an org scope is required`; people in their own orgs, org admins and keys are
  unaffected. Not new, and the first thing a local e2e with a `hanzo/<user>` meets.

### zipdoc needs a router it can RESOLVE, and the gate accepts the gap

`zipdoc` resolves a typed op's path STATICALLY. Register on the `cloud.Router`
parameter and it cannot follow the interface to a prefix, so it refuses to lift —
and the refusal is silent in every gate downstream, because `spec.Complete`
passes an operation carrying EITHER a summary or a description. `zip.WithSummary`
alone is therefore enough to be green and empty.

`POST /v1/exec` shipped exactly that way: a real doc comment on the handler, no
`//go:generate` directive in the package, no `zipdoc_gen.go`, and an operation
that reached the document, the SDKs and the tool list with a summary and no
prose. The fix is two lines — the directive, and `reg := cloud.ZipApp(app)` so the
registration is spelled where the generator can read it (the pattern `apps/meet`
and `apps/blueprint` already use). ROUTES move to the `*zip.App`; middleware stays
on the scoped router, where the prefix guard applies to it.

**The chain is countable end to end**: 51 operations carry no description →
51 of the 2448 commands at `/v1/commands` have an empty `Description` (49 `ai`,
2 `router`, all owned by `apps/ai`). One hole, three surfaces.

### The field surface is a different fact, counted in a different place

One tranche closed here (343 properties): `authors` 39→0, `label` 37→0,
`channels` 34→0, `prompts` 29→0, `leaderboard` 40→0, `campaign` 42→6,
`affiliates` 123→10, `exec` 15→0.

- **A field reached through an EMBEDDED struct cannot be described today, and it is
  a zip defect, not an app one.** zipdoc files a field's prose under the type that
  DECLARES it; zip's schema builder inlines the embedding and looks the prose up
  under the type that PROMOTES it (`zip@v1.27.0/openapi.go:701` keys
  `fields[t.Name()+"."+name]`). The two never meet. Found twice, independently:
  `envelope.msg` vs `directoryOut.msg` — every enveloped `*Out` in the fleet
  publishes its `msg` and `status` bare, **167 properties, 3% of the backlog** —
  and `campaignWrite.audience` vs `campaignUpdate.audience`, where
  `internal/zipdoc/extract.go:634` additionally skips the embedded field outright
  because `campaignWrite` is unexported. Write the comment on the embedded struct
  anyway; do NOT unroll it into hand-copied field pairs, which would duplicate the
  shape the type exists to share AND break the embedding that keeps create and
  update in step. The fix is one change in zip: `structFields` must recurse into an
  embedded struct and re-emit under the OUTER type's name.
- **A defined type over another struct publishes NOTHING.** `type campaignRecord
  Campaign` has no struct literal of its own, so zipdoc emitted nothing for it and
  all 30 of its properties published bare. Declare the struct under its published
  name and make the domain name an ALIAS (`type Campaign = campaignRecord`) — same
  type, one shape, and the prose lands.

### There is no hand-authored copy of the surface left

`docs/automations-openapi.yaml` was a 25 KB hand-written OpenAPI document
describing 17 operations of `/v1/automations`. Nothing referenced it and no gate
compared it, so it had drifted exactly as a second copy always does: it claimed
two operations the fleet does not serve (`GET /v1/automations/health`,
`POST /v1/automations/mcp`) and omitted three it does (`POST
/v1/automations/connectors/{id}/run`, `/flows/{id}/versions`,
`/hooks/{source}/{event}`). Deleted. The reference pages at docs.hanzo.ai are
generated per product from this document; nothing else here describes an endpoint.

### GraphQL: two graphs, one engine, and it is zip's

zip's engine (zap-proto/zip `document.go` + `resolve.go`, LLM.md "GraphQL: one
engine") is the only GraphQL parser and executor here. cloud owns two SCHEMAS and
the answer to a root field of each; parsing, fragments, variables, directives,
bounds (64 root fields, depth 64, 10000 fields selected and 10000 resolutions
spent, refused whole with a 400; at most 8 root fields at once), the projection of
every answer through its selection, and what a caller is told of a failure (the
message of a 4xx `zip.HTTPError`, or `internal error (request <X-Request-Id>)` with
the detail in the log) are zip's.

- **`/v1/graphql` is the host's** (`cmd/cloud` `graphql()` → `client.UseGraph`,
  `client/graph.go`). Schema: `openapi.GraphQL` over `openapi.Fleet(subsets)` — the
  same composed document `/v1/openapi.json` serves; dispatch: `openapi.Fields` over
  that one document, built together once (`sync.OnceValues`), so a field a caller
  can read is a field a caller can send. `client.Resolve` answers a root field as
  ONE HOP to the owning app: the field's kind must match the op (GET is a query,
  everything else a mutation — a write selected under a query is refused and
  dispatches nothing), path and query parameters come from the arguments, `body`
  is the JSON body, and the caller's request rides along (`relayed`): every header
  of it (`zip.Headers`) LESS every name ingress strips (`sdk/principal.Stripped`,
  the one list `SanitizeIdentity` strips by too), PLUS the identity zip resolves for the caller (`zip.Identity`, the
  rule `zip.Call` uses) — so the child authenticates and scopes the caller exactly
  as over REST, and a header the edge did not mint cannot ride the hop whatever
  the middleware order. A co-resident app is served in memory. A child's 4xx is
  told as `the operation answered 403 Forbidden`; its 5xx and a failed dial (which
  name the app, its socket and the address asked) go to the log only.
- **`/v1/graph/graphql` is the knowledge graph's** (`apps/graph/graphql.go`),
  mounted on the `/v1/graph` group (`zip.Group.MountGraphFor`) so the group's
  middleware stands in front of it. The SDL is written by hand. Query and Entity
  are `table`s of fields, each row an op call asked only when selected; Entity is a
  `zip.Object`, so `edges` answer more Entities and nesting is traversal. The four
  value types (assertion, resolution, vocabulary, declaration) are Go structs zip
  projects against their JSON names, which costs the request's budget nothing.
  `edges` is the only recursion, so its cost is bounded where it is spent: nesting
  past three (`hopsMost`) is refused before any op runs, and one root field's edges
  answer at most 1000 entities (`entityMost`) before the next walk is refused.
  Before these bounds, red's 13 fully connected hubs nested four deep made 24,678
  entities from ~2,056 walks in 3.6 s; `TestATraversalCannotFanOutWithoutEnd` is
  that request. GET at the same address serves the SDL; introspection is not
  served. `graphql_test.go` holds the SDL to the tables and the struct fields both
  ways and runs `zip.CheckSchema`.
- **`/v1/base/graphql` is the org's Base** (`apps/base/graph.go`, Base's `graph`
  package): the schema of that org's own collections, every field a request on
  the org Base's router as the caller (`graph.New(app, mux)`, the mux `handler`
  compiles), pinned for the request. `/v1/graphql` composes it as `base`
  (`client/graph_base.go`): the Base's SDL fetched with the caller's request and
  hung under Query and Mutation, and a `base { … }` subtree printed back into one
  document and sent there, its type names and errors answered at their paths. A
  caller with no org Base gets the fleet schema alone.
- A plugin child's own `/v1/graphql` (`serve.go`, `app.MountGraph`) is zip's
  projection of that child's registry; in the fleet the host's address shadows it.

## Generated and frozen artifacts: what goes stale, and how you find out

- **The wire freeze test must be updated in the same diff as `Wire()`.** It is a
  golden, not an invariant. A new subsystem lands RED until `frozen` names it.
  That is the design — the failure is the review prompt — but do not "fix" it by
  loosening the test.
- **DELETING AN APP ROTS THREE HAND-KEPT LEDGERS, and each one stops the release
  train.** They are keyed by app or by file, so removing the package leaves an
  entry describing nothing, and all three are `go-unit`:

      allowedRequestUses      typed_request_gate_test.go   (by FILE)
      allowedTokenPrimitives  token_gate_test.go           (by FILE)
      meteredApps             spend.go                     (by APP)

      Go doc comment -> zipdoc -> the app's own binary -> openapi.json -> catalogue

  What it replaced is the argument for it. `hanzoai/openapi`'s `skills.py` read
  THAT repo's per-service specs, so the catalogue was a function of a checkout
  this one does not pin: measured, a run against a branched checkout emitted 357
  skills per brand against the 542 committed, arriving as a routine
  "regenerate". It had drifted in both directions — advertising `balancers` and
  `builds`, which production 404s, while missing products this fleet serves — and
  it kept a SECOND COPY of the brand table, which had drifted to `zoo.id` as the
  Zoo OIDC issuer. That host does not resolve. Brands now come from `cloud/brand`,
  the registry the serving code already reads, so the catalogue cannot disagree
  with the deployment about who issues its tokens.
  **Keyed by PRODUCT, not by app** — `spec.Product`, imported rather than
  re-derived, because that is the axis the tags, the CLI tree and every SDK
  already use: `apps/plan` serves `/v1/plans`, `apps/visor` serves the compute
  plane, and billing and treasury share `/v1/finance`. A customer calls the
  product. Coverage is the check that matters and it is total: all **127**
  products carrying a GET have at least one skill.
  **Uncapped, deliberately.** A per-product cap of 10 was tried first and dropped
  live surfaces — `/v1/ai/chats` answers 200 in production and lost its slot to
  higher-ranked clusters of the same product. The old per-SERVICE cap hid that,
  because the other repo's finer service split gave each its own budget.

## An agent is one hop from its brand's manifest (`brand.Agent`)

One fact in the brand registry, `Info.Agent` (hanzo: hanzoskills.com/hanzo.md,
lux: lux.network/llms.txt, zoo: zoo.ngo/llms.txt; pars and bootnode none), read by
`brand.Agent(host)`, which has NO default: a host no brand's Domain/AltDomain holds,
a customer's site in a Published namespace and a brand with no manifest get nothing.
Three surfaces name it, each from that one function:

- every /v1 answer: `Link: <manifest>; rel="describedby"; type="text/markdown"`
  beside the OpenAPI `describedby` (`openapi/index.go` `refer`);
- every refusal an agent clears by itself (`errmap.go` `guided`): a keyless 401
  (code `unauthorized` only, never a step-up), every 402 and 429 carry `agent`;
  where money clears it (`insufficient_balance`, `allowance_spent`,
  `payment_required`, and every zen plan refusal) also `topup`, the relative
  `cloud.TopupPath` (`/v1/billing/crypto/deposit`, an address of the caller's own
  that is credited once the transfer is deep enough).
  Same names on the problem body
  and the money wire's nested `error`;
- `GET|HEAD /llms.txt` and `/skill.md` on the host (`openapi/agent.go`, composed by
  `cmd/cloud/agent.go`), after the site edge and before the console. Endpoint NAMES only, derived from the
  brand (`IAMIssuer`, `APIHost`, `Console`); the wallet steps only where
  `Info.Wallet`. `cmd/cloud/agent_test.go` checks every step against
  `openapi.yaml`, so a renamed route fails the gate, not an agent.

Static sites reach the same page through the edge: universe's registry
(`infra/sites`, `agent:` zones) routes a site's `/llms.txt` to cloud with Host
rewritten to its brand's API host. The door reads that literal Host header, never
`c.Host()`: the host trusts the ingress, so `c.Host()` is X-Forwarded-Host, the
site's own name, which no brand Domain holds (`TestTheDoorReadsTheHostTheEdgeWrote`).

## The agent's door (`openapi/agent.go`): what a crawler reads before it holds a key

One table, `pages`, is the door. Each row is a TYPED operation of the host's own
capability (`openapi.Self`): `declare` registers it on `core()`'s router, so
private.yaml carries it (x-app openapi, x-tool, `security: []`, its media), and
`UseAgent` answers the same row on the edge by calling the same writer. Rows and
scope (`admits`):

| row | scope | answer |
|---|---|---|
| `/` | API host | HTML; Markdown for `Accept: text/markdown`; JSON = `GET /v1` byte for byte. `Link` = the contract string (api-catalog, describedby llms, service-desc, service-doc), `Vary: Accept`, `no-store` |
| `/robots.txt` | API host | ONE group: `*` + `openapi.Crawlers`, `Content-Signal: search=yes, ai-input=yes, ai-train=yes`, `Allow: /`, `Disallow: /v1/`, `Disallow:` each manifest prefix outside /v1 and /.well-known (`/login/oauth`), `Allow: <p>$` for every sitemap address under /v1/, then `Sitemap:` |
| `/sitemap.xml` | API host | `crawlable(doc)`: every parameterless GET owned by Self or under /.well-known (ga only) + every capability index Discover answers |
| `/auth.md` | API host | bearer header, wallet steps (`Info.Wallet`), device flow (`Info.Login`), console keys, OIDC, scopes, discovery; recipe = `Info.Agent` |
| `/.well-known/api-catalog` | API host | RFC 9727 linkset (`application/linkset+json; profile=…rfc9727`): API, MCP, `Info.Guide` |
| `/.well-known/mcp/server-card.json` | API host | SEP-2127 fields + the SEP-1649 draft fields scanners read; `application/mcp-server-card+json` when asked |
| `/.well-known/agent-card.json` (+alias `/.well-known/agent.json`) | API host + first-party sites | A2A 1.0 card: the default assistant, one `JSONRPC` interface at `https://api.<domain>/v1/agent/a2a`, no streaming/push/extended card, OIDC (`authority()`'s issuer) or API-key Bearer, one `assistant` skill |
| `/llms.txt`, `/skill.md` | any brand host | unchanged |
| `/.well-known/oauth-protected-resource` (+`/v1/mcp`) | every host | RFC 9728, + `resource_name`, `resource_documentation` (deployment brand) |

- **Why middleware, not routes.** The answer turns on the Host, and the console
  owns `/` and its own robots.txt on console hosts; a route at `/` would take them on
  every host. The door sits AFTER the mounts, so `openapi.Routed` counts its rows: a
  manifest row claiming one would shadow it (`TestNoAppClaimsAHostEndpoint`).
- **One reading.** `openapi.Read(subsets)` is the index's once-render; `UseIndex`
  and `UseAgent` share it, so `/v1`, the JSON home, the sitemap and robots are one
  compose. `rendered.pages` is the crawlable set.
- **Every address answers.** `cmd/cloud` `TestEveryAddressTheDoorNamesAnswers`
  GETs every sitemap URL, catalog href and home link on the composed host. robots
  allows exactly the sitemap under /v1/ so an obeying crawler never meets a 401.
- **Brand facts** are registry fields (`Guide`, `Docs`, `Reference`, `Status`,
  `Login`), set for hanzo only; a brand without one omits the line. status.zoo.ngo
  does not resolve, so status is not derived from the apex.
- **The card carries no tool count**: SEP-2127 excludes primitives, and counting
  means `tools/list`, which fans out (never-probe rule).
- **A2A (`apps/agents/a2a.go`, hanzo-inc/cloud#422).** `POST /v1/agent/a2a`, A2A 1.0
  JSON-RPC, answered as the default assistant (`builtinAgent`, or the org's own
  `hanzo` row, exactly as the chat bridges resolve it). A task IS an agent run:
  `runAgent` with the caller's `principal.Payer`, so the balance gate and the flat
  run fee (`CLOUD_AGENT_FEE_CENTS`) are the run's; a refusal before the task exists
  is JSON-RPC -32040 with the fleet code as `ErrorInfo.reason`. `a2a_tasks` (per-org
  file) holds what a run does not: state while working, `context_id`, the caller's
  `messageId`, cancel. The run is DETACHED from the request (`WithoutCancel`, bounded
  by `toolRunBudget`+1m) because the edge closes long requests: a waiting caller gets
  the finished task, `returnImmediately` gets it working, and GetTask/ListTasks read
  it later. A task is its person's (`billingActor`), like a run. Continuing a
  conversation = new message with the `contextId` (the last 20 completed turns ride
  as history); a message naming a `taskId` is -32004. v0.3 slash methods and any
  `A2A-Version` but `1.0` are -32009. No credential = HTTP 401 + the MCP challenge
  header; every other outcome is a 200 JSON-RPC body. The card and the server read
  `manifest/a2a`, a leaf like `manifest/mcp`.
- **Not served, on purpose.** No Web Bot Auth directory: crawl, websearch and world fetch
  with a Firefox User-Agent by design (datacenter IPs get challenge pages), so cloud
  signs nothing as a Hanzo bot. The gap, sized: an owner decision to fetch as a named
  bot, an Ed25519 key in KMS, an RFC 9421 signer with `Signature-Agent` in
  `apps/crawl` fetch (~200 lines; the headless-Chromium path in hanzoai/crawl signs
  nothing and would need its own), and a signed
  `/.well-known/http-message-signatures-directory` row here.

## MCP revisions (`manifest/mcp`)
