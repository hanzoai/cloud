// subsystem.go mounts the KB retrieval + ingestion control-plane at /v1/kb/*. It is
// the thin surface on top of the framework DocType store (CRUD lives at
// /v1/framework/kb-*) and the vector index (index.go):
//
//   - POST /v1/kb/search — the RAG entry point. An agent/chat resolves the org from
//     its validated principal and asks "what does this org know about X"; the org's
//     OWN vector namespace answers. This is how human wiki + AI memory become
//     retrievable org knowledge for an agent.
//
//   - Connectors (connectors.go): per-org OAuth connections to Slack/GitHub/Google
//     whose synced documents land in the SAME store + SAME index as manual pages.
//
// Every handler resolves its tenant through principal.Tenant (the ONE boundary) and
// scopes strictly to that org — a caller can only ever search or connect its own
// knowledge.
package knowledge

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// state is knowledge's own data; shared deps (logger, KMS, domain) live in the
// embedded cloud.Base. The vector index is the process singleton (index()) and the
// connectors are stateless, so this subsystem keeps no per-request or per-org state.
type state struct{}

// Mount wires the KB control-plane onto app per HIP-0106. CRUD + fixtures are the
// framework's surface; this adds only retrieval + connectors.
func Mount(app *zip.App, deps cloud.Deps) error {
	// kbAI reaches the lazy index() singleton (built on first use, without deps)
	// so embeddings run through the ONE shared, org/project-aligned AI client.
	kbAI = deps.AI
	return cloud.Mount(app, deps, "knowledge", build, routes)
}

// build carries no per-org state (the vector index is the process singleton and the
// connectors are stateless); it only logs the resolved KB surface.
func build(b cloud.Base) (state, error) {
	b.Log.Info("kb surface mounted",
		"index", index().enabled(), "vector", index().vectorURL, "embedModel", index().embedModel)
	return state{}, nil
}

// routes registers the retrieval + connector surface under both the canonical
// /v1/knowledge prefix and the pre-rename /v1/kb back-compat alias (kb→knowledge):
// the SAME handlers under both prefixes, so clients pinned to /v1/kb keep working
// while /v1/knowledge is the canonical surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	for _, p := range []string{"/v1/knowledge", "/v1/kb"} {
		app.Post(p+"/search", cloud.Handle(s, search))                 // RAG entry point
		app.Get(p+"/connectors", cloud.Handle(s, listConnectors))      // per-org OAuth ingestion
		app.Get(p+"/connectors/catalog", cloud.Handle(s, listCatalog)) // the ONE catalog
		app.Get(p+"/connectors/:provider/connect", cloud.Handle(s, connectStart))
		app.Get(p+"/connectors/:provider/callback", cloud.Handle(s, connectCallback))
		app.Post(p+"/connectors/:provider/sync", cloud.Handle(s, syncConnector))
		app.Delete(p+"/connectors/:provider", cloud.Handle(s, disconnectConnector))
	}
}

func init() {
	// Order 130: after the framework (129) so its DocType store is mounted first
	// (the connectors call framework.Ingest into it), and before the AI /v1/*
	// catch-all (150) so /v1/kb/* resolves here.
	cloud.Register("knowledge", 130, cloud.Typed(Mount))
}

// searchBody is the POST /v1/kb/search request. `query` is the natural-language
// question; `limit` bounds hits (default 10, max 50); `project` optionally narrows
// to a project scope; `doctypes` optionally restricts to a subset of the indexed
// knowledge doctypes. There is NO org field — the org is the validated tenant, so a
// client can never search another org's knowledge by asking.
type searchBody struct {
	Query    string   `json:"query"`
	Limit    int      `json:"limit,omitempty"`
	Project  string   `json:"project,omitempty"`
	DocTypes []string `json:"doctypes,omitempty"`
}

// search runs an org-scoped semantic retrieval over the org's knowledge namespace.
// The org comes from principal.Tenant (a validated principal), so cross-tenant
// retrieval is impossible: the collection AND the payload filter are both pinned to
// this org. An unreachable/disabled index returns an honest empty result set, never
// a 5xx — the RAG caller degrades to no-context rather than failing the turn.
func search(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	var body searchBody
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Query) == "" {
		return zip.ErrBadRequest("query is required")
	}

	req := searchReq{
		org:      org,
		query:    body.Query,
		limit:    body.Limit,
		project:  strings.TrimSpace(body.Project),
		doctypes: sanitizeDocTypes(body.DocTypes),
	}
	hits, err := index().searchDoc(c.Context(), req)
	if err != nil {
		// Log-and-empty: a retrieval outage must not surface as a hard error to the
		// agent turn. The framework CRUD store is unaffected.
		s.Log.Warn("kb search failed", "org", org, "err", err)
		return c.JSON(http.StatusOK, map[string]any{"hits": []hit{}, "degraded": true})
	}
	return c.JSON(http.StatusOK, map[string]any{"hits": hits})
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
