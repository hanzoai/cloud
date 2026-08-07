package destinations

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// tags.go — GET /v1/tags, the PUBLIC browser-tag config the hosted tag (track.js /
// /v1/event.js) fetches to know which client-side pixels to inject for an org.
//
// It is the CLIENT half of the one config: an org connects a destination once (POST
// /v1/destinations/:platform — ids in the store, secret in KMS), and that single row
// drives BOTH planes — the server-side CAPI fan-out (fanout.go) AND, for the platforms
// that also have a browser pixel, this endpoint, so the browser tag and the server
// conversion carry the same ids and can never drift.
//
// Public and pk--keyed like /v1/event.js and POST /v1/event: it self-resolves the org
// from the publishable key, returns ONLY non-secret ids (a measurement/pixel id ships
// in the page anyway), and is FAIL-SAFE — no key, an unknown key, or a store miss all
// answer 200 with an empty tag set, so a page never breaks on its telemetry config.
//
// GATEWAY: like /v1/event(.js), this path must be reachable without a validated session
// (the browser presents only the publishable key), so it belongs on the same public
// allowlist those two already ride.

// browserTag names a platform's client-side pixel: the tag `type` track.js switches on
// to inject it, and the Config field (set at connect) that holds its id. Only platforms
// whose server-side connect id DOUBLES as the browser tag id are here; the rest forward
// server-side only (the unblockable path) until a distinct browser-tag id is captured.
type browserTag struct {
	Type      string // the injector track.js dispatches on: ga | meta | tiktok | x
	ConfigKey string // the DestinationField key whose value is the browser tag id
}

var browserTags = map[string]browserTag{
	"ga4":    {Type: "ga", ConfigKey: "measurementId"}, // gtag('config', G-…)
	"meta":   {Type: "meta", ConfigKey: "pixelId"},     // fbq('init', …)
	"tiktok": {Type: "tiktok", ConfigKey: "pixelCode"}, // ttq.load(…)
	"x":      {Type: "x", ConfigKey: "pixelId"},        // twq('config', …)
}

// browserTagOut is one injectable tag on the wire: the platform, the injector type, and
// the non-secret id.
type browserTagOut struct {
	Platform string `json:"platform"`
	Type     string `json:"type"`
	ID       string `json:"id"`
}

// tagConfig is the /v1/tags response: the org's injectable browser tags. Never a secret.
type tagConfig struct {
	Tags []browserTagOut `json:"tags"`
}

func init() {
	openapi.Register("/v1/tags", http.MethodGet, nil, tagConfig{})
	openapi.Describe("/v1/tags", http.MethodGet,
		"The org's browser tag set for the hosted tag — which pixels to inject, by publishable key",
		"Returns the client-side pixels the org has connected (GA, Meta, TikTok, X) with their "+
			"NON-SECRET ids, so the hosted tag (/v1/event.js) injects them first-party and stamps each "+
			"browser event with the same event_id the server-side Conversions API uses — deduping the "+
			"two. Keyed by the publishable key on ?key= (or Authorization: Bearer); it returns only ids "+
			"that already ship in the page, never a credential. WITHOUT A VALID KEY it answers an empty "+
			"set at 200 — a page never breaks on its tag config. The same connected destination also "+
			"drives the server-side fan-out, so the browser tag and the server conversion never drift.")
}

// buildTags maps an org's ENABLED destination rows to the injectable browser tags. Pure:
// a platform with no browser pixel, or a connected one whose browser-tag id is unset, is
// omitted. Order follows the input (ListEnabled is platform-sorted).
func buildTags(rows []Row) []browserTagOut {
	out := make([]browserTagOut, 0, len(rows))
	for _, row := range rows {
		bt, ok := browserTags[row.Platform]
		if !ok {
			continue // server-side-only destination — no browser pixel to inject
		}
		if id := row.Config.get(bt.ConfigKey); id != "" {
			out = append(out, browserTagOut{Platform: row.Platform, Type: bt.Type, ID: id})
		}
	}
	return out
}

// tagsKey lifts the publishable key from the request: Authorization: Bearer first, then
// ?key= (the hosted tag's data-key), then the retiring ?ingest_key=.
func tagsKey(r *http.Request) string {
	if b := r.Header.Get("Authorization"); strings.HasPrefix(b, "Bearer ") {
		if k := strings.TrimSpace(strings.TrimPrefix(b, "Bearer ")); k != "" {
			return k
		}
	}
	q := r.URL.Query()
	return firstNonEmpty(strings.TrimSpace(q.Get("key")), strings.TrimSpace(q.Get("ingest_key")))
}

// serveTags writes the org's browser tag config. Public + cross-origin (the tag is
// loaded from every property), non-secret, fail-safe. A raw net/http handler so it sets
// CORS + cache headers directly, exactly like /v1/event.js.
func serveTags(s *cloud.Service[state], w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=60")

	out := tagConfig{Tags: []browserTagOut{}}
	if key := tagsKey(r); key != "" {
		if org := cloud.ResolvePublishableKeyOrg(r.Context(), key); org != "" && validOrg(org) {
			if rows, err := s.State.store.ListEnabled(r.Context(), org); err == nil {
				out.Tags = buildTags(rows)
			}
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		body = []byte(`{"tags":[]}`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
