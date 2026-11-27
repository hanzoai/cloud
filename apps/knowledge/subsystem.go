// subsystem.go mounts the KB retrieval + ingestion control-plane at /v1/knowledge/*. It is
// the thin surface on top of the framework DocType store (CRUD lives at
// /v1/framework) and the two retrieval legs (index.go, lexical.go):
//
//   - POST /v1/knowledge/search — the RAG entry point. An agent/chat resolves the org from
//     its validated principal and asks "what does this org know about X"; the org's
//     OWN passages and lexical index answer, fused (retrieve.go). This is how human
//     wiki + AI memory become retrievable org knowledge for an agent.
//
//   - GET /v1/knowledge/graph (graph.go) — the org's knowledge as a node/edge graph for a
//     force-directed renderer: pages/memories/sources as nodes; the parent tree,
//     wikilinks, and connector provenance as edges.
//
//   - POST /v1/knowledge/import (import.go) — an Obsidian-importer-equivalent that ingests
//     an Obsidian/Notion/Roam/Evernote export as a kb.page tree with links intact.
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
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// state is knowledge's own data; shared deps (logger, KMS, domain) live in the
// embedded cloud.Base. The vector leg is the process's idx (index.go), which the
// hooks and the plane op reach without a request, and the connectors are
// stateless, so this subsystem keeps no per-request state of its own.
type state struct{}

// Mount wires the KB control-plane onto app per HIP-0106. CRUD + fixtures are the
// framework's surface; this adds only retrieval + connectors.
//
// It sets this process's vector leg (idx) and publishes its plane op once the
// mount has succeeded, whole. Embeddings run through the org/project-aligned
// EMBED client — the read-only (pk-) credential, split from the completions
// (M2M) client (deps.AI). The gateway serves the SKU, not the raw upstream name:
// "bge-m3" 400s.
func Use(app cloud.Router, deps cloud.Deps) error {
	model := env.Or("CLOUD_EMBED_MODEL", "zen-embedding")
	x := &indexer{ai: deps.Embed, chat: deps.AI, model: model, space: vec.Policy(model), builds: map[string]chan struct{}{}}
	x.ctx, x.stop = context.WithCancel(context.Background())
	if err := cloud.Use(app, deps, "knowledge", func(b cloud.Base) (state, error) { return mount(b, x) }, routes); err != nil {
		x.stop()
		return err
	}
	idx = x
	expose()
	x.wg.Add(1)
	go func() {
		defer x.wg.Done()
		x.q.await(x.ctx, x.log)
		x.resumeLoop()
	}()
	return nil
}

// mount opens nothing: the org stores open on first use, one file per org.
func mount(b cloud.Base, x *indexer) (state, error) {
	x.stores = cloud.NewOrgStore(b, "knowledge", openStore)
	x.log = b.Log
	b.Log.Info("kb surface mounted", "vector", x.enabled(), "embedModel", x.model, "embedSpace", x.space.Model)
	return state{}, nil
}

// Shutdown ends the builds in flight, then closes every open org store.
// Idempotent.
func Shutdown() error {
	x := idx
	if x == nil {
		return nil
	}
	x.q.close()
	x.stop()
	x.wg.Wait()
	err := x.stores.CloseAll()
	idx = nil
	return err
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

// routes registers the retrieval + connector surface at /v1/knowledge. One prefix: the
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
	o := ops{s: s}
	g := app.Group("/v1/knowledge")
	g.Post("/search", o.search)                            // RAG entry point
	g.Post("/files", o.registerFile)                       // an org object becomes a workspace file
	g.Get("/files", o.listFiles)                           //
	g.Post("/files/search", o.searchFiles)                 // passages that match, across the org's files
	g.Post("/files/retrieve", o.retrieveFiles)             // ToC-first: pick sections, drill, expand the graph
	g.Get("/files/:id", o.getFile)                         //
	g.Delete("/files/:id", o.forgetFile)                   //
	g.Get("/files/:id/toc", o.fileToc)                     // the table of contents, summarized
	g.Get("/files/:id/sections/:section", o.openSection)   // one section, its links and entities
	g.Get("/files/:id/graph", o.fileGraph)                 // the files it is joined to
	g.Post("/reindex", o.reindex)                          // rebuild both legs from the documents
	g.Get("/graph", o.graph)                               // force-directed knowledge graph
	g.Get("/connectors", o.listConnectors)                 // per-org OAuth ingestion
	g.Get("/connectors/catalog", o.listCatalog)            // the ONE catalog
	g.Get("/connectors/:provider/connect", o.connectStart) //
	g.Get("/connectors/:provider/callback", o.connectCallback)
	g.Post("/connectors/:provider/sync", o.syncConnector)
	g.Delete("/connectors/:provider", o.disconnectConnector)
	// UNTYPED, and it has to be: this route takes an UPLOAD — a multipart "file"
	// part, else the raw request body — and that body is a zip archive, an .enex XML
	// document or a JSON export, chosen by ?format=. zip decodes a typed op's body
	// as JSON before the handler runs (op.invoke) and 400s on anything it cannot
	// parse, so a typed In would refuse every Obsidian and Notion vault this route
	// exists to accept. See readUpload in import.go.
	g.Raw(http.MethodPost, "/import", cloud.Handle(s, importVault)) // Obsidian/Notion/Roam/Evernote import
}

// searchIn is the POST /v1/knowledge/search request. There is NO org field — the org is
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
	// (kb.page, kb.memory, kb.source). An empty or foreign list reads all of them.
	DocTypes []string `json:"doctypes,omitempty"`
}

// searchOut is the retrieval answer.
type searchOut struct {
	// Hits are the matching documents, most relevant first: the order is the two
	// legs fused, so a document both the semantic and the keyword leg found comes
	// before one only a single leg found.
	Hits []hit `json:"hits"`
	// Degraded is true when a leg FAILED — the query could not be embedded, or a
	// store could not be read — and Hits are what the other leg found, possibly
	// nothing. A RAG caller continues with what it got instead of failing the turn.
	// Absent on a normal answer, including one from a deployment that runs only
	// one of the legs.
	Degraded bool `json:"degraded,omitempty"`
}

// SearchKnowledge runs a hybrid search over the caller org's own knowledge —
// its wiki pages, its agent memories and everything its connectors have synced.
// This is the RAG entry point: an agent asks "what does this org know about X"
// and the org's OWN knowledge answers. Two legs run and are fused by reciprocal
// rank: a semantic leg that embeds the query and compares it with every passage
// of every document in reach (a document is cut into passages of about 2000
// bytes, so a fact deep in a long page is found), and a keyword leg over the
// org's knowledge index. The org comes from the validated principal and its
// passages live in its own database, so cross-tenant retrieval is impossible. A
// failed leg answers 200 with what the other leg found and degraded=true, never
// a 5xx.
//
// Example: {"query": "how do we rotate the signing key", "limit": 5}
func (o ops) search(ctx context.Context, in *searchIn) (*searchOut, error) {
	org, err := principal.Acting(ctx)
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
		subject:  subject(ctx),
	}
	hits, degraded := retrieve(ctx, o.s.Log, req)
	return &searchOut{Hits: hits, Degraded: degraded}, nil
}

// sanitizeDocTypes restricts a client-supplied doctype filter to the KB knowledge
// set — a caller can never widen the search to arbitrary (e.g. another lane's)
// doctypes through this field, and an empty/foreign list falls back to all indexed
// knowledge doctypes.
func sanitizeDocTypes(in []string) []string {
	allowed := map[string]bool{DTPage.String(): true, DTMemory.String(): true, DTSource.String(): true}
	out := make([]string, 0, len(in))
	for _, d := range in {
		if allowed[d] {
			out = append(out, d)
		}
	}
	return out
}
