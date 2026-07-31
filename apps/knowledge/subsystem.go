// subsystem.go mounts the KB retrieval + ingestion control-plane at /v1/kb/*. It is
// the thin surface on top of the framework DocType store (CRUD lives at
// /v1/kb) and the vector index (index.go):
//
//   - POST /v1/kb/search — the RAG entry point. An agent/chat resolves the org from
//     its validated principal and asks "what does this org know about X"; the org's
//     OWN vector namespace answers. This is how human wiki + AI memory become
//     retrievable org knowledge for an agent.
//
//   - GET /v1/kb/graph (graph.go) — the org's knowledge as a node/edge graph for a
//     force-directed renderer: pages/memories/sources as nodes; the parent tree,
//     wikilinks, and connector provenance as edges.
//
//   - POST /v1/kb/import (import.go) — an Obsidian-importer-equivalent that ingests
//     an Obsidian/Notion/Roam/Evernote export as a kb-page tree with links intact.
//
//   - Connectors (connectors.go): per-org OAuth connections to Slack/GitHub/Google
//     whose synced documents land in the SAME store + SAME index as manual pages.
//
// Every handler resolves its tenant through principal.Org (the ONE boundary) and
// scopes strictly to that org — a caller can only ever search or connect its own
// knowledge.
package knowledge

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// state is knowledge's own data; shared deps (logger, KMS, domain) live in the
// embedded cloud.Base. The vector index is the process singleton (index()) and the
// connectors are stateless, so this subsystem keeps no per-request or per-org state.
type state struct{}

// Mount wires the KB control-plane onto app per HIP-0106. CRUD + fixtures are the
// framework's surface; this adds only retrieval + connectors.
func Mount(app cloud.Router, deps cloud.Deps) error {
	// Every route but the upload is a TYPED op, and the op registry lives on the
	// *zip.App — a Router that is not backed by one has nowhere to put them, so
	// the mount fails rather than serving routes no projection knows about.
	if app != nil && cloud.ZipApp(app) == nil {
		return fmt.Errorf("knowledge.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	// kbAI reaches the lazy index() singleton (built on first use, without deps)
	// so embeddings run through the org/project-aligned EMBED client — the read-only
	// (pk-) credential, split from the completions (M2M) client (deps.AI).
	kbAI = deps.Embed
	return cloud.Mount(app, deps, "knowledge", build, routes)
}

// build carries no per-org state (the vector index is the process singleton and the
// connectors are stateless); it only logs the resolved KB surface.
func build(b cloud.Base) (state, error) {
	b.Log.Info("kb surface mounted",
		"index", index().enabled(), "vector", index().vectorURL, "embedModel", index().embedModel)
	return state{}, nil
}

// routes registers the retrieval + connector surface at /v1/kb. One prefix: the
// same handler reachable at two paths is two answers to "where is this".
//
// Every route is a TYPED op — registered on the App with its ABSOLUTE path,
// because the op registry (the one value OpenAPI, MCP and the CLI are projected
// from) keys on it — except the import: it takes a multipart "file" or a RAW
// archive body (readUpload), and a typed op decodes its input as JSON, so typing
// it would reject every real upload.
func routes(app cloud.Router, s *cloud.Service[state]) {
	z := cloud.ZipApp(app)
	o := ops{s: s}
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its tenant through it.
	app.Group("/v1/kb").Use(cloud.Bridge())
	zip.Post(z, "/v1/kb/search", o.search)                  // RAG entry point
	zip.Get(z, "/v1/kb/graph", o.graph)                     // force-directed knowledge graph
	app.Post("/v1/kb/import", cloud.Handle(s, importVault)) // raw/multipart upload — see above
	zip.Get(z, "/v1/kb/connectors", o.listConnectors)       // per-org OAuth ingestion
	zip.Get(z, "/v1/kb/connectors/catalog", o.listCatalog)  // the ONE catalog
	zip.Get(z, "/v1/kb/connectors/:provider/connect", o.connectStart)
	zip.Get(z, "/v1/kb/connectors/:provider/callback", o.connectCallback)
	zip.Post(z, "/v1/kb/connectors/:provider/sync", o.syncConnector)
	zip.Delete(z, "/v1/kb/connectors/:provider", o.disconnectConnector)
}

// ops binds the service to knowledge's typed handlers: a typed handler takes only
// a context and its decoded In, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the ONE tenant boundary — for a typed op. The org is
// exactly what SanitizeIdentity minted from the validated IAM owner claim,
// carried across the typed seam by cloud.Bridge, and NEVER an input field: an
// input is what the caller says about itself. Off the HTTP path there is none,
// so the op refuses rather than reading across orgs.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid principal required")
	}
	return org, nil
}

// providerRef addresses one connector provider.
type providerRef struct {
	// Provider is the connector id from the path (slack, github, google, …).
	Provider string `json:"provider"`
}

// searchResult is the retrieval answer. degraded is present only when the index
// was unreachable and the empty hit list is an honest "no context", not "nothing
// matched" — the RAG caller degrades instead of failing the turn.
type searchResult struct {
	// Hits are the matching documents, best score first.
	Hits []hit `json:"hits"`
	// Degraded is true only when retrieval was unavailable and hits is empty.
	Degraded bool `json:"degraded,omitempty"`
}

// searchBody is the POST /v1/kb/search request. `query` is the natural-language
// question; `limit` bounds hits (default 10, max 50); `project` optionally narrows
// to a project scope; `doctypes` optionally restricts to a subset of the indexed
// knowledge doctypes. There is NO org field — the org is the validated tenant, so a
// client can never search another org's knowledge by asking.
type searchBody struct {
	// Query is the natural-language question. Required.
	Query string `json:"query" validate:"required"`
	// Limit bounds the hits returned; default 10, max 50.
	Limit int `json:"limit,omitempty"`
	// Project narrows retrieval to one project scope.
	Project string `json:"project,omitempty"`
	// DocTypes restricts retrieval to a subset of kb-page, kb-memory, kb-source.
	// An empty or foreign list means all indexed knowledge doctypes.
	DocTypes []string `json:"doctypes,omitempty"`
}

// search runs an org-scoped semantic retrieval over the org's knowledge namespace.
// The org comes from principal.Org (a validated principal), so cross-tenant
// retrieval is impossible: the collection AND the payload filter are both pinned to
// this org. An unreachable/disabled index returns an honest empty result set, never
// a 5xx — the RAG caller degrades to no-context rather than failing the turn.
//
// Example: {"query": "how do we rotate the signing key", "limit": 10}
// Response: {"hits": [{"doctype": "kb-page", "name": "runbook", "title": "Key rotation", "score": 0.82}]}
func (o ops) search(ctx context.Context, in *searchBody) (*searchResult, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, zip.ErrBadRequest("query is required")
	}

	req := searchReq{
		org:      org,
		query:    in.Query,
		limit:    in.Limit,
		project:  strings.TrimSpace(in.Project),
		doctypes: sanitizeDocTypes(in.DocTypes),
	}
	hits, err := index().searchDoc(ctx, req)
	if err != nil {
		// Log-and-empty: a retrieval outage must not surface as a hard error to the
		// agent turn. The framework CRUD store is unaffected.
		o.s.Log.Warn("kb search failed", "org", org, "err", err)
		return &searchResult{Hits: []hit{}, Degraded: true}, nil
	}
	return &searchResult{Hits: hits}, nil
}

// sanitizeDocTypes restricts a client-supplied doctype filter to the KB knowledge
// set — a caller can never widen the search to arbitrary (e.g. another lane's)
// doctypes through this field, and an empty/foreign list falls back to all indexed
// knowledge doctypes.
func sanitizeDocTypes(in []string) []string {
	allowed := map[string]bool{DTPage: true, DTMemory: true, DTSource: true}
	out := make([]string, 0, len(in))
	for _, d := range in {
		if allowed[d] {
			out = append(out, d)
		}
	}
	return out
}
