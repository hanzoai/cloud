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
