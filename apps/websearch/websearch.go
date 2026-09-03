// Package websearch is a web search and a page fetch your agents can call.
//
// It exposes Hanzo-native Web Search + Scrape on the unified cloud-api /v1
// plane, so hanzo.chat's web_search agent tool runs entirely on Hanzo
// infrastructure with NO external SaaS provider, per HIP-0106.
//
// hanzo.chat (LibreChat fork) implements web_search as a fixed 3-stage pipeline
// whose provider contracts are frozen by the upstream client
// (@librechat/agents tools/search). The only self-hostable, key-less-to-a-SaaS
// providers it accepts are:
//   - search provider  "searxng"   → GET  {searxngInstanceUrl}/search?q=&format=json
//     ← {results:[{url,title,content,img_src?}]}
//   - scraper provider "firecrawl" → POST {firecrawlApiUrl}/{version}/scrape
//     body {url,formats} ← {success,data:{markdown,metadata}}
//     (reranker is optional; we omit it — provider+scraper is sufficient.)
//
// This subsystem serves BOTH contracts, backed by Hanzo's
// own services — never a third-party search API:
//   - GET  /v1/websearch/search        SearXNG-shaped. Served NATIVELY in-process
//     by a keyless Go meta-search (search.go) — no SearXNG pod, no search SaaS.
//   - POST /v1/websearch/scrape        Firecrawl-shaped. Served NATIVELY in-process
//     by clients/crawl — fetch, extract, render —
//     returning {success,data:{markdown,metadata}}.
//
// Both halves are now in-process Go, for the same reason and by the same shape: a
// keyless meta-search here, a fetch-and-extract in clients/crawl. Neither has a pod
// to be down. Scrape previously dialled a separate crawler at crawl.hanzo.svc that
// did NOT exist — the name was NXDOMAIN — so this surface answered 200 while every
// scrape inside it returned success:false. clients/crawl is the same capability
// with no network hop and no second deployment to keep alive.
//
// The chat server calls these SERVER-SIDE in-cluster, so point
// searxngInstanceUrl at this surface (public api.hanzo.ai/v1 or the internal
// cloud-api svc DNS — same binary either way). The fetch is a HANZO address
// now, not a firecrawl one: a firecrawl client composes
// {apiUrl}/{version}/scrape, which cannot spell /v1/websearch/scrape from any
// base URL it accepts, so the chat server's scraper is pointed at the Hanzo
// address directly. The envelope is what stayed compatible; the path carries
// the name of the capability behind it (HIP-0139 §3, whose §3.2 exemptions are
// a closed list firecrawl is not on).
//
// AUTH: one gate on both surfaces, never an open proxy — a validated principal
// (principal.Validated — X-User-Id minted by the identity middleware from a
// verified JWT). A caller without one is refused.
//
// SEARCH used to admit a second way, and SCRAPE used to require it instead: a
// shared service key, WEBSEARCH_API_KEY, on X-API-Key or a Bearer, for the
// hanzo.chat server reaching cloud with no user principal. Two gates had to agree
// about who a caller was while only one of them had ever seen an identity, and
// keeping our own credential meant distributing, rotating and eventually leaking
// it. A service that needs these surfaces presents a service identity.
//
// A caller with no validated principal is 401; a request
// with a validated principal never needs the key. So neither surface is ever an
// open proxy, and the signed-in console user reaches search without the shared key.
//
// THE NATIVE ENDPOINT IS A TYPED OP; THE TWO COMPAT ENDPOINTS CANNOT BE.
//
//   - POST /v1/websearch is Hanzo's own address for this capability, and it is a
//     typed op — so it is an MCP tool, a CLI command, an SDK method and a
//     described operation, which is what the assistant reaches for when it is
//     asked what the weather is. It runs the SAME metaSearch over the SAME
//     engines and answers the SAME envelope as the SearXNG endpoint; there is one
//     search here, offered at the address each caller can actually speak.
//
// WHY THE OTHER TWO ARE NOT TYPED OPS (re-verified at zip v1.27.0), so the next
// sweep does not re-litigate it. Both exist to be BYTE-COMPATIBLE with a client
// this repo does not own — LibreChat's frozen searxng and firecrawl contracts —
// and each is compatible in a way a typed op structurally cannot be:
//
//   - /v1/websearch/search is registered with All (Mount, below), so it answers
//     every method in the router's set — today delete, get, options, patch, post,
//     put and trace. zip has no typed `All`, and declaring the five named verbs
//     instead would DROP options and trace from the path: a routing change, not a
//     description. The POST/PUT/PATCH arms also read their query string and IGNORE
//     the body entirely, while a typed op 400s on any unparseable non-empty body
//     (typed.go op.invoke) — so those arms cannot be typed even one at a time.
//   - /v1/websearch/scrape deliberately answers 200 {"success":false,"error":"missing url"} to
//     a malformed or oversized body (scrapeScoped, below): firecrawl clients read
//     data.success, not the status line, and it caps the read at 1 MiB — a bound on
//     the body it was handed — rather than refusing. A typed op cannot express
//     either: the 400 is raised before the handler runs, and the cap is invisible
//     to it.
//
// The route that unblocks the first is a typed `All` in zip; the second needs a
// body-TOLERANT op. Neither is a reason to leave the CAPABILITY unreachable, which
// is what the previous version of this note concluded: it read the two adapters'
// wire constraints as a property of web search itself, and so this subsystem
// served the fleet's only path to the live internet while projecting no tool at
// all. An adapter's frozen contract binds the adapter. Their PROSE is declared
// through openapi.Describe beside the route table (Mount, below), which is the
// client for exactly the operations the wire refuses to type.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/crawl"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// ── SearXNG-shaped search: native keyless meta-search, in-process ────────────
// search.go's metaSearch runs the enabled keyless engines and returns the exact
// SearXNG /search?format=json envelope, so the LibreChat searxng client decodes
// it verbatim — no SearXNG pod, no third-party search API. This replaces the
// retired reverse proxy. Reads the SearXNG query params (q, language).
func searchNative(c *zip.Ctx) error {
	q := strings.TrimSpace(c.Query("q"))
	lang := strings.TrimSpace(c.Query("language"))
	return writeJSON(c, http.StatusOK, metaSearch(c.Context(), q, lang))
}

// ── The native endpoint: POST /v1/websearch, a typed op ─────────────────────

// Go drops comments at compile time, so cmd/zipdoc is the ONLY path from the
// handler's prose to the published document, the SDKs and the MCP tool
// description. Its output is committed; `make zipdoc-check` fails on drift.
//
// Without this directive the package builds, the tests pass, and the typed op
// below publishes a summary with no description — openapi.Complete accepts
// either, so the gap is invisible to every gate and visible in every SDK. A
// search tool a model cannot read is a search tool it will not reach for.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Path is the address the native search answers at, spelled once so the
// registration and the prose cannot disagree.
const Path = "/v1/websearch"

// webSearchQuery is the POST /v1/websearch body. It carries the SAME two inputs
// the SearXNG endpoint reads off its query string, so the two endpoints are one
// search asked two ways rather than two searches.
type webSearchQuery struct {
	// Q is the query. Required — an empty one is refused rather than answered
	// with the whole web.
	Q string `json:"q"`
	// Language narrows the engines to a locale, BCP-47-ish ("en", "ja", "de").
	// Empty means no narrowing.
	Language string `json:"language,omitempty"`
}

// webSearch searches the live web and answers with ranked results.
//
// This is the fleet's path to what is happening RIGHT NOW — today's weather, an
// outage, a release that postdates any model's training. `q` is the query and
// `language` narrows it to a locale. The answer is `{query, number_of_results,
// results:[{url, title, content, engine}]}`, where `content` is the ENGINE's
// snippet and not the page: read a page with POST /v1/crawl.
//
// It is served in-process by a Go meta-search over keyless public engines — never
// a third-party search API and never a search key. The enabled engines run
// concurrently and their hits are merged, deduplicated by normalised URL (host
// and path, trailing slash and fragment dropped, query kept, so distinct queries
// stay distinct results) and capped at 30. Ranking is deterministic rather than
// scored: the first configured engine's hits lead.
//
// It fails SOFT on the engines. One that errors, times out or is served a
// bot-challenge page contributes zero results and never fails the call, so an
// empty `results` is a real answer — nothing was found — and not an outage. The
// array is always present, never null.
//
// Two refusals in the order they have to be asked, both in the PREAMBLE. A typed
// op is also an MCP tool, a call-plane operation, a graph field and a CLI
// command, and every one of those invokes it with no route and therefore no
// middleware — so what admits a caller here is asked where every caller reaches
// it rather than in a middleware only one of them passes through.
//
// A VALIDATED PRINCIPAL IS REQUIRED, and there is no tenant beyond that: the
// results are public web pages, identical for every caller, so nothing here is
// scoped and nothing here can leak across orgs.
//
// THEN THE ANTI-FORGERY TOKEN, immediately before the money, because that is
// what it is about. This search is the SAME bought meta-search the compat
// endpoint runs — the engines cost, and account.Shared/meter.go bills the
// caller's ledger for the answer — so a page the caller never visited must not
// be able to spend for them by sending their browser here with a cookie they
// already hold. Nothing leaks; the answer is unreadable cross-origin. What
// moves is money.
//
// It is account's control, the one every operation in this estate asks, and it
// is a no-op the moment a caller PRESENTS a credential (Bearer, gateway, API
// key) — which is every service and console caller here — so it costs a CLI, an
// agent and an API client nothing. Only the ambient-cookie path is asked for the
// echoed token. The raw /v1/websearch/search route asks the same control on its
// group (see Mount), so the two addresses of one search are admitted alike.
func webSearch(ctx context.Context, in *webSearchQuery) (*webSearchResults, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrUnauthorized("sign in to search the web")
	}
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		return nil, zip.ErrBadRequest("q required")
	}
	out := metaSearch(ctx, q, strings.TrimSpace(in.Language))
	return &out, nil
}

// ── Firecrawl scrape: adapt Hanzo Crawl → the firecrawl response shape ──────

// firecrawlRequest is the subset of the firecrawl /scrape body we honor.
type firecrawlRequest struct {
	URL string `json:"url"`
}

// firecrawlResponse is the exact shape the LibreChat firecrawl client decodes:
// {success, data:{markdown, metadata}}.
type firecrawlResponse struct {
	Success bool           `json:"success"`
	Data    *firecrawlData `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type firecrawlData struct {
	Markdown string         `json:"markdown"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// maxScrapeBody caps what a scrape will parse. A body past it is TRUNCATED and
// therefore unparseable, which is the domain refusal below and not a status —
// firecrawl clients read data.success, not the status line.
const maxScrapeBody = 1 << 20

// scrapeScoped serves one scrape under a caller scope, so scraped pages land in
// the same corpus /v1/crawl fills — one crawl, one archive, whichever endpoint
// was used.
func scrapeScoped(c *zip.Ctx, s crawl.Scope) error {
	// A validated principal, like every other caller. firecrawl clients send
	// Authorization: Bearer <key> and this used to compare that bearer against a
	// shared key of our own; the bearer is an IAM credential now and the identity
	// middleware is what reads it. The refusal writes its bytes rather than
	// returning a zip error: the compat contract is {"status":…,"error":…}, and a
	// returned *zip.HTTPError renders as RFC 9457 problem-details, which moves the
	// sentence from `error` to `detail`.
	if !principal.Validated(c) {
		return writeErr(c, http.StatusUnauthorized, "scrape requires a validated principal")
	}

	// c.Body() hands back the whole body fasthttp already read, so the cap is
	// applied to the slice where the io.LimitReader used to apply it to the
	// stream. DECODE over a reader rather than json.Unmarshal: Decode stops at
	// the first complete JSON value and ignores what follows, which is what a
	// truncated body leaves and what the LimitReader has always accepted.
	b := c.Body()
	if len(b) > maxScrapeBody {
		b = b[:maxScrapeBody]
	}
	var req firecrawlRequest
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&req); err != nil || req.URL == "" {
		return writeJSON(c, http.StatusOK, firecrawlResponse{Success: false, Error: "missing url"})
	}

	page, err := crawl.Read(c.Context(), s, req.URL)
	if err != nil {
		return writeJSON(c, http.StatusOK, firecrawlResponse{Success: false, Error: err.Error()})
	}
	return writeJSON(c, http.StatusOK, firecrawlResponse{
		Success: true,
		Data:    &firecrawlData{Markdown: page.Markdown, Metadata: page.Metadata},
	})
}

// ── shared JSON writers ─────────────────────────────────────────────────────
//
// c.Bytes over c.JSON, deliberately: fiber's JSON writes
// `application/json; charset=utf-8` and these two contracts are read by clients
// we do not own, so the header stays the bare `application/json` these
// endpoints have always sent. Send touches no header, so SetHeader survives it.

func writeErr(c *zip.Ctx, status int, msg string) error {
	return writeRaw(c, status, []byte(fmt.Sprintf(`{"status":%d,"error":%q}`, status, msg)))
}

func writeJSON(c *zip.Ctx, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return writeErr(c, http.StatusInternalServerError, "encode error")
	}
	return writeRaw(c, status, b)
}

func writeRaw(c *zip.Ctx, status int, body []byte) error {
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(status, body)
}

// Mount registers the web-search surface on app.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("websearch.Use:  nil app")
	}
	logger := luxlog.Default()
	if logger == nil {
		return fmt.Errorf("websearch.Use:  nil luxlog.Default()")
	}
	logger = logger.New("subsystem", "websearch")
	// The package logs one thing and only one thing: an engine that went blind
	// on a query another engine answered (outcome.go). Held here rather than
	// threaded through metaSearch because the three entry points into search — this
	// subsystem's two handlers and compose.go's in-process caller — do not all
	// have a logger to pass, and a search that must not run without one would be
	// a worse trade than a warning that stays quiet in a library caller.
	setLogger(logger)
	// Bound the same way and for the same reason as the logger above: the paid
	// engines are asked from metaSearch, which every caller reaches and none of
	// them can hand a meter to. See meter.go.
	bindMeter(cloud.NewMeter(deps, "websearch"))

	// /v1/websearch/search admits a caller ONE way: a validated principal.
	// principal.Validated(c) is true when the identity middleware set X-User-Id
	// from a verified JWT — the SAME gate the whole /v1 data plane uses — so the
	// caller is already authenticated and already has a payer.
	//
	// There was a second arm: a shared X-API-Key, for the hanzo.chat server
	// reaching cloud without a user principal. A subsystem checking its own
	// credential is a subsystem doing IAM's job, and the two arms had to agree
	// about who a caller was while only one of them had ever seen an identity.
	// A service that needs this surface presents a service identity like any
	// other caller.
	// The NATIVE endpoint, registered on the *zip.App rather than on the cloud.Router,
	// and that is what makes the prose reach the document: zipdoc resolves a typed
	// op's path STATICALLY, and a cloud.Router parameter is an interface it cannot
	// follow to a prefix. The path below is absolute and the subsystem scope adds no
	// prefix, so this is the same registration spelled where the generator can read
	// it (the same move apps/exec made, for the same reason).
	reg := cloud.ZipApp(app)
	if reg == nil {
		return fmt.Errorf("websearch.Use:  router carries no typed-op registry")
	}
	// Named, not derived. The id a POST to /v1/websearch derives is
	// `create_websearch`, which reads as "make a websearch" — a resource this
	// subsystem does not have — and a model choosing from an `op` enum picks by
	// that name before it reads any description. It is a verb over a noun, like
	// every other op an agent is offered (lease_sandbox, run_in_sandbox).
	zip.Post(reg, Path, webSearch,
		zip.WithOperationID("search_web"),
		zip.WithSummary("Search the live web"))

	// /v1/websearch/search is named in cloud's paidReads: one debit per answer a
	// bought engine served, so this surface's READ is what spends. account's control
	// asks the same money rule the balance gate does, and is a no-op for a caller
	// that presented any credential — which is every service and console caller here.
	//
	// A GROUP REACHES A ROUTE AND NOTHING ELSE, so this covers the raw registrations
	// below and only those. The typed op above is reachable by NAME as well — MCP,
	// the call plane, the graph, the CLI — and asks the same control in its own
	// preamble, which is the one place every seam passes through. One decision, two
	// shapes, never two decisions.
	g := app.Group("/v1/websearch", cloud.RequireCSRFOnSpend())
	// Both arms are ONE handler and one context. There is no net/http adaptor
	// here any more, and the re-attachment that used to sit in this leaf went
	// with it: c.Context() IS the context cloud.Bridge parked the validated
	// caller in, so the payer the paid engines bill is resolved by construction
	// rather than by handing a rebuilt request back its own context. The adaptor
	// overwrote it with the transport's, which is why a search arriving here
	// once reached metaSearch with no principal and no ledger.
	g.All("/search", func(c *zip.Ctx) error {
		if !principal.Validated(c) {
			return writeErr(c, http.StatusUnauthorized, "web search requires a validated principal")
		}
		return searchNative(c)
	})

	// Scope resolved at the zip layer where the verified principal lives; the
	// service caller (chat) has none and lands in the shared prefix. See crawl.scope.
	scrape := func(c *zip.Ctx) error {
		org, _ := principal.Org(c)
		return scrapeScoped(c, crawl.Scope{Org: org, Project: principal.Project(c)})
	}
	// On the group, so the fetch answers under the name of the capability that
	// performs it. It sat at a top-level /v1/scrape to be reachable by a
	// firecrawl client, which composes {apiUrl}/{version}/scrape and offers no
	// way to say anything else — point that client at the API root and it lands
	// on /v1/scrape, point it here and it lands on the doubled
	// /v1/websearch/v1/scrape. So there is no base URL that reaches this
	// address, and that is the whole cost of the move: the BODY and the ANSWER
	// are still firecrawl's, and a caller is re-pointed at the Hanzo spelling
	// rather than redirected from the old one.
	g.Post("/scrape", scrape)

	logger.Info("web search surface mounted (native searxng-compat meta-search + firecrawl-compat scrape, both in-process)")
	return nil
}

// searchPath is the address the meta-search answers at, spelled once: Mount hangs
// the handler on it and the prose below is keyed by it, so the described operation
// is the served one by construction.
const searchPath = "/v1/websearch/search"

// scrapePath is the fetch's address, spelled once for searchPath's reason: the
// group composes it from "/scrape" and the prose below is keyed by it.
const scrapePath = "/v1/websearch/scrape"

// The prose for both surfaces, declared beside the wire facts that keep them
// untyped (see the package doc for why neither can be a typed op). zipdoc lifts an
// op's prose from its handler's doc comment and there is no typed op here to lift
// from, so without this the eight operations publish an operationId and nothing
// else: eight SDK methods that cannot explain themselves and eight CLI commands
// with no help text.
//
// Search states ONE fact once per published method because it IS one handler
// answering every method, and saying it several different ways would be several
// chances to be wrong.
//
// The method set comes from [openapi.Methods] — the projection's OWN set — and
// not from a list here. A local copy is a second place to be right: this file
// held one, it said seven methods including OPTIONS and TRACE, and the day the
// document stopped publishing those two the copy went on describing operations
// that no longer existed. Reading the projection's set moves both halves at once.
func init() {
	for _, method := range openapi.Methods() {
		openapi.Describe(searchPath, method,
			"Keyless web meta-search, in the SearXNG JSON envelope.",
			"Answers {query, number_of_results, results:[{url, title, content, engine}]} — the "+
				"exact /search?format=json contract a SearXNG client decodes, so an agent tool "+
				"configured against SearXNG reaches this with no change. `q` is the query and "+
				"`language` narrows it; both are read from the QUERY STRING.\n\n"+

				"Served in-process by a Go meta-search over keyless public engines, never a "+
				"third-party search API and never a search key. The enabled engines run "+
				"concurrently and their hits are merged, deduplicated by normalised URL (host and "+
				"path, trailing slash and fragment dropped, query kept — distinct queries are "+
				"distinct results) and capped at 30. Ranking is deterministic rather than scored: "+
				"the first configured engine's hits lead.\n\n"+

				"TWO WAYS IN, one-way equivalent, and no third: a validated principal — the same "+
				"gate the whole data plane uses — passes straight through, and a caller without "+
				"one must present the shared service key as X-API-Key, compared in constant time. "+
				"A deployment with no key configured answers 503 rather than opening the surface "+
				"to everyone, and a missing or wrong key is 401. It is never an open proxy. There "+
				"is no tenant scoping beyond that gate, and there is nothing to scope: the results "+
				"are public web pages, identical for every caller.\n\n"+

				"It fails SOFT on the engines and closed only on the gate. An engine that errors "+
				"or is served a bot-challenge page contributes zero results and never fails the "+
				"request, so an empty `results` is a real answer — nothing was found — and not an "+
				"outage. The array is always present, never null.\n\n"+

				"The one thing to get right: every method answers identically. This is one "+
				"handler registered for all of them, and it reads only the query string, so a body "+
				"sent on the write verbs is ignored rather than refused.")
	}

	openapi.Describe(scrapePath, http.MethodPost,
		"Fetch one page and get its extracted markdown, in the firecrawl envelope.",
		"Takes {url} and answers {success, data:{markdown, metadata}} — the exact contract a "+
			"firecrawl client decodes. The fetch, extraction and optional browser render run "+
			"in-process; there is no crawler pod to be down.\n\n"+

			"The shared service key is required as an Authorization Bearer, compared in constant "+
			"time: unset on the deployment is 503, missing or wrong is 401. Unlike search, a "+
			"validated principal does NOT substitute for it — this is the service-to-service endpoint.\n\n"+

			"A page is archived under the caller's own org and project, taken from the verified "+
			"principal when there is one, so a scrape lands in the same corpus /v1/crawl fills and "+
			"a URL already read under that scope is answered from the archive without touching the "+
			"network. A service caller carrying no principal shares the unscoped prefix.\n\n"+

			"The URL is caller-supplied and fetched from INSIDE the cluster, which makes this a "+
			"request-forgery primitive by construction: in-namespace service DNS and a cloud "+
			"metadata endpoint that hands credentials to anyone who asks are both a resolution "+
			"away. Only http and https are accepted, and every address actually dialled must be "+
			"public unicast — loopback, link-local, private and multicast are refused. The check "+
			"lives in the DIALER rather than on the hostname, because resolving a name to validate "+
			"it and then letting the transport resolve it again is a gap DNS rebinding walks "+
			"straight through; redirects re-enter the same dialer, so a public URL that bounces to "+
			"the metadata address is refused at the hop that matters.\n\n"+

			"The one thing to get right: FAILURE IS 200. A missing or unparseable url, a body over "+
			"the 1 MiB read cap, and a fetch that could not be completed all answer HTTP 200 with "+
			"success:false and a reason — a firecrawl client reads data.success, not the status "+
			"line. Only the two auth refusals use a status code, so a caller that branches on HTTP "+
			"status alone will read every failed scrape as a success.")
}
