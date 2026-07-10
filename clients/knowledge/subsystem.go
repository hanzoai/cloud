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
// Every handler resolves its tenant through principal.Org (the ONE boundary) and
// scopes strictly to that org — a caller can only ever search or connect its own
// knowledge.
package knowledge

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// svc holds the mounted subsystem's dependencies. deps carries the KMS client the
// connectors store OAuth tokens in and the logger; the vector index is the process
// singleton (index()), so svc holds no per-request or per-org state.
type svc struct {
	deps cloud.Deps
}

// Mount wires the KB control-plane onto app per HIP-0106. CRUD + fixtures are the
// framework's surface; this adds only retrieval + connectors.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("kb.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("kb.Mount: nil deps.Logger")
	}
	// kbAI reaches the lazy index() singleton (built on first use, without deps)
	// so embeddings run through the ONE shared, org/project-aligned AI client.
	kbAI = deps.AI

	s := &svc{deps: deps}
	log := deps.Logger.New("subsystem", "knowledge")

	// Canonical /v1/knowledge surface + pre-rename /v1/kb back-compat alias
	// (kb→knowledge): the SAME handlers under both prefixes, so clients pinned
	// to /v1/kb keep working while /v1/knowledge is the canonical surface.
	for _, p := range []string{"/v1/knowledge", "/v1/kb"} {
		app.Post(p+"/search", s.search)                 // RAG entry point
		app.Get(p+"/connectors", s.listConnectors)      // per-org OAuth ingestion
		app.Get(p+"/connectors/catalog", s.listCatalog) // the ONE catalog
		app.Get(p+"/connectors/:provider/connect", s.connectStart)
		app.Get(p+"/connectors/:provider/callback", s.connectCallback)
		app.Post(p+"/connectors/:provider/sync", s.syncConnector)
		app.Delete(p+"/connectors/:provider", s.disconnectConnector)
	}

	log.Info("kb surface mounted",
		"index", index().enabled(), "vector", index().vectorURL, "embedModel", index().embedModel)
	return nil
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
// The org comes from principal.Org (a validated principal), so cross-tenant
// retrieval is impossible: the collection AND the payload filter are both pinned to
// this org. An unreachable/disabled index returns an honest empty result set, never
// a 5xx — the RAG caller degrades to no-context rather than failing the turn.
func (s *svc) search(c *zip.Ctx) error {
	org, ok := principal.Org(c)
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
		s.deps.Logger.Warn("kb search failed", "org", org, "err", err)
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
