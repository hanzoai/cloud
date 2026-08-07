package destinations

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/apps/projects"
	"github.com/hanzoai/cloud/openapi"
)

// tags.go — GET /v1/tags, the PUBLIC per-site browser-tag config the hosted tag
// (track.js) fetches to know which client-side pixels to inject.
//
// PER SITE, not per org: a project IS a site and carries its own pixel ids
// (projects.Project.Tags); track.js is dual-resolved to the right site — by the
// publishable KEY when it is a per-site project key, else by the request HOST (an
// org-level key + where the tag runs). So hanzo.ai and hanzo.chat inject different pixels
// under one org, and the server-side CAPI reads the same per-site ids. Public + pk--keyed
// like /v1/event(.js); non-secret ids only; FAIL-SAFE to an empty set so a page never
// breaks on its tag config.
//
// GATEWAY: like /v1/event(.js) this must be reachable without a validated session — the
// browser presents only the publishable key — so it rides the same public allowlist.

// browserTags names the platforms with a client-side pixel track.js can inject, and the
// injector `type` it dispatches on. A platform absent here forwards server-side only.
var browserTags = map[string]string{
	"ga4":    "ga",     // gtag('config', G-…)
	"meta":   "meta",   // fbq('init', …)
	"tiktok": "tiktok", // ttq.load(…)
	"x":      "x",      // twq('config', …)
}

// browserTagOut is one injectable tag on the wire: the platform, the injector type, the id.
type browserTagOut struct {
	Platform string `json:"platform"`
	Type     string `json:"type"`
	ID       string `json:"id"`
}

// tagConfig is the /v1/tags response: the site's injectable browser tags. Never a secret.
type tagConfig struct {
	Tags []browserTagOut `json:"tags"`
}

func init() {
	openapi.Register("/v1/tags", http.MethodGet, nil, tagConfig{})
	openapi.Describe("/v1/tags", http.MethodGet,
		"The site's browser tag set for the hosted tag — which pixels to inject, by publishable key",
		"Returns the client-side pixels the SITE has connected (GA/Meta/TikTok/X) with their "+
			"NON-SECRET ids, so the hosted tag injects them first-party and stamps each browser event "+
			"with the same event_id the server-side Conversions API uses — deduping the two. Resolved "+
			"per site: by the publishable key on ?key= when it names a project, else by the request "+
			"host, so hanzo.ai and hanzo.chat carry different tags under one org. WITHOUT a resolvable "+
			"site it answers an empty set at 200 — a page never breaks on its tag config.")
}

// buildTags maps a site's tag config (platform → non-secret pixel id) to injectable
// browser tags, in stable platform order. A platform with no client pixel, or an empty
// id, is omitted.
func buildTags(tags map[string]string) []browserTagOut {
	platforms := make([]string, 0, len(tags))
	for p := range tags {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	out := make([]browserTagOut, 0, len(platforms))
	for _, platform := range platforms {
		typ, ok := browserTags[platform]
		if !ok {
			continue // server-side-only destination — no browser pixel to inject
		}
		if id := strings.TrimSpace(tags[platform]); id != "" {
			out = append(out, browserTagOut{Platform: platform, Type: typ, ID: id})
		}
	}
	return out
}

// tagsKey lifts the publishable key: Authorization: Bearer first, then ?key= (the tag's
// data-key), then the retiring ?ingest_key=.
func tagsKey(r *http.Request) string {
	if b := r.Header.Get("Authorization"); strings.HasPrefix(b, "Bearer ") {
		if k := strings.TrimSpace(strings.TrimPrefix(b, "Bearer ")); k != "" {
			return k
		}
	}
	q := r.URL.Query()
	return firstNonEmpty(strings.TrimSpace(q.Get("key")), strings.TrimSpace(q.Get("ingest_key")))
}

// tagsHost lifts the site host for the org-key derivation path: ?host= first, else the
// Origin, else the Referer — reduced to a bare hostname.
func tagsHost(r *http.Request) string {
	q := r.URL.Query()
	for _, raw := range []string{q.Get("host"), r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if h := hostname(raw); h != "" {
			return h
		}
	}
	return ""
}

// hostname reduces an origin/URL/bare-host to its hostname ("" if none).
func hostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			return u.Hostname()
		}
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, ':'); i > 0 {
		raw = raw[:i]
	}
	return raw
}

// serveTags writes the site's browser tag config. Public + cross-origin (the tag loads
// from every property), non-secret, fail-safe. A raw net/http handler so it sets CORS +
// cache directly, exactly like /v1/event.js. Dual-resolves the site via projects.TagsFor.
func serveTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=60")

	out := tagConfig{Tags: []browserTagOut{}}
	if tags, ok := projects.TagsFor(r.Context(), tagsKey(r), tagsHost(r)); ok {
		out.Tags = buildTags(tags)
	}
	body, err := json.Marshal(out)
	if err != nil {
		body = []byte(`{"tags":[]}`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
