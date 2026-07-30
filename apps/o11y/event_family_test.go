package o11y

import (
	"net/http"
	"testing"
)

// TestEventFamilyCarriesIngestOnly pins the /v1/event/error wire: both Sentry
// spellings map onto their runtime ingest routes, and nothing else — no READ
// API is reachable through the family, so the principal gate's two ingest
// exemptions remain the only exemptions.
func TestEventFamilyCarriesIngestOnly(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
		ok           bool
	}{
		// Real Sentry SDKs expand DSN …/v1/event/error/<project> with an "api" segment.
		{http.MethodPost, "/v1/event/error/api/42/envelope/", "/v1/o11y/api/42/envelope/", true},
		{http.MethodPost, "/v1/event/error/api/42/store/", "/v1/o11y/api/42/store/", true},
		// The bare form maps onto the /v1/sentry routes (project is a UUID there).
		{http.MethodPost, "/v1/event/error/6ba7b810-9dad-11d1-80b4-00c04fd430c8/envelope/", "/v1/sentry/6ba7b810-9dad-11d1-80b4-00c04fd430c8/envelope/", true},
		// Ingest is POST; reads and probes do not pass.
		{http.MethodGet, "/v1/event/error/api/42/envelope/", "", false},
		{http.MethodPost, "/v1/event/error/api/v1/query_range", "", false},
		{http.MethodPost, "/v1/event/error/issues", "", false},
		{http.MethodPost, "/v1/event/ingestion", "", false},
		{http.MethodPost, "/v1/event", "", false},
	}
	for _, c := range cases {
		got, ok := eventToRuntimePath(c.method, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("eventToRuntimePath(%s %s) = (%q,%v), want (%q,%v)", c.method, c.path, got, ok, c.want, c.ok)
		}
	}
}
