// inventory.go is the read-only inventory of the lexical backend: which indexes
// the deployment's Meilisearch holds and what they add up to.
//
// It came from apps/product, which answered these two addresses plus two more
// over Qdrant while owning no store of its own. An app with no store has no
// boundary to split along (HIP-0139 §7.2), so its four operations went to the two
// capabilities that own the roots they already sat under: /v1/search is this
// one's, so /v1/search/{indexes,stats} are too, and the vector pair went to
// provisioning, which manages that backend. THE ADDRESSES DID NOT MOVE. Only the
// owner did.
//
// AUTH IS THE UPSTREAM'S OWN KEY, not a tenant's. These reads span the whole
// deployment's index set, and the console's Search panel calls them with
// HANZO_SEARCH_API_KEY; the gateway bypasses these paths (AUTH_PUBLIC_PATHS)
// because that bearer is an opaque service key and not a JWT. So the key is
// checked here, constant-time, and an unconfigured deployment fails closed at 503
// rather than serving an open endpoint.
//
// It answers 200-with-nothing when Meilisearch is unreachable, which is the
// opposite of what Query does one file over, and the difference is the caller.
// Query's caller ACTS on the result, so a silent empty there reads as "no such
// document" and hid a credential drift for five days; the panel here renders an
// inventory, where an error banner and an empty table say the same thing to the
// person reading it. The operator is told either way — every miss is logged.
package search

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

	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/zap-proto/zip"
)

// config is resolved once from env at Mount. The endpoint defaults to the
// in-cluster service DNS; the key comes from the search secret already mounted on
// the cloud-api deployment.
type config struct {
	url string
	key string
}

func loadConfig() config {
	return config{
		url: environ.Or("searchEndpoint", "http://search.hanzo.svc.cluster.local:7700"),
		key: os.Getenv("searchApiKey"),
	}
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// inventory binds the resolved upstream config to the two inventory ops. A
// TypedHandler takes no service parameter, so the config arrives as a RECEIVER
// and every op is a method value — also the only bound form cmd/zipdoc can lift
// prose from.
type inventory struct{ cfg config }

// mountInventory registers the two upstream reads. Called from Mount.
//
// THEY ARE AT THE OPERATOR'S DEPTH, and they used to sit at /v1/search/indexes
// and /v1/search/stats, which made one prefix mean two unrelated things: the
// tenant's fused query over its OWN corpora, and an operator's inventory of ONE
// shared backend deployment. A caller reading /v1/search/indexes has every reason
// to expect "the corpora my search covers" and gets the index list of a
// Meilisearch that answers only part of one leg.
//
// /v1/admin is the operator family — openapi.Product drops it from the public
// contract by ADDRESS, so the split needs no flag — and apps/provisioning already
// put its shared-backend reads at /v1/admin/provisioning/vector/* for exactly this
// reason. Two literal segments also stop squatting where a per-corpus search would
// address, which is what kept /v1/search from being one prefix with one meaning.
func mountInventory(z *zip.App) {
	o := inventory{cfg: loadConfig()}
	zip.Get(z, "/v1/admin/search/indexes", o.searchIndexes)
	zip.Get(z, "/v1/admin/search/stats", o.searchStats)
}

// keyedIn is the input of both reads: no body, no path or query parameter — just
// the bearer that authorizes it. The header is a typed input field because that
// is how a typed op sees a request header, and it puts the credential in the op's
// contract: a header parameter in the document, a flag on the command, a property
// in the MCP tool schema.
type keyedIn struct {
	// Authorization carries the surface's bearer key (`Bearer <key>`); the bare
	// key is accepted too. It is not `validate:"required"` on purpose: requireKey
	// answers absence itself, so an unconfigured surface 503s and a missing bearer
	// 401s — a validation refusal would rewrite both statuses.
	Authorization string `json:"authorization" header:"Authorization"`
}

// requireKey enforces the bearer against the configured upstream key with a
// constant-time compare. It is the first call of both handlers and returns a
// *zip.HTTPError (which zip's errorHandler renders as a JSON body with the right
// status) on rejection. An unset key fails closed (503) so a mis-provisioned
// deploy never silently serves an open endpoint.
func requireKey(auth, want string) error {
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "search inventory not configured")
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

// searchIndexList is the GET /v1/admin/search/indexes envelope.
type searchIndexList struct {
	// Indexes is one row per Meilisearch index, sorted by name. Empty — never
	// absent — when the search service cannot be reached.
	Indexes []searchIndex `json:"indexes"`
}

// searchIndexes lists the search indexes with their document counts and timestamps.
//
// It reads the in-cluster Meilisearch service and reshapes its /stats and
// /indexes replies into the rows the console's Search panel renders. The read is
// degrade-friendly by design: an unreachable Meilisearch answers 200 with an
// EMPTY list, so the panel shows an honest empty state instead of an error.
// createdAt falls back to now and lastIndexedAt to null when the index list is
// unavailable.
func (o inventory) searchIndexes(ctx context.Context, in *keyedIn) (*searchIndexList, error) {
	if err := requireKey(in.Authorization, o.cfg.key); err != nil {
		return nil, err
	}
	stats, err := meiliStats(o.cfg)
	if err != nil {
		log.Warn("search indexes: meili stats unreachable", "err", err)
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
func (o inventory) searchStats(ctx context.Context, in *keyedIn) (*searchStats, error) {
	if err := requireKey(in.Authorization, o.cfg.key); err != nil {
		return nil, err
	}
	stats, err := meiliStats(o.cfg)
	if err != nil {
		log.Warn("search stats: meili unreachable", "err", err)
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

// ── console response shapes (must match web/src/features/search/types.ts) ──

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
	if err := getJSON(cfg.url+"/stats", "Bearer "+cfg.key, &out); err != nil {
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
	if err := getJSON(cfg.url+"/indexes?limit=1000", "Bearer "+cfg.key, &resp); err != nil {
		return out
	}
	for _, r := range resp.Results {
		out[r.UID] = indexTimes{createdAt: r.CreatedAt, updatedAt: r.UpdatedAt}
	}
	return out
}

// ── http helper ───────────────────────────────────────────────────────

func getJSON(url, auth string, into any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d: %s", url, resp.StatusCode, shorten.To(string(body), 200))
	}
	return json.Unmarshal(body, into)
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
