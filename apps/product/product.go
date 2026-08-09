// Package product is the read-only inventory of the search and vector backends:
// /v1/search/{indexes,stats} read from Meilisearch and
// /v1/vector/{collections,stats} from Qdrant, reshaped into the rows the console
// renders.
//
// The console's Search/Indexes and Vector panels call
// https://api.hanzo.ai/v1/search/* and /v1/vector/* with a
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
	"github.com/hanzoai/cloud/internal/environ"
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
		searchURL: environ.Or("searchEndpoint", "http://search.hanzo.svc.cluster.local:7700"),
		searchKey: os.Getenv("searchApiKey"),
		vectorURL: environ.Or("vectorEndpoint", "http://vector.hanzo.svc.cluster.local:6333"),
		vectorKey: os.Getenv("vectorApiKey"),
	}
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

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
	cfg := loadConfig()
	o := productOps{cfg: cfg, log: logger}

	// The bearer key is a REQUEST fact, so each op declares it: keyedIn carries
	// the Authorization header as a typed input field, and requireKey opens every
	// handler — an unconfigured deployment still 503s and a wrong key still 401s
	// before anything reaches an upstream. Two keys, never crossed: the search
	// bearer never admits a vector read.
	//
	// NOT middleware. /v1/search and /v1/vector belong to provisioning
	// (manifest/apps.go); product owns exactly four routes inside them, and a key
	// check hung on either subtree would have gated provisioning's routes in the
	// unified binary — the confinement gate refused that boot, and it was right
	// to. Declaring the header on In is zip's replacement for exactly that
	// middleware: the credential appears in the document, the CLI flag and the
	// MCP schema, instead of being smuggled past every projection.
	sg := app.Group("/v1/search")
	zip.Get(sg, "/indexes", o.searchIndexes)
	zip.Get(sg, "/stats", o.searchStats)

	vg := app.Group("/v1/vector")
	zip.Get(vg, "/collections", o.vectorCollections)
	zip.Get(vg, "/stats", o.vectorStats)

	logger.Info("product surface mounted",
		"search", cfg.searchURL, "vector", cfg.vectorURL,
		"searchKey", cfg.searchKey != "", "vectorKey", cfg.vectorKey != "")
	return nil
}

// requireKey enforces the bearer key against the configured upstream key with a
// constant-time compare. It is the first call of every handler and returns a
// *zip.HTTPError (which zip's errorHandler renders as a JSON body with the
// right status) on rejection. An unset key fails closed (503) so a
// mis-provisioned deploy never silently serves an open endpoint.
func requireKey(auth, want string) error {
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "product surface not configured")
	}
	if subtle.ConstantTimeCompare([]byte(bearer(auth)), []byte(want)) != 1 {
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

// ── typed ops ────────────────────────────────────────────────────────────────

// productOps binds the resolved upstream config to the typed product ops. A
// TypedHandler takes no service parameter, so the config arrives as a RECEIVER
// and every op is a method value — also the only bound form cmd/zipdoc can lift
// prose from.
type productOps struct {
	cfg config
	log luxlog.Logger
}

// keyedIn is the input of every product read: no body, no path or query
// parameter — just the bearer that authorizes it. The header is a typed input
// field because that is how a typed op sees a request header, and it puts the
// credential in the op's contract: a header parameter in the document, a flag
// on the command, a property in the MCP tool schema.
type keyedIn struct {
	// Authorization carries the surface's bearer key (`Bearer <key>`); the bare
	// key is accepted too. Search and vector are two surfaces with two keys.
	// It is not `validate:"required"` on purpose: requireKey answers absence
	// itself, so an unconfigured surface 503s and a missing bearer 401s — a
	// validation refusal would rewrite both statuses.
	Authorization string `json:"authorization" header:"Authorization"`
}

// searchIndexList is the GET /v1/search/indexes envelope.
type searchIndexList struct {
	// Indexes is one row per Meilisearch index, sorted by name. Empty — never
	// absent — when the search service cannot be reached.
	Indexes []searchIndex `json:"indexes"`
}

// vectorCollectionList is the GET /v1/vector/collections envelope.
type vectorCollectionList struct {
	// Collections is one row per Qdrant collection, sorted by name. Empty — never
	// absent — when the vector service cannot be reached.
	Collections []vectorCollection `json:"collections"`
}

// searchIndexes lists the search indexes with their document counts and timestamps.
//
// It reads the in-cluster Meilisearch service and reshapes its /stats and
// /indexes replies into the rows the console's Search panel renders. The read is
// degrade-friendly by design: an unreachable Meilisearch answers 200 with an
// EMPTY list, so the panel shows an honest empty state instead of an error.
// createdAt falls back to now and lastIndexedAt to null when the index list is
// unavailable.
func (o productOps) searchIndexes(ctx context.Context, in *keyedIn) (*searchIndexList, error) {
	if err := requireKey(in.Authorization, o.cfg.searchKey); err != nil {
		return nil, err
	}
	stats, err := meiliStats(o.cfg)
	if err != nil {
		o.log.Warn("search indexes: meili stats unreachable", "err", err)
		return &searchIndexList{Indexes: []searchIndex{}}, nil
	}
	created := meiliIndexCreatedAt(o.cfg) // best-effort; may be empty
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
	return &searchIndexList{Indexes: out}, nil
}

// searchStats totals the documents across every search index.
//
// totalDocuments is summed from Meilisearch's own per-index counts. The other
// three fields are structurally zero rather than estimated: Meilisearch keeps no
// query-history counters, so searches, sessions and the per-day series are not
// derivable from the index and this surface reports the honest zero instead of a
// fabricated number. An unreachable Meilisearch answers 200 with all zeros.
func (o productOps) searchStats(ctx context.Context, in *keyedIn) (*searchStats, error) {
	if err := requireKey(in.Authorization, o.cfg.searchKey); err != nil {
		return nil, err
	}
	stats, err := meiliStats(o.cfg)
	if err != nil {
		o.log.Warn("search stats: meili unreachable", "err", err)
		return &searchStats{SearchesPerDay: []dayCount{}}, nil
	}
	var total int64
	for _, ix := range stats.Indexes {
		total += ix.NumberOfDocuments
	}
	return &searchStats{
		TotalDocuments: total,
		TotalSearches:  0,
		TotalSessions:  0,
		SearchesPerDay: []dayCount{},
	}, nil
}

// vectorCollections lists the vector collections with their size and geometry.
//
// It reads the in-cluster Qdrant service: the collection list, then each
// collection's detail for its point count, vector dimension and distance metric.
// Per-collection detail is best-effort — one collection that fails to describe
// itself keeps its name and defaults (dimension 0, cosine) rather than blanking
// the whole panel — and an unreachable Qdrant answers 200 with an EMPTY list.
func (o productOps) vectorCollections(ctx context.Context, in *keyedIn) (*vectorCollectionList, error) {
	if err := requireKey(in.Authorization, o.cfg.vectorKey); err != nil {
		return nil, err
	}
	cols, err := qdrantCollections(o.cfg)
	if err != nil {
		o.log.Warn("vector collections: qdrant unreachable", "err", err)
		return &vectorCollectionList{Collections: []vectorCollection{}}, nil
	}
	return &vectorCollectionList{Collections: cols}, nil
}

// vectorStats totals the collections, vectors and storage across the vector store.
//
// Every figure is summed from the same per-collection detail
// GET /v1/vector/collections returns, so the two panels can never disagree. An
// unreachable Qdrant answers 200 with all zeros rather than an error.
func (o productOps) vectorStats(ctx context.Context, in *keyedIn) (*vectorStats, error) {
	if err := requireKey(in.Authorization, o.cfg.vectorKey); err != nil {
		return nil, err
	}
	cols, err := qdrantCollections(o.cfg)
	if err != nil {
		o.log.Warn("vector stats: qdrant unreachable", "err", err)
		return &vectorStats{}, nil
	}
	var vectors, storage int64
	for _, col := range cols {
		vectors += col.VectorCount
		storage += col.StorageBytes
	}
	return &vectorStats{
		TotalCollections:  int64(len(cols)),
		TotalVectors:      vectors,
		TotalStorageBytes: storage,
	}, nil
}

// ── console response shapes (must match web/src/features/{search,vector}/types.ts) ──

// searchIndex is one Meilisearch index as the console's Search panel reads it.
type searchIndex struct {
	// Name is the index uid.
	Name string `json:"name"`
	// DocCount is how many documents the index currently holds.
	DocCount int64 `json:"docCount"`
	// LastIndexedAt is the index's last update time (RFC 3339), null when the
	// index list could not be read.
	LastIndexedAt *string `json:"lastIndexedAt"`
	// CreatedAt is the index's creation time (RFC 3339); it falls back to now when
	// the index list could not be read.
	CreatedAt string `json:"createdAt"`
}

// dayCount is one day of a per-day series.
type dayCount struct {
	// Date is the day, YYYY-MM-DD.
	Date string `json:"date"`
	// Count is that day's total.
	Count int64 `json:"count"`
}

// searchStats is the search totals the console's Search panel renders.
type searchStats struct {
	// TotalDocuments is the sum of every index's document count.
	TotalDocuments int64 `json:"totalDocuments"`
	// TotalSearches is always 0: Meilisearch keeps no query-history counter, so
	// this surface reports the honest zero rather than an estimate.
	TotalSearches int64 `json:"totalSearches"`
	// TotalSessions is always 0, for the same reason as totalSearches.
	TotalSessions int64 `json:"totalSessions"`
	// SearchesPerDay is always empty, for the same reason as totalSearches.
	SearchesPerDay []dayCount `json:"searchesPerDay"`
}

// vectorCollection is one Qdrant collection as the console's Vector panel reads it.
type vectorCollection struct {
	// Name is the collection name.
	Name string `json:"name"`
	// VectorCount is the collection's point count.
	VectorCount int64 `json:"vectorCount"`
	// Dimension is the size of one vector in the collection.
	Dimension int64 `json:"dimension"`
	// DistanceMetric is the collection's distance function; "cosine" when the
	// collection's detail could not be read.
	DistanceMetric string `json:"distanceMetric"`
	// StorageBytes is the collection's on-disk size, omitted when unknown.
	StorageBytes int64 `json:"storageBytes,omitempty"`
	// CreatedAt is the collection's creation time (RFC 3339); Qdrant does not
	// report one, so it is empty today.
	CreatedAt string `json:"createdAt"`
}

// vectorStats is the vector-store totals the console's Vector panel renders.
type vectorStats struct {
	// TotalCollections is how many collections the store holds.
	TotalCollections int64 `json:"totalCollections"`
	// TotalVectors is the sum of every collection's point count.
	TotalVectors int64 `json:"totalVectors"`
	// TotalStorageBytes is the sum of every collection's on-disk size.
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
