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

import (
	"context"
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

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init. Go
// drops comments at compile time, so this build-time pass is the ONLY way that
// prose reaches the published document, the MCP tool list and the generated SDKs.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the subsystem's state onto every typed op. A typed handler takes a
// context and its decoded In and nothing else, so the state rides on the receiver.
type ops struct{ s *cloud.Service[state] }

// routes registers the retrieval + connector surface at /v1/kb. One prefix: the
// same handler reachable at two paths is two answers to "where is this".
//
// Every route but the import is a TYPED op: ONE registry entry that is at once
// the REST route, the OpenAPI operation with its schemas, the MCP tool, the CLI
// command and the generated SDK method.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// cloud.Bridge carries into a typed op the request its signature drops — here
	// the validated org, which is the ONLY tenant key this surface may use and can
	// never be an In field. The COMPOSER installs it, not this subsystem: the
	// fused host once at its root (serve.go), and a plugin program's constructor
	// likewise. An install here would hang middleware on declared prefixes with no
	// routes beneath them, a program zip refuses to compose.
	g := app.Group("/v1/kb")
	o := ops{s: s}
	zip.Post(g, "/search", o.search)                            // RAG entry point
	zip.Get(g, "/graph", o.graph)                               // force-directed knowledge graph
	zip.Get(g, "/connectors", o.listConnectors)                 // per-org OAuth ingestion
	zip.Get(g, "/connectors/catalog", o.listCatalog)            // the ONE catalog
	zip.Get(g, "/connectors/:provider/connect", o.connectStart) //
	zip.Get(g, "/connectors/:provider/callback", o.connectCallback)
	zip.Post(g, "/connectors/:provider/sync", o.syncConnector)
	zip.Delete(g, "/connectors/:provider", o.disconnectConnector)
	// UNTYPED, and it has to be: this route takes an UPLOAD — a multipart "file"
	// part, else the raw request body — and that body is a zip archive, an .enex XML
	// document or a JSON export, chosen by ?format=. zip decodes a typed op's body
	// as JSON before the handler runs (op.invoke) and 400s on anything it cannot
	// parse, so a typed In would refuse every Obsidian and Notion vault this route
	// exists to accept. See readUpload in import.go.
	g.Post("/import", cloud.Handle(s, importVault)) // Obsidian/Notion/Roam/Evernote import
}

// tenant resolves the caller's org — the ONE tenant boundary on this surface —
// for a typed op, which receives a context and nothing else. cloud.Bridge parks
// the validated org there; an In field could only ever be a tenant key the caller
// asserted for itself, which is a cross-tenant read.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid principal required")
	}
	return org, nil
}

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// searchIn is the POST /v1/kb/search request. There is NO org field — the org is
// the validated tenant, so a client can never search another org's knowledge by
// asking.
type searchIn struct {
	// Query is the natural-language question. Required.
	Query string `json:"query"`
	// Limit bounds the hits returned. Default 10, maximum 50.
	Limit int `json:"limit,omitempty"`
	// Project narrows retrieval to one project scope.
	Project string `json:"project,omitempty"`
	// DocTypes restricts retrieval to a subset of the indexed knowledge doctypes
	// (kb-page, kb-memory, kb-source). An empty or foreign list reads all of them.
	DocTypes []string `json:"doctypes,omitempty"`
}

// searchOut is the retrieval answer.
type searchOut struct {
	// Hits are the matching passages, most relevant first.
	Hits []hit `json:"hits"`
	// Degraded is true when the index was unreachable and this answer is honestly
	// empty rather than wrong — a RAG caller continues with no context instead of
	// failing the turn. Absent on a normal answer.
	Degraded bool `json:"degraded,omitempty"`
}

// SearchKnowledge runs a semantic search over the caller org's own knowledge —
// its wiki pages, its agent memories and everything its connectors have synced —
// and returns the matching passages. This is the RAG entry point: an agent asks
// "what does this org know about X" and the org's OWN vector namespace answers.
// The org comes from the validated principal, and both the collection and the
// payload filter are pinned to it, so cross-tenant retrieval is impossible. An
// unreachable index returns an honest empty result set with degraded=true, never
// a 5xx.
//
// Example: {"query": "how do we rotate the signing key", "limit": 5}
func (o ops) search(ctx context.Context, in *searchIn) (*searchOut, error) {
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
		return &searchOut{Hits: []hit{}, Degraded: true}, nil
	}
	return &searchOut{Hits: hits}, nil
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
