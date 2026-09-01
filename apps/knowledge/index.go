package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/framework"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/namespace"
)

// index.go is the ONE per-org vector-write + search path for KB knowledge. Every
// knowledge document — a manual wiki page, an AI memory, a connector-ingested doc
// — flows through THIS file and nowhere else: the after_save hook (hooks.go) calls
// indexDoc, the trash hook calls deindexDoc, and the retrieval subsystem
// (subsystem.go) calls searchDoc. Connectors never fork this path; they produce
// framework documents and the SAME hook indexes them.
//
// Store: the live in-cluster Qdrant (hanzoai/vector), reached over its REST API
// exactly as clients/product does — plain net/http, no gRPC client dep. Per-org
// isolation is PHYSICAL: each org gets its OWN collection ("kb_<org>"), and every
// point ALSO carries the org in its payload so a search filters payload.org==org
// (defense in depth — a collection-name bug can never leak across tenants because
// the payload filter would still exclude foreign points, and vice-versa).
//
// Embeddings: the Hanzo AI gateway (standard /embeddings), the SAME model
// for index and query so vector dimensions always match. Indexing is FAIL-OPEN: if
// the gateway or Qdrant is unreachable, the document is still saved (the after_save
// hook logs and moves on) — knowledge writes never block on the index. Query is
// FAIL-HONEST: an unreachable store yields an empty result, never a 5xx or a
// fabricated hit.

// indexer is the process-wide knowledge index, configured once from env at first
// use (mirrors clients/product.loadConfig — the config is deployment-static). It
// holds no per-org state: the org is a parameter on every call, so ONE client
// serves all tenants and the org can never be captured from a stale field.
type indexer struct {
	vectorURL  string         // Qdrant REST base, e.g. http://vector.hanzo.svc.cluster.local:6333
	vectorKey  string         // Qdrant api-key header (empty ⇒ unauthenticated in-cluster)
	ai         cloud.AIClient // shared AI client — embeddings go the ONE org/project-aligned, metered path
	embedModel string         // embedding model — SAME for index + query
	dims       int            // embedding dimension the collection is created with
	http       *http.Client

	mu          sync.Mutex
	ensuredOrgs map[string]bool // collections already ensured this process (best-effort cache)
}

var (
	idxOnce sync.Once
	idx     *indexer
	kbAI    cloud.AIClient // set once in Mount; the AI client the lazy index() embeds through
)

// index resolves the process-wide indexer, configured once from env. Endpoints
// default to the same in-cluster service DNS clients/product uses; the embedding
// model + dims are operator-tunable but MUST be stable for a collection's life
// (changing dims after points exist is refused per-collection, never silently
// corrupts). embedKey defaults to the AI virtual key already mounted on cloud-api.
func index() *indexer {
	idxOnce.Do(func() {
		dims := 1536
		if v := os.Getenv("KB_EMBED_DIMS"); v != "" {
			if n, err := parseInt(v); err == nil && n > 0 && n <= 8192 {
				dims = n
			}
		}
		idx = &indexer{
			vectorURL:   strings.TrimRight(environ.Or("vectorEndpoint", "http://vector.hanzo.svc.cluster.local:6333"), "/"),
			vectorKey:   os.Getenv("vectorApiKey"),
			ai:          kbAI,
			embedModel:  environ.Or("CLOUD_EMBED_MODEL", "zen-embedding"), // the served SKU; raw "bge-m3" 400s at the gateway
			dims:        dims,
			http:        &http.Client{Timeout: 20 * time.Second},
			ensuredOrgs: map[string]bool{},
		}
	})
	return idx
}

// enabled reports whether the index can operate (an embedding key is provisioned).
// When disabled, indexing is a silent no-op and search returns empty — an
// un-provisioned deployment degrades to a pure DocType store with an honest empty
// retrieval, never a crash and never a fabricated result.
func (x *indexer) enabled() bool { return x.ai != nil && x.vectorURL != "" }

// collection is the org's PHYSICAL vector namespace. The "kb_" prefix keeps KB's
// collections disjoint from any other vector use of the same org slug. The org is
// run through namespace.Sanitize — the codebase's ONE org-slug normalizer,
// shared with S3/KMS/projects — so the PHYSICAL namespace is INJECTIVE in the
// owner: distinct owners that would otherwise fold onto one Qdrant collection ("a b"
// vs "a_b") get a hash-suffixed slug and stay distinct, and an unsafe-rune org folds
// to "" (which yields the sentinel "kb_" namespace that indexing/search both use
// consistently, never a foreign one). This is defense in depth: the payload.org
// filter already blocks a cross-tenant READ, but the physical boundary now holds too
// (RED LOW-1). Sanitizing happens HERE, the one place every caller funnels through,
// so index and search always derive the same collection for the same org.
func (x *indexer) collection(org string) string {
	return "kb_" + namespace.Sanitize(org)
}

// pointID is the deterministic id for a document's vector: a UUIDv5-shaped hex of
// (org|doctype|name). Deterministic so a re-save OVERWRITES the same point (no
// duplicates) and a trash DELETES exactly it. The org is folded into the id so two
// orgs' identically-named docs never share a point even if a collection were
// (incorrectly) shared. Qdrant accepts a 32-hex-digit id as a UUID.
func pointID(org, doctype, name string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + doctype + "\x00" + name))
	h := hex.EncodeToString(sum[:16]) // 32 hex chars = a UUID-shaped point id
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// docText renders a knowledge document to the plain text the indexer embeds. It is
// doctype-aware (title + the body/content field) and strips a Lexical RichText body
// to its text runs so the embedding is over prose, not editor JSON. Empty text ⇒
// nothing to index (the caller skips the upsert).
func docText(dt framework.ID, title string, data map[string]any) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	switch dt {
	case DTPage:
		b.WriteString(lexicalText(str(data["body"])))
	case DTMemory:
		b.WriteString(str(data["content"]))
	case DTSource:
		b.WriteString(str(data["body"]))
	}
	return strings.TrimSpace(b.String())
}

// docMeta is the non-secret payload stored alongside a vector for filtering +
// citation on retrieval. org is ALWAYS present so a search can filter
// payload.org==org (defense in depth over the per-org collection). No document
// body/secret beyond the title is stored — the payload is for scoping and display,
// not a data copy.
func docMeta(org, doctype, name, title string, data map[string]any) map[string]any {
	m := map[string]any{
		"org":     org,
		"doctype": doctype,
		"name":    name,
		"title":   title,
	}
	if p := str(data["project"]); p != "" {
		m["project"] = p
	}
	if pr := str(data["provider"]); pr != "" {
		m["provider"] = pr
	}
	if u := str(data["url"]); u != "" {
		m["url"] = u
	}
	if o := str(data["owner"]); o != "" {
		m["owner"] = o
	}
	return m
}

// indexDoc embeds a knowledge document and upserts it into the org's collection.
// FAIL-OPEN by contract: it is called from the after_save hook, so a returned error
// is logged but the document is already persisted — the knowledge write is never
// blocked by an index outage. Empty text (an untitled, empty page) is a no-op.
func (x *indexer) indexDoc(ctx context.Context, org string, dt framework.ID, name, title string, data map[string]any) error {
	if !x.enabled() {
		return nil
	}
	text := docText(dt, title, data)
	if text == "" {
		// Nothing to embed, but a PRIOR version may have text — remove any stale point
		// so an emptied doc stops being retrievable.
		return x.deindexDoc(ctx, org, dt, name)
	}
	vec, err := x.embed(ctx, org, "", text)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if err := x.ensureCollection(ctx, org); err != nil {
		return fmt.Errorf("ensure collection: %w", err)
	}
	body := map[string]any{
		"points": []map[string]any{{
			"id":      pointID(org, dt.String(), name),
			"vector":  vec,
			"payload": docMeta(org, dt.String(), name, title, data),
		}},
	}
	// wait=true so a create-then-immediately-search (the RAG proof) is consistent.
	return x.qdrant(ctx, http.MethodPut, "/collections/"+x.collection(org)+"/points?wait=true", body, nil)
}

// deindexDoc removes a document's point from the org's collection. Best-effort:
// callers (the trash hook) swallow its error so a vector outage never blocks a
// delete. Deletes by explicit point id AND is scoped to the org's own collection.
func (x *indexer) deindexDoc(ctx context.Context, org string, dt framework.ID, name string) error {
	if !x.enabled() {
		return nil
	}
	body := map[string]any{"points": []string{pointID(org, dt.String(), name)}}
	return x.qdrant(ctx, http.MethodPost, "/collections/"+x.collection(org)+"/points/delete?wait=true", body, nil)
}

// deindexProvider removes every ingested point for a provider from the org's
// collection (used on connector disconnect). Filters payload.org==org AND
// payload.provider==provider — the org filter is defense in depth even though the
// collection is already the org's own.
func (x *indexer) deindexProvider(ctx context.Context, org, provider string) error {
	if !x.enabled() {
		return nil
	}
	body := map[string]any{
		"filter": map[string]any{"must": []map[string]any{
			{"key": "org", "match": map[string]any{"value": org}},
			{"key": "provider", "match": map[string]any{"value": provider}},
		}},
	}
	return x.qdrant(ctx, http.MethodPost, "/collections/"+x.collection(org)+"/points/delete?wait=true", body, nil)
}

// hit is one semantic-search result: enough to cite and open the source, never a
// full copy of the document body.
type hit struct {
	// DocType is which kind of knowledge matched, by address: kb.page (a wiki
	// page), kb.memory (a unit of agent memory) or kb.source (a document a
	// connector ingested).
	// Those three are the whole indexed set, and searchIn.DocTypes filters on them.
	DocType string `json:"doctype"`
	// Name is the document's name in the framework store — the id to read or open
	// it with. Unique per (org, doctype), so it identifies the document with
	// DocType and not alone.
	Name string `json:"name"`
	// Title is the document's title as it was indexed. Empty for a document saved
	// without one; it is a label to show, never the id (that is Name).
	Title string `json:"title"`
	// Project is the project scope the document was saved under. Absent for a
	// document saved with none, which is also why a project-scoped query cannot
	// reach it.
	Project string `json:"project,omitempty"`
	// Provider is the connector that ingested the document — github, slack, google
	// or notion. Absent for a page or memory written in the product, which came
	// from no connector.
	Provider string `json:"provider,omitempty"`
	// URL is the document's link back into the app it was ingested from. Absent
	// when the indexed payload carries none, which is the normal case for pages and
	// memories.
	URL string `json:"url,omitempty"`
	// Text is the document's text as the index embeds it, read from the store
	// by Semantic for a reranker to score; it is never on the wire.
	Text string `json:"-"`
	// Score is the cosine similarity between the query's embedding and the
	// document's, from -1 to 1, higher being closer — the collection is created
	// with Cosine distance. Hits arrive ordered by it, descending. There is no
	// absolute cutoff: what counts as a good score moves with the query and the
	// embedding model, so compare scores within one response and not across
	// queries.
	Score float64 `json:"score"`
}

// searchReq is a parsed, org-scoped retrieval query. org is set by the handler from
// principal.Org — NEVER from a client field — so a caller can only ever search
// its OWN knowledge.
type searchReq struct {
	org      string
	query    string
	limit    int
	project  string   // optional scope filter
	doctypes []string // optional restriction (default: all indexed doctypes)
	// subject is the asking person, or "" for the org itself; it decides whose
	// owned documents are in reach. Set by the caller from the validated
	// principal, never from a client field.
	subject string
}

// searchDoc runs a per-org semantic search: embed the query with the SAME model,
// then Qdrant-search the org's OWN collection with a payload filter that pins
// org==req.org (defense in depth) plus any project/doctype restriction. Returns an
// empty slice (never an error) when the index is disabled or the query is empty;
// an unreachable store surfaces an error the handler turns into an honest empty.
func (x *indexer) searchDoc(ctx context.Context, req searchReq) ([]hit, error) {
	if !x.enabled() || strings.TrimSpace(req.query) == "" {
		return []hit{}, nil
	}
	vec, err := x.embed(ctx, req.org, req.project, req.query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	limit := req.limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	// The payload filter ALWAYS pins the org. This is the second, independent
	// tenant boundary: even if the collection name were wrong, a point from another
	// org could never match because its payload.org differs.
	must := []map[string]any{{"key": "org", "match": map[string]any{"value": req.org}}}
	if req.project != "" {
		must = append(must, map[string]any{"key": "project", "match": map[string]any{"value": req.project}})
	}
	if len(req.doctypes) > 0 {
		must = append(must, map[string]any{"key": "doctype", "match": map[string]any{"any": req.doctypes}})
	}

	body := map[string]any{
		"vector":       vec,
		"limit":        limit,
		"with_payload": true,
		"filter":       ownerFilter(must, req.subject),
	}
	var resp struct {
		Result []struct {
			Score   float64        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := x.qdrant(ctx, http.MethodPost, "/collections/"+x.collection(req.org)+"/points/search", body, &resp); err != nil {
		return nil, err
	}
	out := make([]hit, 0, len(resp.Result))
	for _, r := range resp.Result {
		// Belt-and-suspenders: drop any point whose payload org doesn't match (should
		// be impossible given the filter, but a search result is never trusted to be
		// in-tenant without the check).
		if str(r.Payload["org"]) != req.org {
			continue
		}
		out = append(out, hit{
			DocType:  str(r.Payload["doctype"]),
			Name:     str(r.Payload["name"]),
			Title:    str(r.Payload["title"]),
			Project:  str(r.Payload["project"]),
			Provider: str(r.Payload["provider"]),
			URL:      str(r.Payload["url"]),
			Score:    r.Score,
		})
	}
	return out, nil
}

// ownerFilter is the reach of one asker over owned documents, on top of the
// tenant clauses in must. The org itself (subject "") reaches only what has no
// owner; a person reaches that and their own — a document owned by anyone else
// is outside every filter but its owner's.
func ownerFilter(must []map[string]any, subject string) map[string]any {
	shared := map[string]any{"is_empty": map[string]any{"key": "owner"}}
	if subject == "" {
		return map[string]any{"must": append(must, shared)}
	}
	return map[string]any{
		"must":   must,
		"should": []map[string]any{shared, {"key": "owner", "match": map[string]any{"value": subject}}},
	}
}

// ensureCollection creates the org's collection on first use with the configured
// dims + Cosine distance, and indexes the payload keys it filters on. Idempotent
// (a 200/409 both mean "exists"). If the collection already exists with a DIFFERENT
// vector size, it refuses to proceed (an error the caller logs) rather than
// upserting a wrong-dimension vector that Qdrant would reject or that would corrupt
// recall — the operator must not change KB_EMBED_DIMS under a live collection.
func (x *indexer) ensureCollection(ctx context.Context, org string) error {
	x.mu.Lock()
	ok := x.ensuredOrgs[org]
	x.mu.Unlock()
	if ok {
		return nil
	}
	col := x.collection(org)

	// Probe: if it exists, verify the dimension matches before caching.
	var info struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size int `json:"size"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}
	err := x.qdrant(ctx, http.MethodGet, "/collections/"+col, nil, &info)
	switch {
	case err == nil:
		if sz := info.Result.Config.Params.Vectors.Size; sz != 0 && sz != x.dims {
			return fmt.Errorf("collection %q has dim %d, want %d (KB_EMBED_DIMS changed under a live collection)", col, sz, x.dims)
		}
	case isNotFound(err):
		create := map[string]any{
			"vectors": map[string]any{"size": x.dims, "distance": "Cosine"},
		}
		if e := x.qdrant(ctx, http.MethodPut, "/collections/"+col, create, nil); e != nil && !isConflict(e) {
			return e
		}
		// Index the payload keys we filter on (org/doctype/project/provider) so the
		// filter is efficient. Best-effort — a missing index only slows a filter.
		for _, key := range []string{"org", "doctype", "project", "provider", "owner"} {
			_ = x.qdrant(ctx, http.MethodPut, "/collections/"+col+"/index?wait=true",
				map[string]any{"field_name": key, "field_schema": "keyword"}, nil)
		}
	default:
		return err
	}

	x.mu.Lock()
	x.ensuredOrgs[org] = true
	x.mu.Unlock()
	return nil
}

// embed returns the float32 vector for a single input through the shared
// cloud.AIClient (deps.AI) — the SAME org/project-aligned, metered, observed path
// chat and clients/code run through. There is no embed-specific key or endpoint.
// An empty vector is an error (the caller decides whether that fails open at
// index time or empty at query time).
func (x *indexer) embed(ctx context.Context, org, project, text string) ([]float32, error) {
	if x.ai == nil {
		return nil, fmt.Errorf("kb: embeddings disabled (no AI client)")
	}
	vecs, err := x.ai.Embed(ctx, &cloud.EmbedRequest{Model: x.embedModel, Inputs: []string{text}, Org: org, Project: project})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("kb: embeddings: empty vector")
	}
	return vecs[0], nil
}

// qdrant performs one Qdrant REST call. body is JSON-encoded when non-nil; when out
// is non-nil the 2xx response is decoded into it. The api-key header is set only
// when configured (in-cluster Qdrant is typically unauthenticated). Sentinel status
// codes surface as errNotFound/errConflict so ensureCollection can branch.
func (x *indexer) qdrant(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, x.vectorURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if x.vectorKey != "" {
		req.Header.Set("api-key", x.vectorKey)
	}
	resp, err := x.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusConflict:
		return errConflict
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("qdrant %s %s: status %d: %s", method, path, resp.StatusCode, truncate(raw, 200))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode qdrant response: %w", err)
		}
	}
	return nil
}
