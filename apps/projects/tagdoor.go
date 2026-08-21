package projects

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// tagdoor.go — GET /v1/projects/tags, the PUBLIC per-site browser-tag config the
// hosted tag (track.js / /v1/event.js) fetches to know which client-side pixels to
// inject. Under /v1/projects because the door follows the store it reads, which is
// this app's (HIP-0139 §3.1).
//
// It lives HERE, in the projects app, because a project IS a site and carries its own
// Project.Tags — and this is the process that OWNS the project store. (It first lived in
// the destinations app and read empty in production: destinations runs in a different
// process, so its cross-app reach for the store resolved to nil. The rule the key
// resolver already follows: the door that reads the project store must be served by the
// process that holds it.)
//
// Dual-resolved per site: by the publishable KEY when it is a per-site project key
// (ResolveKey), else by the request HOST (ResolveHost) for an org-level key — so
// hanzo.ai and hanzo.chat inject different pixels under one org. Public + pk--keyed like
// /v1/event(.js); NON-SECRET ids only; FAIL-SAFE to an empty set so a page never breaks.

// browserTags names the platforms with a client-side pixel track.js can inject, and the
// injector `type` it dispatches on. A platform absent here forwards server-side only.
var browserTags = map[string]string{
	"ga4":    "ga",
	"meta":   "meta",
	"tiktok": "tiktok",
	"x":      "x",
}

type browserTagOut struct {
	Platform string `json:"platform"`
	Type     string `json:"type"`
	ID       string `json:"id"`
}

type tagConfig struct {
	Tags []browserTagOut `json:"tags"`
}

func init() {
	openapi.Register("/v1/projects/tags", http.MethodGet, nil, tagConfig{})
	openapi.Describe("/v1/projects/tags", http.MethodGet,
		"The site's browser tag set for the hosted tag — which pixels to inject, by publishable key",
		"Returns the client-side pixels the SITE has connected (GA/Meta/TikTok/X) with their "+
			"NON-SECRET ids, so the hosted tag injects them first-party and stamps each browser event "+
			"with the same event_id the server-side Conversions API uses — deduping the two. Resolved "+
			"per site: by the publishable key on ?key= when it names a project, else by the request "+
			"host, so hanzo.ai and hanzo.chat carry different tags under one org. WITHOUT a resolvable "+
			"site it answers an empty set at 200 — a page never breaks on its tag config.")
}

// mountTagDoor registers GET /v1/projects/tags as a public, raw net/http handler
// (like analytics' /v1/event.js) that reads THIS process's project store directly.
func mountTagDoor(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/projects/tags", zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveTags(s, w, r)
	})))
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
			continue
		}
		if id := strings.TrimSpace(tags[platform]); id != "" {
			out = append(out, browserTagOut{Platform: platform, Type: typ, ID: id})
		}
	}
	return out
}

// tagsKey lifts the publishable key: Authorization: Bearer first, then ?key=, then ?ingest_key=.
func tagsKey(r *http.Request) string {
	if b := r.Header.Get("Authorization"); strings.HasPrefix(b, "Bearer ") {
		if k := strings.TrimSpace(strings.TrimPrefix(b, "Bearer ")); k != "" {
			return k
		}
	}
	q := r.URL.Query()
	if k := strings.TrimSpace(q.Get("key")); k != "" {
		return k
	}
	return strings.TrimSpace(q.Get("ingest_key"))
}

// tagsHost lifts the site host for the org-key derivation path: ?host= first, else the
// Origin, else the Referer — reduced to a bare hostname.
func tagsHost(r *http.Request) string {
	q := r.URL.Query()
	for _, raw := range []string{q.Get("host"), r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if h := hostnameOf(raw); h != "" {
			return h
		}
	}
	return ""
}

func hostnameOf(raw string) string {
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

// resolveSiteTags dual-resolves the site and returns its Project.Tags, reading THIS
// process's store directly (in-process; no cross-process reach). nil,false when nothing
// resolves — the door then answers an empty set.
func resolveSiteTags(s *cloud.Service[state], r *http.Request) (map[string]string, bool) {
	ctx := r.Context()
	if key := tagsKey(r); key != "" {
		if p, err := s.State.store.ResolveKey(ctx, key); err == nil {
			return p.Tags, true
		}
	}
	if host := tagsHost(r); host != "" {
		if p, err := s.State.store.ResolveHost(ctx, host); err == nil {
			return p.Tags, true
		}
	}
	return nil, false
}

// serveTags writes the site's browser tag config. Public + cross-origin, non-secret,
// fail-safe. A raw net/http handler so it sets CORS + cache directly, like /v1/event.js.
func serveTags(s *cloud.Service[state], w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=60")

	out := tagConfig{Tags: []browserTagOut{}}
	if tags, ok := resolveSiteTags(s, r); ok {
		out.Tags = buildTags(tags)
	}
	body, err := json.Marshal(out)
	if err != nil {
		body = []byte(`{"tags":[]}`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
