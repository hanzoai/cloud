// Package productsvc exposes the read-only Search and Vector product surfaces
// the Hanzo console panels call at api.cloud.hanzo.ai, per HIP-0106.
//
// The console's Search/Indexes and Vector panels are hardcoded to call
// https://api.cloud.hanzo.ai/api/search-docs/* and /api/vector/* with a
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
package productsvc

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
)

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

// Mount registers the product (search + vector) read surface on app per
// HIP-0106. Read-only: every panel degrades to an honest empty state in the
// console when this surface is unreachable, so these handlers prefer returning
// an empty-but-valid body over a 5xx whenever the upstream hiccups.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("productsvc.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("productsvc.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "product")
	cfg := loadConfig()

	// ── Search (Meilisearch-backed) ───────────────────────────────────
	app.Get("/api/search-docs/indexes", func(c *zip.Ctx) error {
		if err := authorize(c, cfg.searchKey); err != nil {
			return err
		}
		stats, err := meiliStats(cfg)
		if err != nil {
			logger.Warn("search indexes: meili stats unreachable", "err", err)
			return c.JSON(http.StatusOK, map[string]any{"indexes": []searchIndex{}})
		}
		created := meiliIndexCreatedAt(cfg) // best-effort; may be empty
		out := make([]searchIndex, 0, len(stats.Indexes))
		for name, ix := range stats.Indexes {
			ts := created[name]
			out = append(out, searchIndex{
				Name:          name,
				DocCount:      ix.NumberOfDocuments,
				LastIndexedAt: nullableTS(ts.updatedAt),
				CreatedAt:     orNow(ts.createdAt),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return c.JSON(http.StatusOK, map[string]any{"indexes": out})
	})

	app.Get("/api/search-docs/stats", func(c *zip.Ctx) error {
		if err := authorize(c, cfg.searchKey); err != nil {
			return err
		}
		stats, err := meiliStats(cfg)
		if err != nil {
			logger.Warn("search stats: meili unreachable", "err", err)
			return c.JSON(http.StatusOK, searchStats{SearchesPerDay: []dayCount{}})
		}
		var total int64
		for _, ix := range stats.Indexes {
			total += ix.NumberOfDocuments
		}
		return c.JSON(http.StatusOK, searchStats{
			TotalDocuments: total,
			// Meilisearch keeps no query-history counters; sessions/searches and
			// the per-day series are not derivable from the index. Report the
			// honest zero/empty rather than a fabricated number.
			TotalSearches:  0,
			TotalSessions:  0,
			SearchesPerDay: []dayCount{},
		})
	})

	// ── Vector (Qdrant-backed) ────────────────────────────────────────
	app.Get("/api/vector/collections", func(c *zip.Ctx) error {
		if err := authorize(c, cfg.vectorKey); err != nil {
			return err
		}
		cols, err := qdrantCollections(cfg)
		if err != nil {
			logger.Warn("vector collections: qdrant unreachable", "err", err)
			return c.JSON(http.StatusOK, map[string]any{"collections": []vectorCollection{}})
		}
		return c.JSON(http.StatusOK, map[string]any{"collections": cols})
	})

	app.Get("/api/vector/stats", func(c *zip.Ctx) error {
		if err := authorize(c, cfg.vectorKey); err != nil {
			return err
		}
		cols, err := qdrantCollections(cfg)
		if err != nil {
			logger.Warn("vector stats: qdrant unreachable", "err", err)
			return c.JSON(http.StatusOK, vectorStats{})
		}
		var vectors, storage int64
		for _, col := range cols {
			vectors += col.VectorCount
			storage += col.StorageBytes
		}
		return c.JSON(http.StatusOK, vectorStats{
			TotalCollections: int64(len(cols)),
			TotalVectors:     vectors,
			TotalStorageBytes: storage,
		})
	})

	logger.Info("product surface mounted",
		"search", cfg.searchURL, "vector", cfg.vectorURL,
		"searchKey", cfg.searchKey != "", "vectorKey", cfg.vectorKey != "")
	return nil
}

// authorize enforces the bearer key against the configured upstream key with a
// constant-time compare. It returns a *zip.HTTPError (which zip's errorHandler
// renders as a JSON body with the right status) on rejection and writes
// nothing itself, so the handler simply `return`s the error — no double-write.
// An unset key fails closed (503) so a mis-provisioned deploy never silently
// serves an open endpoint.
func authorize(c *zip.Ctx, want string) error {
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "product surface not configured")
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

type searchIndex struct {
	Name          string  `json:"name"`
	DocCount      int64   `json:"docCount"`
	LastIndexedAt *string `json:"lastIndexedAt"`
	CreatedAt     string  `json:"createdAt"`
}

type dayCount struct {
	Date  string `json:"date"`
	Count int64  `json:"count"`
}

type searchStats struct {
	TotalDocuments int64      `json:"totalDocuments"`
	TotalSearches  int64      `json:"totalSearches"`
	TotalSessions  int64      `json:"totalSessions"`
	SearchesPerDay []dayCount `json:"searchesPerDay"`
}

type vectorCollection struct {
	Name           string `json:"name"`
	VectorCount    int64  `json:"vectorCount"`
	Dimension      int64  `json:"dimension"`
	DistanceMetric string `json:"distanceMetric"`
	StorageBytes   int64  `json:"storageBytes,omitempty"`
	CreatedAt      string `json:"createdAt"`
}

type vectorStats struct {
	TotalCollections  int64 `json:"totalCollections"`
	TotalVectors      int64 `json:"totalVectors"`
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

func qdrantCollections(cfg config) ([]vectorCollection, error) {
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
	out := make([]vectorCollection, 0, len(list.Result.Collections))
	for _, c := range list.Result.Collections {
		col := vectorCollection{Name: c.Name, DistanceMetric: "cosine"}
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

func init() {
	cloud.Register("product", 145, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("productsvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
