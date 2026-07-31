// Package product exposes the read-only Search and Vector product surfaces
// the Hanzo console panels call at api.cloud.hanzo.ai, per HIP-0106.
//
// The console's Search/Indexes and Vector panels call
// https://api.hanzo.ai/v1/search-docs/* and /v1/vector/* with a
// bearer key (HANZO_SEARCH_API_KEY / HANZO_VECTOR_API_KEY). cloud-api is the
// single edge that owns those paths: this subsystem proxies them to the
// in-cluster Meilisearch (search.hanzo.svc) and Qdrant (vector.hanzo.svc)
// services and translates each upstream response into the exact JSON shape the
// console's tRPC routers decode. No search/vector logic is reimplemented — this
// is a shape-translating proxy.
//
// Auth: the gateway middleware (order 80) bypasses these paths via
// AUTH_PUBLIC_PATHS (the bearer is an opaque service key, not a JWT). This
// subsystem enforces the key itself with a constant-time compare against the
// configured upstream master key, so the endpoints are never open.
//
// Module boundary: search lives in the Meilisearch service, vectors in Qdrant.
// This wrapper is glue; it holds no index/collection state.
package product

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// config is resolved once from env at Mount. Endpoints default to the
// in-cluster service DNS; keys come from the search/vector secrets already
// mounted on the cloud-api deployment.
type config struct {
	searchURL string
	searchKey string
	vectorURL string
	vectorKey string
}

func loadConfig() config {
	return config{
		searchURL: getenv("searchEndpoint", "http://search.hanzo.svc.cluster.local:7700"),
		searchKey: os.Getenv("searchApiKey"),
		vectorURL: getenv("vectorEndpoint", "http://vector.hanzo.svc.cluster.local:6333"),
		vectorKey: os.Getenv("vectorApiKey"),
	}
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// ops binds the resolved upstream config and the subsystem logger to the typed
// handlers. A TypedHandler takes only (context, *In), so everything else arrives
// as a RECEIVER.
type ops struct {
	cfg config
	log luxlog.Logger
}

// Mount registers the product (search + vector) read surface on app per
// HIP-0106. Read-only: every panel degrades to an honest empty state in the
// console when this surface is unreachable, so these handlers prefer returning
// an empty-but-valid body over a 5xx whenever the upstream hiccups.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("product.Mount: nil app")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("product.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "product")
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("product.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{cfg: loadConfig(), log: logger}

	// The bridge FIRST — a typed op is handed only a context, and these routes
	// authorize on the request's own bearer, which is parked there. Bounded to
	// product's declared prefixes; Serve installs one app-wide too and nesting is
	// harmless.
	app.Use(cloud.Bridge())

	// ── Search (Meilisearch-backed) ───────────────────────────────────
	zip.Get(z, "/v1/search-docs/indexes", o.searchIndexes, zip.WithOperationID("searchIndexes"))
	zip.Get(z, "/v1/search-docs/stats", o.searchStats, zip.WithOperationID("searchStats"))
	// ── Vector (Qdrant-backed) ────────────────────────────────────────
	zip.Get(z, "/v1/vector/collections", o.vectorCollections, zip.WithOperationID("vectorCollections"))
	zip.Get(z, "/v1/vector/stats", o.vectorStats, zip.WithOperationID("vectorStats"))

	logger.Info("product surface mounted",
		"search", o.cfg.searchURL, "vector", o.cfg.vectorURL,
		"searchKey", o.cfg.searchKey != "", "vectorKey", o.cfg.vectorKey != "")
	return nil
}

// searchIndexes lists every Meilisearch index with its document count. Creation
// and last-indexed timestamps ride along. An unreachable upstream reads as an empty
// index list, never a 5xx, so the console panel degrades to an honest empty state.
//
// Response: {"indexes": [{"name": "docs", "docCount": 4210, "lastIndexedAt": "2026-07-02T09:31:00Z", "createdAt": "2026-01-08T17:04:11Z"}]}
func (o ops) searchIndexes(ctx context.Context, _ *struct{}) (*SearchIndexes, error) {
	if err := authorize(ctx, o.cfg.searchKey); err != nil {
		return nil, err
	}
	stats, err := meiliStats(o.cfg)
	if err != nil {
		o.log.Warn("search indexes: meili stats unreachable", "err", err)
		return &SearchIndexes{Indexes: []SearchIndex{}}, nil
	}
	created := meiliIndexCreatedAt(o.cfg) // best-effort; may be empty
	out := make([]SearchIndex, 0, len(stats.Indexes))
	for name, ix := range stats.Indexes {
		ts := created[name]
		out = append(out, SearchIndex{
			Name:          name,
			DocCount:      ix.NumberOfDocuments,
			LastIndexedAt: nullableTS(ts.updatedAt),
			CreatedAt:     orNow(ts.createdAt),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return &SearchIndexes{Indexes: out}, nil
}

// searchStats totals the documents across every Meilisearch index. Query counters
// are reported as an honest zero: Meilisearch keeps no query history, so searches,
// sessions and the per-day series are not derivable from the index.
//
// Response: {"totalDocuments": 4210, "totalSearches": 0, "totalSessions": 0, "searchesPerDay": []}
func (o ops) searchStats(ctx context.Context, _ *struct{}) (*SearchStats, error) {
	if err := authorize(ctx, o.cfg.searchKey); err != nil {
		return nil, err
	}
	stats, err := meiliStats(o.cfg)
	if err != nil {
		o.log.Warn("search stats: meili unreachable", "err", err)
		return &SearchStats{SearchesPerDay: []DayCount{}}, nil
	}
	var total int64
	for _, ix := range stats.Indexes {
		total += ix.NumberOfDocuments
	}
	return &SearchStats{
		TotalDocuments: total,
		// Meilisearch keeps no query-history counters; sessions/searches and
		// the per-day series are not derivable from the index. Report the
		// honest zero/empty rather than a fabricated number.
		TotalSearches:  0,
		TotalSessions:  0,
		SearchesPerDay: []DayCount{},
	}, nil
}

// vectorCollections lists every Qdrant collection with its vector count. Dimension
// and distance metric ride along. An unreachable upstream reads as an empty
// collection list.
//
// Response: {"collections": [{"name": "docs", "vectorCount": 128000, "dimension": 1536, "distanceMetric": "Cosine", "createdAt": ""}]}
func (o ops) vectorCollections(ctx context.Context, _ *struct{}) (*VectorCollections, error) {
	if err := authorize(ctx, o.cfg.vectorKey); err != nil {
		return nil, err
	}
	cols, err := qdrantCollections(o.cfg)
	if err != nil {
		o.log.Warn("vector collections: qdrant unreachable", "err", err)
		return &VectorCollections{Collections: []VectorCollection{}}, nil
	}
	return &VectorCollections{Collections: cols}, nil
}

// vectorStats sums the Qdrant collections into three headline totals. Those are
// collections, vectors and stored bytes. An unreachable upstream reads as zeros.
//
// Response: {"totalCollections": 3, "totalVectors": 128000, "totalStorageBytes": 91750400}
func (o ops) vectorStats(ctx context.Context, _ *struct{}) (*VectorStats, error) {
	if err := authorize(ctx, o.cfg.vectorKey); err != nil {
		return nil, err
	}
	cols, err := qdrantCollections(o.cfg)
	if err != nil {
		o.log.Warn("vector stats: qdrant unreachable", "err", err)
		return &VectorStats{}, nil
	}
	var vectors, storage int64
	for _, col := range cols {
		vectors += col.VectorCount
		storage += col.StorageBytes
	}
	return &VectorStats{
		TotalCollections:  int64(len(cols)),
		TotalVectors:      vectors,
		TotalStorageBytes: storage,
	}, nil
}

// authorize enforces the bearer key against the configured upstream key with a
// constant-time compare. It returns a *zip.HTTPError (which zip's errorHandler
// renders as a JSON body with the right status) on rejection and writes
// nothing itself, so the handler simply `return`s the error — no double-write.
// An unset key fails closed (503) so a mis-provisioned deploy never silently
// serves an open endpoint, and so does a call arriving off the HTTP path (the
// CLI/MCP projections carry no bearer to compare).
func authorize(ctx context.Context, want string) error {
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "product surface not configured")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return zip.ErrUnauthorized("invalid api key")
	}
	got := bearer(c.Header("Authorization"))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return zip.ErrUnauthorized("invalid api key")
	}
	return nil
}

func bearer(h string) string {
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return h
}

// ── console response shapes (must match web/src/features/{search,vector}/types.ts) ──

// SearchIndex is one Meilisearch index as the console's Indexes panel reads it.
type SearchIndex struct {
	// Name is the index uid.
	Name string `json:"name"`
	// DocCount is how many documents the index currently holds.
	DocCount int64 `json:"docCount"`
	// LastIndexedAt is the index's last update time, null when the upstream did
	// not report one.
	LastIndexedAt *string `json:"lastIndexedAt"`
	// CreatedAt is the index creation time, now() when the upstream did not
	// report one.
	CreatedAt string `json:"createdAt"`
}

// SearchIndexes is the Indexes panel's payload.
type SearchIndexes struct {
	// Indexes is every index, sorted by name. Empty when the upstream is
	// unreachable.
	Indexes []SearchIndex `json:"indexes"`
}

// DayCount is one bucket of a per-day series.
type DayCount struct {
	// Date is the bucket day.
	Date string `json:"date"`
	// Count is the number of searches in that day.
	Count int64 `json:"count"`
}

// SearchStats is the Search panel's headline roll-up.
type SearchStats struct {
	// TotalDocuments is the summed document count across every index.
	TotalDocuments int64 `json:"totalDocuments"`
	// TotalSearches is always 0 — Meilisearch keeps no query-history counter.
	TotalSearches int64 `json:"totalSearches"`
	// TotalSessions is always 0, for the same reason as TotalSearches.
	TotalSessions int64 `json:"totalSessions"`
	// SearchesPerDay is always empty — the series is not derivable from the index.
	SearchesPerDay []DayCount `json:"searchesPerDay"`
}

// VectorCollection is one Qdrant collection as the console's Vector panel reads it.
type VectorCollection struct {
	// Name is the collection name.
	Name string `json:"name"`
	// VectorCount is the collection's point count.
	VectorCount int64 `json:"vectorCount"`
	// Dimension is the configured vector size.
	Dimension int64 `json:"dimension"`
	// DistanceMetric is the configured distance, "cosine" when the upstream did
	// not report one.
	DistanceMetric string `json:"distanceMetric"`
	// StorageBytes is the collection's on-disk size when the upstream reports it.
	StorageBytes int64 `json:"storageBytes,omitempty"`
	// CreatedAt is the collection creation time when the upstream reports it.
	CreatedAt string `json:"createdAt"`
}

// VectorCollections is the Vector panel's collection list.
type VectorCollections struct {
	// Collections is every collection, sorted by name. Empty when the upstream is
	// unreachable.
	Collections []VectorCollection `json:"collections"`
}

// VectorStats is the Vector panel's headline roll-up.
type VectorStats struct {
	// TotalCollections is how many collections exist.
	TotalCollections int64 `json:"totalCollections"`
	// TotalVectors is the summed point count across every collection.
	TotalVectors int64 `json:"totalVectors"`
	// TotalStorageBytes is the summed on-disk size across every collection.
	TotalStorageBytes int64 `json:"totalStorageBytes"`
}

// ── Meilisearch upstream ──────────────────────────────────────────────

type meiliStatsResp struct {
	DatabaseSize int64  `json:"databaseSize"`
	LastUpdate   string `json:"lastUpdate"`
	Indexes      map[string]struct {
		NumberOfDocuments int64 `json:"numberOfDocuments"`
		RawDocumentDbSize int64 `json:"rawDocumentDbSize"`
		IsIndexing        bool  `json:"isIndexing"`
	} `json:"indexes"`
}

func meiliStats(cfg config) (*meiliStatsResp, error) {
	var out meiliStatsResp
	if err := getJSON(cfg.searchURL+"/stats", "Authorization", "Bearer "+cfg.searchKey, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type indexTimes struct{ createdAt, updatedAt string }

// meiliIndexCreatedAt fetches the index list for createdAt/updatedAt. Best
// effort: if it fails the caller falls back to now() / null.
func meiliIndexCreatedAt(cfg config) map[string]indexTimes {
	out := map[string]indexTimes{}
	var resp struct {
		Results []struct {
			UID       string `json:"uid"`
			CreatedAt string `json:"createdAt"`
			UpdatedAt string `json:"updatedAt"`
		} `json:"results"`
	}
	if err := getJSON(cfg.searchURL+"/indexes?limit=1000", "Authorization", "Bearer "+cfg.searchKey, &resp); err != nil {
		return out
	}
	for _, r := range resp.Results {
		out[r.UID] = indexTimes{createdAt: r.CreatedAt, updatedAt: r.UpdatedAt}
	}
	return out
}

// ── Qdrant upstream ───────────────────────────────────────────────────

func qdrantCollections(cfg config) ([]VectorCollection, error) {
	var list struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := getJSON(cfg.vectorURL+"/collections", "api-key", cfg.vectorKey, &list); err != nil {
		return nil, err
	}
	out := make([]VectorCollection, 0, len(list.Result.Collections))
	for _, c := range list.Result.Collections {
		col := VectorCollection{Name: c.Name, DistanceMetric: "cosine"}
		// Per-collection detail carries dimension/distance/point-count. Best
		// effort: a single missing collection should not blank the whole panel.
		var info struct {
			Result struct {
				PointsCount int64 `json:"points_count"`
				Config      struct {
					Params struct {
						Vectors struct {
							Size     int64  `json:"size"`
							Distance string `json:"distance"`
						} `json:"vectors"`
					} `json:"params"`
				} `json:"config"`
			} `json:"result"`
		}
		if err := getJSON(cfg.vectorURL+"/collections/"+c.Name, "api-key", cfg.vectorKey, &info); err == nil {
			col.VectorCount = info.Result.PointsCount
			col.Dimension = info.Result.Config.Params.Vectors.Size
			if d := info.Result.Config.Params.Vectors.Distance; d != "" {
				col.DistanceMetric = d
			}
		}
		out = append(out, col)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ── http helper ───────────────────────────────────────────────────────

func getJSON(url, hdrKey, hdrVal string, into any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if hdrKey != "" {
		req.Header.Set(hdrKey, hdrVal)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d: %s", url, resp.StatusCode, truncate(body, 200))
	}
	return json.Unmarshal(body, into)
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

func nullableTS(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func orNow(s string) string {
	if s == "" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return s
}
