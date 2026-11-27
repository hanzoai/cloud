# LLM.md — apps/websearch

Guidance for apps/websearch. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Metasearch: an engine that says nothing is either empty or BLIND

The browser service is REAL and the code said it was not. `apps/crawl` opened
with "that service was named in config but did not exist —
crawl.hanzo.svc.cluster.local was NXDOMAIN", and browser.go promised escalation
was best-effort "while the browser is not deployed, which is the state this ships
in". Measured: `svc/crawl` on :11235, pod Running, `/health` 200,
image `ghcr.io/hanzoai/crawl:sha-7b8dc59`. It has been up for days. It needs the
bearer token that reaches both pods from KMS as `crawl-secrets/CRAWL_API_TOKEN` —
unauthenticated it answers `{"detail":"Authentication required"}`, so a missing
token is not a degraded render, it is no render.

**Zero is blind per engine, always, because no engine can be trusted to report
its own emptiness.** Bing has no zero state at all: asked three distinct nonsense
strings it returned ten results each time — Edmonton property tax, Bastille Day,
Microsoft support — and for a fourth, pornography. DDG and Mojeek do return zero,
but also return zero when serving a captcha, which is the case worth catching.
The genuine-empty case is recovered where its evidence actually lives, ACROSS
engines: blind while a sibling answered the same query is proof the query has
results and that engine cannot see them.

Two instruments, deliberately different widths. The counter
`hanzo_websearch_engine_total{engine,outcome}` records every turn (nine series,
bounded by construction — the query is never an attribute); an operator reads the
RATIO. The Warn line fires only on the confirmed fault, so it stays worth
reading. `browsed` rides along because it decides who is woken: false is
configuration, true means a real browser drew the page and our parser still read
nothing — selector rot, the loudest signal this package has.

**DDG's challenge is an HTTP 202, and that one fact cost us the engine.** The
fetch accepted only 200, so the challenge became a transport error, the transport
error short-circuited past the escalation, and the engine the browser was
deployed to rescue was the one that could never reach it — `ddg=failed` on every
live query. Any 2xx is now parsed, and there is ONE remedy for "nothing readable
came back" whichever way it happened: render it. Live, from cluster egress, that
turned `[bing=answered(10) ddg=failed(0) mojeek=answered(20)]` into all three
answering, and pushed `post.ca.gov/Training` off the top of "post quantum
cryptography lattice" (now redhat's lattice-based-cryptography, then Wikipedia).

**GOOGLE IS NOT VIABLE FROM THIS NETWORK. Do not add it.** Every path returns the
`/sorry/` interstitial — ~6KB, 19 captcha markers, "unusual traffic", zero
results: headless Crawl with stealth on, `&udm=14`, `&gbv=1`, and a HEADFUL
Chrome 150 on a real X display (`bot-browser`) from a second egress IP. The
control rules out the technique — that same headful browser reads DDG's ten
results and loads google.com's homepage normally (268KB, title "Google", no
captcha). Only `/search` is refused, from two different node IPs, headless and
headful alike. That is reputation attached to datacenter addresses, so no browser
flag reaches it; the fix would be residential egress, which is a different
decision than a parser.

**crawl, not bot-browser, is the instrument.** They measured IDENTICALLY on both
engines, so bot-browser buys nothing on capability and costs concurrency (one
shared headful Chrome behind a single CDP endpoint), tenancy (one cookie jar for
every caller) and a websocket client cloud does not have. bot-browser is an
interactive instrument — VNC 5900 / noVNC 6080 exist so a person can watch a bot
session — and driving search through it would serialize every query through one
browser.

### The engine setting is SuperAdmin's, at `/v1/websearch/engines`

Which engines run, their order and their options is ONE document: the settings
store's row for the platform org (`admin`), product `websearch` — the same row
every platform knob uses (`apps/settings/fleet_rpc.go`). websearch reads it over
the plane (`settings_fleet`) per search and writes it through `settings_fleet_set`
(SuperAdmin stated caller, audited `settings.fleet.set` with before/after).
`WEBSEARCH_ENGINES`, `WEBSEARCH_MOJEEK_KEY` and `WEBSEARCH_BRAVE_KEY` are read by
nothing: **universe must drop `WEBSEARCH_ENGINES` and `WEBSEARCH_MOJEEK_KEY` (its
`chat-secrets` secretKeyRef) from `charts/app/values/hanzo/cloud.yaml`**.

Keys are KMS, read in process per search: org `admin`, env `prod`, path
`/websearch`, names `MOJEEK_KEY`, `BRAVE_KEY` (ref
`orgs/admin/websearch/MOJEEK_KEY@prod`, custodied by websearch in
`apps/kms/custody.go`). Org `admin` because that is where a SuperAdmin's own
`hanzo kms` writes land. The read runs on `cloud.For(context.Background(),
"admin")`: the request's org would win over a stated one and the store refuses
it (`TestAdmitKeepsTheSearchKeysForThePlatform`).

| Op | Path | Body → answer |
|---|---|---|
| `list_search_engines` | `GET /v1/websearch/engines` | → `{saved, engines:[{name, enabled, order, acceptsKey, needsKey, keyed, available, options:{count,language,region}, supports:[{name,param,min,max,default}]}]}` |
| `set_search_engines` | `PUT /v1/websearch/engines` | `{enabled:[…], options:{<engine>:{count,language,region}}}` → the GET body |
| `set_search_engine_key` | `PUT /v1/websearch/engines/{name}/key` | `{key}` → `{name, keyed:true}` |
| `delete_search_engine_key` | `DELETE /v1/websearch/engines/{name}/key` | → `{name, keyed:false}` |

All four: `cloud.Super` (owner == admin; an org admin is 403), writes also
`cloud.CSRF`. The two key ops are withheld from MCP by `client/surface.go`.

- **Default with nothing saved:** mojeek alone when `MOJEEK_KEY` is held, else
  `bing, ddg, mojeek`.
- **Never an empty lineup:** PUT refuses (400) an enabled set with no available
  engine; DELETE key refuses (409) the one that would leave the saved set so; at
  search time a lineup with none (KMS down) runs the keyless one, and so does a
  caller who cannot pay for the keyed seats (`meter.go` `free`).
- A stored document only a bypass could write (unknown engine, bad option) is read
  leniently and warned once; GET shows what runs.
- Tests stand settings and kms up as real plane peers (`peers_test.go`); no test
  reaches a real engine.
